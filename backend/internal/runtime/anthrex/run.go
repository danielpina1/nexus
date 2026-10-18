package anthrexrt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/anthrex"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Start implements kernel.Runtime: it starts the Anthrex goal and returns a
// run that mirrors it.
func (r *Runtime) Start(ctx context.Context, spec kernel.RunSpec) (kernel.Run, error) {
	a := spec.Assignment
	if a == nil {
		return nil, errors.New("anthrex: missing assignment")
	}
	goal, err := r.cfg.goalRequest(a)
	if err != nil {
		return nil, err
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, fmt.Errorf("anthrex: %w", err)
	}
	run := newRun(r, api, a, spec)
	go run.main(goal)
	return run, nil
}

// gateLevels: Anthrex's plan, hold and design-document gates are COMPUTE
// actions, which policy runs AUTO by default; on this runtime they are the
// owner's decision, so COMPUTE is raised to APPROVE (an agent's policy can
// raise it further, to BIOMETRIC, never lower it).
func gateLevels() map[nexusv1.ActionClass]nexusv1.TrustLevel {
	m := make(map[nexusv1.ActionClass]nexusv1.TrustLevel, len(approvalgate.Defaults))
	for k, v := range approvalgate.Defaults {
		m[k] = v
	}
	m[nexusv1.ActionClass_ACTION_CLASS_COMPUTE] = nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
	return m
}

type run struct {
	rt   *Runtime
	api  anthrex.API
	a    *nexusv1.RunAssignment
	log  *slog.Logger
	gate *approvalgate.Gate

	ctx    context.Context
	cancel context.CancelFunc

	events    chan *nexusv1.Event
	emitMu    sync.Mutex
	finished  bool
	finishing bool
	costMu    sync.Mutex

	mu        sync.Mutex
	runID     string // Anthrex run id
	questions map[string]chan *nexusv1.RunInput
	gates     map[string]context.CancelFunc // open gate key → cancel
	handled   map[string]bool
	last      *anthrex.RunInfo
	cancelled bool

	// snapshot-processing state (main goroutine only)
	taskStates map[string]string
	attention  map[string]bool
	runState   string
	planGen    int
	finishGen  int
	reported   anthrex.TokenUsage
	lastCostAt time.Time
	total      *nexusv1.Cost
	seen       bool
	missing    int
}

func newRun(rt *Runtime, api anthrex.API, a *nexusv1.RunAssignment, spec kernel.RunSpec) *run {
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{
		rt: rt, api: api, a: a, ctx: ctx, cancel: cancel,
		log:       rt.host.Logger().With("runtime", "anthrex", "run_id", a.GetRunId()),
		events:    make(chan *nexusv1.Event, 512),
		questions: map[string]chan *nexusv1.RunInput{},
		gates:     map[string]context.CancelFunc{}, handled: map[string]bool{},
		taskStates: map[string]string{}, attention: map[string]bool{},
		total: &nexusv1.Cost{Runtime: "anthrex"},
	}
	r.gate = approvalgate.New(approvalgate.Config{
		TaskID: a.GetTaskId(), RunID: a.GetRunId(), Agent: a.GetAgent(), NodeID: spec.Env["NEXUS_NODE_ID"],
		Emit: r.emit, Levels: gateLevels(), Log: r.log,
	})
	agentloop.BindGate(spec.Gate, r.gate)
	return r
}

// ---------------------------------------------------------------- kernel.Run

func (r *run) Events() <-chan *nexusv1.Event { return r.events }

func (r *run) Resolve(ctx context.Context, res *nexusv1.ApprovalResolution) error {
	return r.gate.Resolve(ctx, res)
}

func (r *run) Send(ctx context.Context, in *nexusv1.RunInput) error {
	if qid := in.GetQuestionId(); qid != "" {
		r.mu.Lock()
		ch := r.questions[qid]
		r.mu.Unlock()
		if ch == nil {
			return fmt.Errorf("anthrex: no pending question %q", qid)
		}
		select {
		case ch <- in:
			return nil
		default:
			return fmt.Errorf("anthrex: question %q already answered", qid)
		}
	}
	text := strings.TrimSpace(in.GetText())
	if text == "" {
		return errors.New("anthrex: empty message")
	}
	r.mu.Lock()
	id, info := r.runID, r.last
	r.mu.Unlock()
	if id == "" {
		return errors.New("anthrex: the run has not started yet")
	}
	// A message to a live orchestrator is typed into its window (it is an
	// interactive agent session); otherwise it goes to the running workers.
	if info != nil && info.Orchestrator != nil && info.Orchestrator.Live && info.Orchestrator.WindowID != nil && r.api.Mode() == "socket" {
		if err := r.api.SendInput(ctx, *info.Orchestrator.WindowID, []byte(text+"\r")); err != nil {
			return err
		}
		r.output("message typed to the Anthrex orchestrator")
		return nil
	}
	msg, err := r.api.Message(ctx, id, "running", text, anthrex.MessageInfo)
	if err != nil {
		return err
	}
	r.output("message to running workers: " + nonEmpty(msg, "sent"))
	return nil
}

func (r *run) Cancel(ctx context.Context) error {
	r.mu.Lock()
	r.cancelled = true
	id, info := r.runID, r.last
	r.mu.Unlock()
	var err error
	if id != "" && (info == nil || !anthrex.RunStateTerminal(info.State)) {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = r.api.Cancel(cctx, id)
		cancel()
	}
	r.finish(nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled", "", r.resultMap(info, nil))
	return err
}

// ---------------------------------------------------------------- events

func (r *run) emit(ev *nexusv1.Event) {
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	if r.finished {
		return
	}
	ev.TaskId, ev.RunId = r.a.GetTaskId(), r.a.GetRunId()
	if ev.At == nil {
		ev.At = timestamppb.Now()
	}
	r.events <- ev
}

func (r *run) output(line string) {
	r.emit(agentloop.OutputEvent(r.a.GetTaskId(), r.a.GetRunId(), line+"\n", false))
}

func (r *run) state(s nexusv1.TaskState) {
	r.emit(&nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{
		Id: r.a.GetTaskId(), CurrentRunId: r.a.GetRunId(), State: s,
	}}}})
}

// finish emits the terminal TaskUpdated once and stops everything.
func (r *run) finish(state nexusv1.TaskState, summary, errMsg string, result map[string]any) {
	r.emitMu.Lock()
	if r.finished || r.finishing {
		r.emitMu.Unlock()
		return
	}
	r.finishing = true
	r.emitMu.Unlock()
	r.flushCost()
	if errMsg != "" {
		if result == nil {
			result = map[string]any{}
		}
		result["error"] = errMsg
	}
	r.costMu.Lock()
	total := r.total
	r.costMu.Unlock()
	t := &nexusv1.Task{Id: r.a.GetTaskId(), CurrentRunId: r.a.GetRunId(), State: state, Summary: summary, Cost: total}
	if result != nil {
		if st, err := structpb.NewStruct(result); err == nil {
			t.Result = st
		} else {
			r.log.Warn("anthrex: result not encodable", "err", err)
		}
	}
	r.emit(&nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: t}}})
	r.emitMu.Lock()
	r.finished = true
	close(r.events)
	r.emitMu.Unlock()
	r.cancel()
	r.gate.Close()
}

func (r *run) isFinished() bool {
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	return r.finished
}

// ---------------------------------------------------------------- main loop

func (r *run) main(goal anthrex.StartGoalRequest) {
	defer func() {
		if !r.isFinished() {
			r.finish(nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled", "", nil)
		}
	}()
	r.state(nexusv1.TaskState_TASK_STATE_RUNNING)
	r.output(fmt.Sprintf("Anthrex goal in %s (delivery %s, design %s, plan approval %s)",
		goal.Dir, goal.Delivery, goal.Design, map[bool]string{true: "auto", false: "approve"}[goal.Yes]))
	res, err := r.api.StartGoal(r.ctx, goal)
	if err != nil {
		if r.ctx.Err() != nil {
			return
		}
		r.finish(nexusv1.TaskState_TASK_STATE_FAILED, "Anthrex refused the goal: "+err.Error(), err.Error(), nil)
		return
	}
	snaps, err := r.api.WatchRuns(r.ctx)
	if err != nil {
		r.finish(nexusv1.TaskState_TASK_STATE_FAILED, "cannot follow the Anthrex run: "+err.Error(), err.Error(), nil)
		return
	}
	var queued *anthrex.QueuedGoalInfo
	if res.Queued {
		r.output("goal queued: " + nonEmpty(res.Message, "the repository's Anthrex profile is being set up"))
		queued = &anthrex.QueuedGoalInfo{ID: res.GoalID, Project: goal.Dir, Goal: goal.Goal, QueuedAt: uint64(time.Now().Unix()) - 5}
	} else {
		r.setRunID(res.RunID)
		r.output("Anthrex run " + res.RunID + " started" + suffix(": ", res.Message))
	}
	for snap := range snaps {
		if r.isFinished() {
			return
		}
		if queued != nil {
			if !r.adopt(snap, queued) {
				continue
			}
			queued = nil
		}
		if done := r.process(snap); done {
			return
		}
	}
	if r.ctx.Err() == nil {
		r.finish(nexusv1.TaskState_TASK_STATE_FAILED, "lost the Anthrex daemon", "anthrex: run watch ended", r.resultMap(r.lastInfo(), nil))
	}
}

func suffix(sep, s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return sep + strings.TrimSpace(s)
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return strings.TrimSpace(s)
}

func (r *run) setRunID(id string) {
	r.mu.Lock()
	r.runID = id
	r.mu.Unlock()
}

func (r *run) lastInfo() *anthrex.RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// adopt follows a queued goal until it becomes a run (same goal and
// project, created after it was queued). It reports true once adopted.
func (r *run) adopt(snap *anthrex.RunsSnapshot, q *anthrex.QueuedGoalInfo) bool {
	for _, g := range snap.QueuedGoals {
		if (q.ID != "" && g.ID == q.ID) || (q.ID == "" && g.Goal == q.Goal) {
			if q.ID == "" {
				q.ID = g.ID
			}
			key := "setup:" + g.Setup.State
			if !r.handled[key] {
				r.handled[key] = true
				switch g.Setup.State {
				case "needs_review":
					r.output("the repository's Anthrex profile needs review: run `anthrex profile show --proposed` and `anthrex profile confirm` in " + g.Project)
				case "failed":
					r.finish(nexusv1.TaskState_TASK_STATE_FAILED, "Anthrex could not set up the repository profile: "+g.Setup.Reason, g.Setup.Reason, nil)
					return false
				default:
					r.output("repository profile: " + g.Setup.State)
				}
			}
			return false
		}
	}
	var best *anthrex.RunInfo
	for i := range snap.Runs {
		c := &snap.Runs[i]
		if c.Goal == q.Goal && c.CreatedAt >= q.QueuedAt && (best == nil || c.CreatedAt > best.CreatedAt) {
			best = c
		}
	}
	if best == nil {
		return false
	}
	r.setRunID(best.RunID)
	r.output("queued goal started as Anthrex run " + best.RunID)
	return true
}

// process mirrors one snapshot; it reports true when the run is over.
func (r *run) process(snap *anthrex.RunsSnapshot) bool {
	r.mu.Lock()
	id := r.runID
	r.mu.Unlock()
	info := snap.Run(id)
	if info == nil {
		if r.seen {
			r.missing++
			if r.missing >= 3 {
				r.finish(nexusv1.TaskState_TASK_STATE_FAILED, "the Anthrex run "+id+" is no longer listed", "anthrex: run vanished", r.resultMap(r.lastInfo(), nil))
				return true
			}
		}
		return false
	}
	r.seen, r.missing = true, 0
	r.mu.Lock()
	r.last = info
	r.mu.Unlock()

	if info.State != r.runState {
		prev := r.runState
		r.runState = info.State
		line := "run " + info.State
		if info.HaltedReason != nil && info.State == anthrex.RunHalted {
			line += ": " + *info.HaltedReason
		}
		r.output(line)
		if info.State == anthrex.RunAwaitingApproval {
			r.planGen++
		}
		if info.State == anthrex.RunComplete && prev != anthrex.RunComplete {
			r.finishGen++
		}
	}
	r.mirrorTasks(info)
	for _, a := range info.Attention {
		if !r.attention[a] {
			r.attention[a] = true
			r.output("attention: " + a)
		}
	}
	r.cost(info, false)
	if anthrex.RunStateTerminal(info.State) {
		r.terminal(info)
		return true
	}
	r.gatesFor(info)
	return false
}

func (r *run) callID(taskID string) string { return "ax_" + r.runID + "_" + taskID }

func (r *run) mirrorTasks(info *anthrex.RunInfo) {
	for i := range info.Tasks {
		t := &info.Tasks[i]
		prev, seen := r.taskStates[t.ID]
		if seen && prev == t.State {
			continue
		}
		r.taskStates[t.ID] = t.State
		role := t.Role()
		if !seen {
			in := map[string]any{"task_id": t.ID, "title": t.Title, "kind": t.Kind, "size": t.Size,
				"state": t.State, "runtime": t.Route.Runtime, "model": t.Route.Model}
			if role != "" {
				in["role"] = role
			}
			r.emit(&nexusv1.Event{Body: &nexusv1.Event_ToolCall{ToolCall: &nexusv1.ToolCall{
				CallId: r.callID(t.ID), Tool: "anthrex.task", Input: mustStruct(in),
				ActionClass: nexusv1.ActionClass_ACTION_CLASS_COMPUTE,
			}}})
			r.output(fmt.Sprintf("task %s %q [%s] %s", t.ID, t.Title, nonEmpty(role, t.Kind), t.State))
		} else {
			line := fmt.Sprintf("task %s: %s → %s", t.ID, prev, t.State)
			if t.Block != nil && t.State == anthrex.TaskBlocked {
				line += " (" + t.Block.Reason + ": " + t.Block.Text + ")"
			} else if t.Activity != nil && *t.Activity != "" {
				line += " — " + *t.Activity
			}
			r.output(line)
		}
		if anthrex.TaskStateFinal(t.State) && !anthrex.TaskStateFinal(prev) {
			u := t.Usage()
			out := map[string]any{"state": t.State, "tokens": float64(u.Billable()), "tool_calls": float64(t.SpentTotal.ToolCalls)}
			if t.MergeCommit != nil {
				out["merge_commit"] = *t.MergeCommit
			}
			if t.Diff != nil {
				out["files"], out["added"], out["removed"] = float64(t.Diff.Files), float64(t.Diff.Added), float64(t.Diff.Removed)
			}
			errMsg := ""
			if t.State == anthrex.TaskCancelled {
				errMsg = "cancelled"
			}
			r.emit(&nexusv1.Event{Body: &nexusv1.Event_ToolResult{ToolResult: &nexusv1.ToolResult{
				CallId: r.callID(t.ID), Output: mustStruct(out), Error: errMsg,
				DurationMs: int64(t.SpentTotal.Secs) * 1000,
			}}})
		}
	}
}

func mustStruct(m map[string]any) *structpb.Struct {
	s, err := structpb.NewStruct(m)
	if err != nil {
		s, _ = structpb.NewStruct(map[string]any{"error": err.Error()})
	}
	return s
}

// runUsage is the run's token total: the engine's own, else the sum of its
// tasks' rounds.
func runUsage(info *anthrex.RunInfo) anthrex.TokenUsage {
	if info.Usage != nil {
		return info.Usage.Total
	}
	var u anthrex.TokenUsage
	for i := range info.Tasks {
		u = u.Add(info.Tasks[i].Usage())
	}
	return u
}

// cost emits CostRecorded for usage growth, at most every 15 s (always
// when final).
func (r *run) cost(info *anthrex.RunInfo, final bool) {
	r.costMu.Lock()
	defer r.costMu.Unlock()
	u := runUsage(info)
	if u.Billable() <= r.reported.Billable() {
		return
	}
	if !final && time.Since(r.lastCostAt) < 15*time.Second {
		return
	}
	d := anthrex.TokenUsage{
		Input: sub(u.Input, r.reported.Input), Output: sub(u.Output, r.reported.Output),
		CacheWrite: sub(u.CacheWrite, r.reported.CacheWrite),
	}
	r.reported, r.lastCostAt = u, time.Now()
	c := &nexusv1.Cost{InputTokens: int64(d.Input + d.CacheWrite), OutputTokens: int64(d.Output), Runtime: "anthrex"}
	if info.Orchestrator != nil {
		c.Model = info.Orchestrator.Route.Model
	}
	agentloop.AddCost(r.total, c)
	r.emit(agentloop.CostEvent(r.a.GetTaskId(), r.a.GetRunId(), c))
}

func sub(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return 0
}

func (r *run) flushCost() {
	if info := r.lastInfo(); info != nil {
		r.cost(info, true)
	}
}

// ---------------------------------------------------------------- terminal

func (r *run) readReport(info *anthrex.RunInfo) string {
	if info == nil || info.ReportPath == "" {
		return ""
	}
	f, err := os.Open(info.ReportPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, r.rt.cfg.ReportMaxBytes+1)
	n, _ := f.Read(buf)
	for n < len(buf) {
		m, err := f.Read(buf[n:])
		n += m
		if err != nil || m == 0 {
			break
		}
	}
	s := string(buf[:n])
	if n > r.rt.cfg.ReportMaxBytes {
		s = s[:r.rt.cfg.ReportMaxBytes] + "\n\n…(REPORT.md truncated)"
	}
	return s
}

func (r *run) resultMap(info *anthrex.RunInfo, extra map[string]any) map[string]any {
	m := map[string]any{"runtime": "anthrex"}
	if info == nil {
		r.mu.Lock()
		if r.runID != "" {
			m["anthrex_run_id"] = r.runID
		}
		r.mu.Unlock()
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	m["anthrex_run_id"] = info.RunID
	m["state"] = info.State
	m["goal"] = info.Goal
	m["title"] = info.DisplayTitle()
	m["project"] = info.Project
	m["base_branch"] = info.BaseBranch
	m["run_branch"] = info.RunBranch
	m["run_head"] = info.RunHead
	m["report_path"] = info.ReportPath
	if info.Outcome != nil {
		m["outcome"] = *info.Outcome
	}
	if info.ResearchReport != nil {
		m["research_report"] = *info.ResearchReport
	}
	var tasks []any
	merged := 0
	for i := range info.Tasks {
		t := &info.Tasks[i]
		if t.State == anthrex.TaskMerged || t.State == anthrex.TaskReported {
			merged++
		}
		tasks = append(tasks, map[string]any{
			"id": t.ID, "title": t.Title, "state": t.State, "role": t.Role(),
			"tokens": float64(t.Usage().Billable()),
		})
	}
	m["tasks"] = tasks
	m["tasks_done"] = float64(merged)
	u := runUsage(info)
	m["usage"] = map[string]any{"input": float64(u.Input), "output": float64(u.Output),
		"cache_read": float64(u.CacheRead), "cache_write": float64(u.CacheWrite)}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func (r *run) terminal(info *anthrex.RunInfo) {
	r.closeGates()
	report := r.readReport(info)
	if report != "" {
		r.emit(agentloop.ArtifactEvent(r.a.GetTaskId(), r.a.GetRunId(), &nexusv1.Attachment{
			Id: "att_" + ulid.Make().String(), Name: "REPORT.md", MimeType: "text/markdown",
			SizeBytes: int64(len(report)), Data: []byte(report),
		}))
	}
	head := fmt.Sprintf("Anthrex run %s (%s) %s.", info.RunID, info.DisplayTitle(), info.State)
	if info.Outcome != nil && *info.Outcome != "" {
		head += " " + *info.Outcome
	}
	summary := head
	if report != "" {
		summary += "\n\n" + report
	}
	r.emit(agentloop.OutputEvent(r.a.GetTaskId(), r.a.GetRunId(), summary, true))
	extra := map[string]any{}
	if report != "" {
		extra["report"] = report
	}
	res := r.resultMap(info, extra)
	switch info.State {
	case anthrex.RunAccepted:
		r.finish(nexusv1.TaskState_TASK_STATE_SUCCEEDED, summary, "", res)
	case anthrex.RunDiscarded:
		r.finish(nexusv1.TaskState_TASK_STATE_CANCELLED, summary, "", res)
	default:
		reason := "the Anthrex run failed"
		if info.HaltedReason != nil {
			reason = *info.HaltedReason
		} else if info.Outcome != nil {
			reason = *info.Outcome
		}
		r.finish(nexusv1.TaskState_TASK_STATE_FAILED, summary, reason, res)
	}
}

// ---------------------------------------------------------------- gates

func (r *run) closeGates() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, c := range r.gates {
		c()
		delete(r.gates, k)
	}
}

// gatesFor opens a gate for every decision the run waits on, and closes
// gates whose condition went away (decided in Anthrex itself).
func (r *run) gatesFor(info *anthrex.RunInfo) {
	want := map[string]func(context.Context){}
	if info.State == anthrex.RunAwaitingApproval {
		want[fmt.Sprintf("plan:%d", r.planGen)] = func(ctx context.Context) { r.planGate(ctx, info) }
	}
	for _, h := range info.Holds {
		if h.State == anthrex.HoldAwaiting {
			h := h
			want["hold:"+h.ID] = func(ctx context.Context) { r.holdGate(ctx, info.RunID, h) }
		}
	}
	if g := info.DocGate; g != nil && g.Revising == nil {
		g := *g
		want[fmt.Sprintf("doc:%s:%d", g.Kind, g.Version)] = func(ctx context.Context) { r.docGate(ctx, info.RunID, g) }
	}
	if o := info.Orchestrator; o != nil && o.Ask != nil {
		ask, win := *o.Ask, o.WindowID
		want[fmt.Sprintf("ask:%d", ask.ID)] = func(ctx context.Context) { r.askGate(ctx, info.RunID, ask, win) }
	}
	if info.State == anthrex.RunComplete && info.Delivery == nil {
		snap := *info
		want[fmt.Sprintf("finish:%d", r.finishGen)] = func(ctx context.Context) { r.finishGate(ctx, &snap) }
	}
	if info.State == anthrex.RunHalted && info.HaltUserOnly {
		reason := ""
		if info.HaltedReason != nil {
			reason = *info.HaltedReason
		}
		want["halt:"+reason] = func(ctx context.Context) { r.haltGate(ctx, info.RunID, reason) }
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for k, c := range r.gates {
		if _, ok := want[k]; !ok {
			c() // decided elsewhere (Anthrex TUI/CLI): stop waiting
			delete(r.gates, k)
		}
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if r.handled[k] {
			continue
		}
		r.handled[k] = true
		gctx, cancel := context.WithCancel(r.ctx)
		r.gates[k] = cancel
		f, key := want[k], k
		go func() {
			defer func() {
				r.mu.Lock()
				if c, ok := r.gates[key]; ok {
					c()
					delete(r.gates, key)
				}
				r.mu.Unlock()
			}()
			f(gctx)
		}()
	}
}

// check asks through the approval gate; it reports approval, the refusal
// reason, and whether the wait was abandoned.
func (r *run) check(ctx context.Context, req approvalgate.Request) (bool, string, bool) {
	out, err := r.gate.Check(ctx, req)
	if err != nil {
		if !r.isFinished() {
			r.state(nexusv1.TaskState_TASK_STATE_RUNNING)
		}
		return false, "", true
	}
	if !out.Approved {
		r.gate.Resume()
	}
	return out.Approved, out.Reason, false
}

// ask emits a Question and waits for its answer.
func (r *run) ask(ctx context.Context, text string, choices []string, free bool) (*nexusv1.RunInput, bool) {
	qid := "q_" + ulid.Make().String()
	ch := make(chan *nexusv1.RunInput, 1)
	r.mu.Lock()
	r.questions[qid] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.questions, qid)
		r.mu.Unlock()
	}()
	r.emit(&nexusv1.Event{Body: &nexusv1.Event_Question{Question: &nexusv1.Question{
		QuestionId: qid, Text: text, Choices: choices, AllowFreeText: free,
	}}})
	r.state(nexusv1.TaskState_TASK_STATE_WAITING_INPUT)
	select {
	case in := <-ch:
		r.state(nexusv1.TaskState_TASK_STATE_RUNNING)
		return in, true
	case <-ctx.Done():
		if !r.isFinished() {
			r.state(nexusv1.TaskState_TASK_STATE_RUNNING)
		}
		return nil, false
	}
}

func (r *run) did(what string, msg string, err error) {
	if err != nil {
		r.output(what + " failed: " + err.Error())
		return
	}
	r.output(what + suffix(": ", msg))
}

func planSummary(info *anthrex.RunInfo) (string, []any) {
	var b strings.Builder
	var ids []any
	fmt.Fprintf(&b, "Goal: %s\nRepository: %s (base %s)\n\n%d tasks:\n", info.Goal, info.Project, info.BaseBranch, len(info.Tasks))
	for i := range info.Tasks {
		t := &info.Tasks[i]
		ids = append(ids, t.ID)
		fmt.Fprintf(&b, "- %s [%s %s] %s — %s/%s\n", t.ID, t.Kind, t.Size, t.Title, t.Route.Runtime, t.Route.Model)
	}
	return b.String(), ids
}

func (r *run) planGate(ctx context.Context, info *anthrex.RunInfo) {
	summary, ids := planSummary(info)
	ok, reason, abandoned := r.check(ctx, approvalgate.Request{
		Class: nexusv1.ActionClass_ACTION_CLASS_COMPUTE,
		Title: "Approve Anthrex plan: " + info.DisplayTitle(), Summary: summary,
		Payload: map[string]any{"anthrex_run": info.RunID, "action": "approve_plan", "goal": info.Goal,
			"project": info.Project, "base_branch": info.BaseBranch, "tasks": ids},
	})
	switch {
	case abandoned:
	case ok:
		msg, err := r.api.Approve(r.ctx, info.RunID)
		r.did("plan approved", msg, err)
	case reason == "denied":
		msg, err := r.api.Reject(r.ctx, info.RunID)
		r.did("plan rejected", msg, err)
	default:
		r.output("plan approval " + reason + "; approve or reject it in Anthrex (`anthrex run approve " + info.RunID + "`)")
	}
}

func (r *run) holdGate(ctx context.Context, runID string, h anthrex.HoldInfo) {
	what := h.Kind.Kind
	if h.Kind.Epic != "" {
		what += " " + h.Kind.Epic
	}
	tasks := make([]any, 0, len(h.Tasks))
	for _, t := range h.Tasks {
		tasks = append(tasks, t)
	}
	ok, reason, abandoned := r.check(ctx, approvalgate.Request{
		Class:   nexusv1.ActionClass_ACTION_CLASS_COMPUTE,
		Title:   "Approve Anthrex work added after the plan (" + what + ")",
		Summary: fmt.Sprintf("Run %s holds %d task(s) for approval: %s", runID, len(h.Tasks), strings.Join(h.Tasks, ", ")),
		Payload: map[string]any{"anthrex_run": runID, "action": "approve_hold", "hold": h.ID, "tasks": tasks},
	})
	switch {
	case abandoned:
	case ok:
		msg, err := r.api.ApproveHold(r.ctx, runID, h.ID)
		r.did("hold "+h.ID+" approved", msg, err)
	case reason == "denied":
		msg, err := r.api.RejectHold(r.ctx, runID, h.ID)
		r.did("hold "+h.ID+" rejected", msg, err)
	default:
		r.output("hold " + h.ID + " approval " + reason)
	}
}

func docText(v any) string {
	m, _ := v.(map[string]any)
	s, _ := m["text"].(string)
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…(truncated)"
}

func (r *run) docGate(ctx context.Context, runID string, g anthrex.DocGateInfo) {
	v := g.Version
	text := ""
	if doc, err := r.api.ShowDoc(ctx, runID, g.Kind, &v); err == nil {
		text = docText(doc)
	}
	summary := fmt.Sprintf("Anthrex wrote the %s (version %d) and waits at its design gate.", g.Kind, g.Version)
	if len(g.ChangesSummary) > 0 {
		summary += "\nChanges: " + strings.Join(g.ChangesSummary, "; ")
	}
	if g.NotReviewed != nil {
		summary += "\nNot reviewed: " + *g.NotReviewed
	}
	if text != "" {
		summary += "\n\n" + truncate(text, 6000)
	}
	ok, reason, abandoned := r.check(ctx, approvalgate.Request{
		Class: nexusv1.ActionClass_ACTION_CLASS_COMPUTE,
		Title: fmt.Sprintf("Approve the %s (v%d)", g.Kind, g.Version), Summary: summary,
		Payload: map[string]any{"anthrex_run": runID, "action": "approve_doc", "gate": g.Kind, "version": float64(g.Version)},
	})
	if abandoned {
		return
	}
	if ok {
		msg, err := r.api.DocGate(r.ctx, runID, g.Kind, anthrex.DocGateAction{Kind: "approve", Version: &v})
		r.did(g.Kind+" approved", msg, err)
		return
	}
	if reason != "denied" {
		r.output(g.Kind + " approval " + reason + "; answer the gate in Anthrex")
		return
	}
	alt := "back"
	if g.Kind == anthrex.GateBrainstorm {
		alt = "rethink"
	}
	in, answered := r.ask(ctx, fmt.Sprintf("You did not approve the %s v%d. What should Anthrex do? Pick %q (revise with your note), %q or %q; or just type what to change.", g.Kind, g.Version, "changes", alt, "reject"),
		[]string{"changes", alt, "reject"}, true)
	if !answered {
		return
	}
	choice, note := strings.ToLower(strings.TrimSpace(in.GetChoice())), strings.TrimSpace(in.GetText())
	if choice == "" {
		choice = "changes"
	}
	if choice != "reject" && note == "" {
		in2, ok := r.ask(ctx, "What should change in the "+g.Kind+"?", nil, true)
		if !ok {
			return
		}
		note = strings.TrimSpace(in2.GetText())
		if note == "" {
			note = strings.TrimSpace(in2.GetChoice())
		}
	}
	act := anthrex.DocGateAction{Kind: choice, Note: note}
	if choice != "changes" && choice != alt && choice != "reject" {
		act = anthrex.DocGateAction{Kind: "changes", Note: note}
	}
	msg, err := r.api.DocGate(r.ctx, runID, g.Kind, act)
	r.did(g.Kind+" "+act.Kind, msg, err)
}

func (r *run) askGate(ctx context.Context, runID string, ask anthrex.AskInfo, window *uint32) {
	text := "Anthrex orchestrator asks: " + ask.Question
	if ask.Context != "" {
		text += "\n\n" + ask.Context
	}
	in, ok := r.ask(ctx, text, ask.Options, true)
	if !ok {
		return
	}
	answer := strings.TrimSpace(in.GetChoice())
	if answer == "" {
		answer = strings.TrimSpace(in.GetText())
	}
	for i, o := range ask.Options {
		if strings.EqualFold(o, answer) || answer == itoa(i+1) {
			c := uint32(i)
			msg, err := r.api.AnswerAsk(r.ctx, runID, ask.ID, &c)
			r.did("answered the orchestrator", msg, err)
			return
		}
	}
	// A free-text answer is typed into the orchestrator's window, then the
	// question is marked answered (what the Anthrex TUI does on Enter).
	if window == nil {
		r.output("cannot deliver a free-text answer: the orchestrator has no window; answer in Anthrex")
		return
	}
	if err := r.api.SendInput(r.ctx, *window, []byte(answer+"\r")); err != nil {
		r.did("answer to the orchestrator", "", err)
		return
	}
	msg, err := r.api.AnswerAsk(r.ctx, runID, ask.ID, nil)
	r.did("answered the orchestrator in its window", msg, err)
}

func (r *run) haltGate(ctx context.Context, runID, reason string) {
	in, ok := r.ask(ctx, "The Anthrex run halted and only you can resume it: "+reason,
		[]string{"resume", "resume with rebaseline", "cancel"}, false)
	if !ok {
		return
	}
	switch in.GetChoice() {
	case "resume":
		msg, err := r.api.Resume(r.ctx, runID, false)
		r.did("resumed", msg, err)
	case "resume with rebaseline":
		msg, err := r.api.Resume(r.ctx, runID, true)
		r.did("resumed (rebaseline)", msg, err)
	case "cancel":
		msg, err := r.api.Cancel(r.ctx, runID)
		r.did("cancelled", msg, err)
	default:
		r.output("halt left as is")
	}
}

func diffTotals(info *anthrex.RunInfo) (files, added, removed uint32, done, checksFailed int) {
	for i := range info.Tasks {
		t := &info.Tasks[i]
		if t.Diff != nil && (t.State == anthrex.TaskMerged) {
			files += t.Diff.Files
			added += t.Diff.Added
			removed += t.Diff.Removed
		}
		if t.State == anthrex.TaskMerged || t.State == anthrex.TaskReported {
			done++
		}
		if t.LastCheck != nil && !t.LastCheck.OK {
			checksFailed++
		}
	}
	return
}

func (r *run) finishGate(ctx context.Context, info *anthrex.RunInfo) {
	files, added, removed, done, failed := diffTotals(info)
	var b strings.Builder
	fmt.Fprintf(&b, "Merge %s into %s in %s.\n", info.RunBranch, info.BaseBranch, info.Project)
	fmt.Fprintf(&b, "%d/%d tasks done; diff: %d files, +%d −%d.\n", done, len(info.Tasks), files, added, removed)
	if failed > 0 {
		fmt.Fprintf(&b, "Warning: %d task(s) last check failed.\n", failed)
	} else {
		b.WriteString("Checks: all passing.\n")
	}
	if info.RunHead == info.BaseSHA {
		b.WriteString("Nothing to merge (research/review run): accepting only records it and removes its branches.\n")
	}
	for _, a := range info.Attention {
		b.WriteString("Attention: " + a + "\n")
	}
	if rep := r.readReport(info); rep != "" {
		b.WriteString("\n" + truncate(rep, 4000))
	}
	payload := map[string]any{"anthrex_run": info.RunID, "action": "accept", "project": info.Project,
		"run_branch": info.RunBranch, "base_branch": info.BaseBranch, "base_sha": info.BaseSHA, "run_head": info.RunHead,
		"files": float64(files), "added": float64(added), "removed": float64(removed)}
	ok, reason, abandoned := r.check(ctx, approvalgate.Request{
		Class: nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH,
		Title: "Merge Anthrex run into " + info.BaseBranch + ": " + info.DisplayTitle(), Summary: b.String(), Payload: payload,
	})
	if abandoned {
		return
	}
	if ok {
		r.accept(ctx, info)
		return
	}
	if reason != "denied" {
		r.output("accept approval " + reason + "; the run stays complete in Anthrex (`anthrex run accept " + info.RunID + "`)")
		return
	}
	in, answered := r.ask(ctx, "Not merged. Keep the Anthrex run (accept later in Anthrex) or discard it (remove its worktrees and branches)?",
		[]string{"keep", "discard"}, false)
	if !answered {
		return
	}
	if in.GetChoice() != "discard" {
		r.finish(nexusv1.TaskState_TASK_STATE_SUCCEEDED,
			fmt.Sprintf("Anthrex run %s is complete but was not merged; accept it later with `anthrex run accept %s`.", info.RunID, info.RunID),
			"", r.resultMap(info, map[string]any{"accepted": false}))
		return
	}
	ok, _, abandoned = r.check(ctx, approvalgate.Request{
		Class: nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE,
		Title: "Discard Anthrex run " + info.RunID, Summary: "Removes the run's worktrees and deletes its branches (" + info.RunBranch + ").",
		Payload: map[string]any{"anthrex_run": info.RunID, "action": "discard", "run_branch": info.RunBranch},
	})
	if abandoned || !ok {
		r.output("discard not approved; the run stays complete in Anthrex")
		return
	}
	if _, err := r.api.Finish(r.ctx, info.RunID, anthrex.FinishDiscard, ""); err != nil {
		r.did("discard", "", err)
		return
	}
	res, err := r.api.Finish(r.ctx, info.RunID, anthrex.FinishDiscard, info.RunID)
	if err == nil {
		r.did("discarded", res.Message, nil)
	} else {
		r.did("discard", "", err)
	}
}

// accept runs Finish(accept): the daemon first answers ConfirmNeeded; a
// moved base needs its own approval before confirming "<run>@<new base>".
func (r *run) accept(ctx context.Context, info *anthrex.RunInfo) {
	res, err := r.api.Finish(r.ctx, info.RunID, anthrex.FinishAccept, "")
	if err != nil {
		r.did("accept", "", err)
		return
	}
	confirm := info.RunID
	if res.ConfirmNeeded && res.BaseMoved != nil {
		bm := res.BaseMoved
		commits := make([]any, 0, len(bm.Commits))
		for _, c := range bm.Commits {
			commits = append(commits, c)
		}
		ok, _, abandoned := r.check(ctx, approvalgate.Request{
			Class: nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH,
			Title: fmt.Sprintf("%s moved: merge onto %.7s?", info.BaseBranch, bm.To),
			Summary: fmt.Sprintf("%s gained %d commit(s) since the run started:\n%s", info.BaseBranch, bm.Total,
				strings.Join(bm.Commits, "\n")),
			Payload: map[string]any{"anthrex_run": info.RunID, "action": "accept_moved_base", "from": bm.From, "to": bm.To, "commits": commits},
		})
		if abandoned || !ok {
			r.output("not merged: the base moved and the new base was not approved")
			return
		}
		confirm = info.RunID + "@" + bm.To
	}
	if !res.ConfirmNeeded {
		r.did("accepted", res.Message, nil)
		return
	}
	res, err = r.api.Finish(r.ctx, info.RunID, anthrex.FinishAccept, confirm)
	if err != nil {
		r.did("accept", "", err)
		return
	}
	if res.ConfirmNeeded { // the base moved again between the two calls
		r.output("not merged: " + res.Prompt + " — accept again in Anthrex")
		return
	}
	r.did("accepted", res.Message, nil)
}
