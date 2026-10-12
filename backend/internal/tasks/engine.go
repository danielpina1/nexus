package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Actor names used on events produced by the engine.
const (
	actorHub  = "hub"
	actorUser = "user"
)

// Engine is the task engine. It implements kernel.TaskEngine.
//
// Concurrency: every read-modify-write of a task happens under mu (see
// mutate). Calls to other services (router, catalog, dispatcher, approval
// broker) are made without holding mu, so those services may call back into
// the engine (e.g. a dispatcher delivering a run event synchronously).
type Engine struct {
	h   kernel.Host
	log *slog.Logger
	db  *sql.DB
	bus kernel.EventBus
	cfg Config
	now func() time.Time

	mu sync.Mutex
}

var _ kernel.TaskEngine = (*Engine)(nil)

// NewEngine creates an engine over the given store and bus. Optional services
// (router, agents, dispatcher, approvals) are looked up on h per call.
func NewEngine(h kernel.Host, st kernel.Store, bus kernel.EventBus, cfg Config) (*Engine, error) {
	if err := st.Migrate("tasks", migrations); err != nil {
		return nil, fmt.Errorf("tasks: migrate: %w", err)
	}
	cfg.defaults()
	return &Engine{h: h, log: h.Logger(), db: st.DB(), bus: bus, cfg: cfg, now: time.Now}, nil
}

func newID(prefix string) string { return prefix + ulid.Make().String() }

// ---------------------------------------------------------------- lazy services

func lookup[T any](h kernel.Host, name string) (T, bool) {
	v, err := kernel.Resolve[T](h, name)
	return v, err == nil
}

func (e *Engine) router() (kernel.Router, bool) { return lookup[kernel.Router](e.h, kernel.SvcRouter) }
func (e *Engine) agents() (kernel.AgentCatalog, bool) {
	return lookup[kernel.AgentCatalog](e.h, kernel.SvcAgents)
}
func (e *Engine) dispatcher() (kernel.Dispatcher, bool) {
	return lookup[kernel.Dispatcher](e.h, kernel.SvcDispatcher)
}
func (e *Engine) approvals() (kernel.ApprovalBroker, bool) {
	return lookup[kernel.ApprovalBroker](e.h, kernel.SvcApprovals)
}

// ---------------------------------------------------------------- mutation

// mutate loads a task under the engine lock, applies fn, persists it and
// publishes one TaskUpdated per state change (plus fn's extra events).
// fn must not call other services.
func (e *Engine) mutate(ctx context.Context, id, actor string, fn func(r *taskRec) error) (*taskRec, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.loadTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := fn(r); err != nil {
		return nil, err
	}
	if len(r.path) == 0 && !r.notify && len(r.extra) == 0 {
		return r, nil
	}
	r.task.UpdatedAt = timestamppb.New(e.now())
	if err := e.saveTask(ctx, r); err != nil {
		return nil, err
	}
	e.publishRec(ctx, r, actor)
	return r, nil
}

func (e *Engine) publishRec(ctx context.Context, r *taskRec, actor string) {
	states := r.path
	if len(states) == 0 && r.notify {
		states = []nexusv1.TaskState{r.task.State}
	}
	for _, s := range states {
		snap := proto.Clone(r.task).(*nexusv1.Task)
		snap.State = s
		e.publish(ctx, &nexusv1.Event{TaskId: r.task.Id, RunId: r.task.CurrentRunId, Actor: actor,
			Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: snap}}})
	}
	for _, ev := range r.extra {
		if ev.TaskId == "" {
			ev.TaskId = r.task.Id
		}
		if ev.Actor == "" {
			ev.Actor = actor
		}
		e.publish(ctx, ev)
	}
}

func (e *Engine) publish(ctx context.Context, ev *nexusv1.Event) {
	if _, err := e.bus.Publish(ctx, ev); err != nil {
		e.log.Error("publish event", "task", ev.TaskId, "err", err)
	}
}

// fail moves a task to failed with summary (no-op on finished tasks).
func (e *Engine) fail(ctx context.Context, id, summary string) (*nexusv1.Task, error) {
	e.log.Warn("task failed", "task", id, "reason", summary)
	r, err := e.mutate(ctx, id, actorHub, func(r *taskRec) error {
		if IsTerminal(r.task.State) {
			return nil
		}
		r.task.Summary = summary
		if err := e.setRunState(ctx, r.task.CurrentRunId, runFailed, summary); err != nil {
			return err
		}
		return r.to(stFailed)
	})
	if err != nil {
		return nil, err
	}
	return r.task, nil
}

// ---------------------------------------------------------------- create

// CreateTask creates a root task and drives it until it is assigned to a node
// (or needs input / failed). The returned error is reserved for invalid
// requests and storage failures; routing or dispatch problems are reported as
// a failed task with a summary.
func (e *Engine) CreateTask(ctx context.Context, req *nexusv1.CreateTaskRequest) (*nexusv1.Task, error) {
	return e.create(ctx, req, nil)
}

// CreateChildTask creates a sub-task of parentID (used by spawn_agent).
func (e *Engine) CreateChildTask(ctx context.Context, parentID string, req *nexusv1.CreateTaskRequest) (*nexusv1.Task, error) {
	parent, err := e.loadTask(ctx, parentID)
	if err != nil {
		return nil, err
	}
	return e.create(ctx, req, parent)
}

func (e *Engine) create(ctx context.Context, req *nexusv1.CreateTaskRequest, parent *taskRec) (*nexusv1.Task, error) {
	if req == nil || (strings.TrimSpace(req.Prompt) == "" && len(req.Attachments) == 0) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("prompt or attachments required"))
	}
	if req.DryRun {
		return e.dryRun(ctx, req)
	}
	req = proto.Clone(req).(*nexusv1.CreateTaskRequest)
	now := e.now()

	e.mu.Lock()
	if req.IdempotencyKey != "" {
		existing, err := e.taskByIdempotencyKey(ctx, req.IdempotencyKey)
		if err != nil || existing != nil {
			e.mu.Unlock()
			if existing != nil {
				return existing.task, nil
			}
			return nil, err
		}
	}
	id := newID("tsk_")
	r := &taskRec{req: req, task: &nexusv1.Task{
		Id:        id,
		RootId:    id,
		Title:     titleOf(req.Prompt),
		Prompt:    req.Prompt,
		AgentId:   req.AgentId,
		State:     stQueued,
		Source:    req.Source,
		CreatedAt: timestamppb.New(now),
		UpdatedAt: timestamppb.New(now),
	}}
	if parent != nil {
		r.task.ParentId = parent.task.Id
		r.task.RootId = parent.task.RootId
		r.depth = parent.depth + 1
	}
	if err := e.insertTask(ctx, r, req.IdempotencyKey); err != nil {
		e.mu.Unlock()
		return nil, err
	}
	r.notify = true
	e.publishRec(ctx, r, actorHub)
	e.mu.Unlock()

	if req.AgentId != "" {
		return e.assign(ctx, id, req.AgentId, assignOpts{})
	}
	return e.route(ctx, id, req.Prompt)
}

// dryRun resolves the agent without persisting anything or publishing events.
func (e *Engine) dryRun(ctx context.Context, req *nexusv1.CreateTaskRequest) (*nexusv1.Task, error) {
	t := &nexusv1.Task{Title: titleOf(req.Prompt), Prompt: req.Prompt, AgentId: req.AgentId,
		State: stQueued, Source: req.Source, CreatedAt: timestamppb.New(e.now())}
	if t.AgentId == "" {
		if rt, ok := e.router(); ok {
			route, err := rt.Route(ctx, req.Prompt, req.Attachments, &nexusv1.CommandContext{Source: req.Source})
			if err != nil {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("route: %w", err))
			}
			if route.GetConfidence() >= float32(e.cfg.RouteThreshold) {
				t.AgentId, t.Tier = route.AgentId, route.Tier
			}
		}
	}
	if cat, ok := e.agents(); ok && t.AgentId != "" {
		if a, _, _, err := cat.GetAgent(ctx, t.AgentId); err == nil && t.Tier == nexusv1.Tier_TIER_UNSPECIFIED {
			t.Tier = a.Tier
		}
	}
	return t, nil
}

func titleOf(prompt string) string {
	s := strings.TrimSpace(prompt)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if utf8.RuneCountInString(s) > 80 {
		s = string([]rune(s)[:79]) + "…"
	}
	return s
}

// ---------------------------------------------------------------- routing

// route moves the task to routing and asks the router for an agent.
func (e *Engine) route(ctx context.Context, id, input string) (*nexusv1.Task, error) {
	r, err := e.mutate(ctx, id, actorHub, func(r *taskRec) error { return r.to(stRouting) })
	if err != nil {
		return nil, err
	}
	rt, ok := e.router()
	if !ok {
		return e.askForAgent(ctx, id, "No router is available. Which agent should handle this?", nil)
	}
	route, err := rt.Route(ctx, input, r.req.Attachments, &nexusv1.CommandContext{Source: r.task.Source})
	if err != nil {
		return e.fail(ctx, id, "routing failed: "+err.Error())
	}
	e.linkDecision(ctx, rt, route, id)
	if route.GetAgentId() != "" && route.GetConfidence() >= float32(e.cfg.RouteThreshold) {
		return e.assign(ctx, id, route.AgentId, assignOpts{tier: route.Tier})
	}
	text := "Which agent should handle this?"
	if route.GetReason() != "" {
		text += " (" + route.Reason + ")"
	}
	return e.askForAgent(ctx, id, text, append([]string{route.GetAgentId()}, route.GetAlternatives()...))
}

// linkDecision tells a router that logs decisions which task a decision produced.
func (e *Engine) linkDecision(ctx context.Context, rt kernel.Router, route *nexusv1.Route, taskID string) {
	l, ok := rt.(interface {
		LinkTask(ctx context.Context, decisionID, taskID string) error
	})
	if !ok {
		return
	}
	if d := route.GetParams().GetFields()["decision_id"].GetStringValue(); d != "" {
		if err := l.LinkTask(ctx, d, taskID); err != nil {
			e.log.Warn("link router decision", "decision", d, "task", taskID, "err", err)
		}
	}
}

// askForAgent moves the task to needs_input and publishes a Question whose
// choices are agent ids. Without any candidate agent the task fails.
func (e *Engine) askForAgent(ctx context.Context, id, text string, candidates []string) (*nexusv1.Task, error) {
	choices := dedupe(candidates)
	if len(choices) == 0 {
		if cat, ok := e.agents(); ok {
			if list, err := cat.ListAgents(ctx, false); err == nil {
				for _, a := range list {
					choices = append(choices, a.Id)
				}
			}
		}
		if len(choices) > e.cfg.MaxChoices {
			choices = choices[:e.cfg.MaxChoices]
		}
	}
	if len(choices) == 0 {
		return e.fail(ctx, id, "no agent could be chosen for this task (no router match and no agents available)")
	}
	qid := newID("q_")
	r, err := e.mutate(ctx, id, actorHub, func(r *taskRec) error {
		r.questionID = qid
		r.extra = append(r.extra, &nexusv1.Event{Body: &nexusv1.Event_Question{Question: &nexusv1.Question{
			QuestionId: qid, Text: text, Choices: choices, AllowFreeText: true,
		}}})
		return r.to(stNeedsInput)
	})
	if err != nil {
		return nil, err
	}
	return r.task, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------- assignment

type assignOpts struct {
	tier        nexusv1.Tier
	hint        string
	previousRun *runRec
}

// assign creates a run for agentID and dispatches it to a node.
//
// Missing agent catalog, unknown/disabled agent, missing dispatcher or no
// matching node all fail the task with a descriptive summary (there is no
// automatic retry when a node connects later; the user can escalate or
// re-create the task).
func (e *Engine) assign(ctx context.Context, id, agentID string, o assignOpts) (*nexusv1.Task, error) {
	cat, ok := e.agents()
	if !ok {
		return e.fail(ctx, id, "no agent catalog available (is the vault/agents module enabled?)")
	}
	agent, instructions, _, err := cat.GetAgent(ctx, agentID)
	if err != nil {
		return e.fail(ctx, id, fmt.Sprintf("unknown agent %q: %v", agentID, err))
	}
	if !agent.Enabled {
		return e.fail(ctx, id, fmt.Sprintf("agent %q is disabled", agentID))
	}
	tier := o.tier
	if tier == nexusv1.Tier_TIER_UNSPECIFIED {
		tier = agent.Tier
	}
	token, tokenHash, err := newRunToken()
	if err != nil {
		return nil, err
	}
	runID := newID("run_")
	r, err := e.mutate(ctx, id, actorHub, func(r *taskRec) error {
		if err := r.to(stAssigned); err != nil {
			return err
		}
		n, err := e.countRuns(ctx, id)
		if err != nil {
			return err
		}
		if err := e.insertRun(ctx, &runRec{id: runID, taskID: id, attempt: n + 1, agentID: agentID, tier: tier, hint: o.hint}, tokenHash); err != nil {
			return err
		}
		r.task.AgentId, r.task.Tier, r.task.CurrentRunId, r.task.NodeId = agentID, tier, runID, ""
		r.questionID, r.approvalID = "", ""
		return nil
	})
	if err != nil {
		return nil, err
	}

	runCtx, err := e.assignmentContext(r, tier, o)
	if err != nil {
		return nil, err
	}
	a := &nexusv1.RunAssignment{
		RunId:        runID,
		TaskId:       id,
		Agent:        agent,
		Instructions: instructions,
		Prompt:       r.task.Prompt,
		Attachments:  r.req.Attachments,
		Context:      runCtx,
		RunToken:     token,
	}
	disp, ok := e.dispatcher()
	if !ok {
		return e.fail(ctx, id, "no dispatcher available (is the nodelink module enabled?)")
	}
	sel := kernel.NodeSelector{Tags: agent.NodeTags, Runtime: agent.Runtime}
	nodeID, err := disp.Assign(ctx, sel, a)
	if err != nil {
		return e.fail(ctx, id, fmt.Sprintf("no node available for agent %q (runtime %q, tags %v): %v", agentID, agent.Runtime, agent.NodeTags, err))
	}

	stale := false
	r, err = e.mutate(ctx, id, actorHub, func(r *taskRec) error {
		if r.task.CurrentRunId != runID || IsTerminal(r.task.State) {
			stale = true // cancelled or superseded while dispatching
			return nil
		}
		r.task.NodeId = nodeID
		r.notify = true
		return e.setRunNode(ctx, runID, nodeID)
	})
	if err != nil {
		return nil, err
	}
	if stale {
		if err := disp.Cancel(ctx, nodeID, &nexusv1.RunCancel{RunId: runID, Reason: "task no longer active"}); err != nil {
			e.log.Warn("cancel stale run", "run", runID, "err", err)
		}
	}
	return r.task, nil
}

// assignmentContext builds RunAssignment.context: task params plus engine metadata.
func (e *Engine) assignmentContext(r *taskRec, tier nexusv1.Tier, o assignOpts) (*structpb.Struct, error) {
	m := map[string]any{}
	for k, v := range r.req.GetParams().AsMap() {
		m[k] = v
	}
	m["task_id"] = r.task.Id
	m["root_id"] = r.task.RootId
	if r.task.ParentId != "" {
		m["parent_id"] = r.task.ParentId
	}
	m["tier"] = tierName(tier)
	if o.hint != "" {
		m["escalation_hint"] = o.hint
	}
	if p := o.previousRun; p != nil {
		m["previous_attempt"] = map[string]any{
			"run_id":   p.id,
			"agent_id": p.agentID,
			"summary":  p.summary,
			"output":   p.output,
		}
	}
	return structpb.NewStruct(m)
}

func tierName(t nexusv1.Tier) string {
	return strings.ToLower(strings.TrimPrefix(t.String(), "TIER_"))
}

func newRunToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	return token, h[:], nil
}

// VerifyRunToken checks a run token presented by a node for hub-side tools
// and returns the run's task. Only the token's SHA-256 is stored.
func (e *Engine) VerifyRunToken(ctx context.Context, runID, token string) (*nexusv1.Task, error) {
	want, err := e.runTokenHash(ctx, runID)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(want, got[:]) != 1 {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid run token"))
	}
	run, err := e.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	return e.GetTask(ctx, run.taskID)
}

// ---------------------------------------------------------------- queries

func (e *Engine) GetTask(ctx context.Context, id string) (*nexusv1.Task, error) {
	r, err := e.loadTask(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.task, nil
}

func (e *Engine) ChildTasks(ctx context.Context, id string) ([]*nexusv1.Task, error) {
	return e.queryTasks(ctx, `WHERE parent_id = ? ORDER BY id`, id)
}

// ListTasks lists tasks newest first. The page token is the last task id.
func (e *Engine) ListTasks(ctx context.Context, req *nexusv1.ListTasksRequest) ([]*nexusv1.Task, string, error) {
	size := int(req.GetPage().GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	where, args := listWhere(req, req.GetPage().GetPageToken())
	out, err := e.queryTasks(ctx, where+` ORDER BY id DESC LIMIT ?`, append(args, size+1)...)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > size {
		out = out[:size]
		next = out[size-1].Id
	}
	return out, next, nil
}

// ---------------------------------------------------------------- steering

// SendMessage delivers a user message. In needs_input it answers the routing
// question (choice = agent id; free text re-routes with the clarification);
// in waiting_input it answers the agent's question and resumes the run; in
// assigned/running it steers the run.
func (e *Engine) SendMessage(ctx context.Context, req *nexusv1.SendMessageRequest) error {
	if req.GetTaskId() == "" {
		// Questions asked by workflow "ask" steps are not tied to a task.
		if v, ok := e.h.Lookup("workflows"); ok && req.GetQuestionId() != "" {
			if wf, ok := v.(interface {
				OwnsQuestion(ctx context.Context, questionID string) bool
				Answer(ctx context.Context, questionID, choice, text string) error
			}); ok && wf.OwnsQuestion(ctx, req.QuestionId) {
				return wf.Answer(ctx, req.QuestionId, req.Choice, req.Text)
			}
		}
		return connect.NewError(connect.CodeInvalidArgument, errors.New("task_id required"))
	}
	r, err := e.loadTask(ctx, req.TaskId)
	if err != nil {
		return err
	}
	if req.IdempotencyKey != "" {
		fresh, err := e.recordMessageKey(ctx, req.IdempotencyKey, req.TaskId)
		if err != nil {
			return err
		}
		if !fresh {
			return nil
		}
	}
	if req.QuestionId != "" && r.questionID != "" && req.QuestionId != r.questionID {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("question %q is no longer pending", req.QuestionId))
	}
	e.publish(ctx, &nexusv1.Event{TaskId: r.task.Id, RunId: r.task.CurrentRunId, Actor: actorUser,
		Body: &nexusv1.Event_UserMessage{UserMessage: &nexusv1.UserMessage{Text: req.Text, Attachments: req.Attachments}}})

	switch r.task.State {
	case stNeedsInput:
		if req.Choice != "" {
			if _, err := e.mutate(ctx, r.task.Id, actorUser, func(r *taskRec) error {
				r.questionID = ""
				return r.to(stRouting)
			}); err != nil {
				return err
			}
			_, err := e.assign(ctx, r.task.Id, req.Choice, assignOpts{})
			return err
		}
		if strings.TrimSpace(req.Text) == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("choice or text required"))
		}
		_, err := e.route(ctx, r.task.Id, r.task.Prompt+"\n\nClarification: "+req.Text)
		return err

	case stWaitingInput, stRunning, stAssigned, stWaitingApproval:
		disp, ok := e.dispatcher()
		if !ok {
			return connect.NewError(connect.CodeUnavailable, errors.New("no dispatcher available"))
		}
		if r.task.NodeId == "" {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("task has no node yet"))
		}
		in := &nexusv1.RunInput{RunId: r.task.CurrentRunId, Text: req.Text, QuestionId: req.QuestionId, Choice: req.Choice}
		if in.QuestionId == "" && r.task.State == stWaitingInput {
			in.QuestionId = r.questionID
		}
		if err := disp.SendInput(ctx, r.task.NodeId, in); err != nil {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("send input to node: %w", err))
		}
		_, err := e.mutate(ctx, r.task.Id, actorUser, func(r *taskRec) error {
			if r.task.State != stWaitingInput {
				return nil
			}
			r.questionID = ""
			return r.to(stRunning)
		})
		return err

	default:
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("task is %s and does not accept messages", stateName(r.task.State)))
	}
}

// CancelTask cancels a task and its non-finished sub-tasks. Cancelling a
// finished task is a no-op.
func (e *Engine) CancelTask(ctx context.Context, id string) error {
	_, err := e.cancel(ctx, id, "cancelled by user")
	return err
}

func (e *Engine) cancel(ctx context.Context, id, reason string) (bool, error) {
	var runID, nodeID string
	changed := false
	_, err := e.mutate(ctx, id, actorUser, func(r *taskRec) error {
		if IsTerminal(r.task.State) {
			return nil
		}
		runID, nodeID = r.task.CurrentRunId, r.task.NodeId
		if r.task.Summary == "" {
			r.task.Summary = reason
		}
		r.questionID = ""
		if err := e.setRunState(ctx, runID, runCancelled, reason); err != nil {
			return err
		}
		changed = true
		return r.to(stCancelled)
	})
	if err != nil {
		return false, err
	}
	if changed && runID != "" && nodeID != "" {
		e.cancelOnNode(ctx, nodeID, runID, reason)
	}
	kids, err := e.nonTerminalIDs(ctx, "parent_id = ?", id)
	if err != nil {
		return changed, err
	}
	for _, k := range kids {
		if _, err := e.cancel(ctx, k, "parent task cancelled"); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

func (e *Engine) cancelOnNode(ctx context.Context, nodeID, runID, reason string) {
	disp, ok := e.dispatcher()
	if !ok {
		return
	}
	if err := disp.Cancel(ctx, nodeID, &nexusv1.RunCancel{RunId: runID, Reason: reason}); err != nil {
		e.log.Warn("cancel run on node", "node", nodeID, "run", runID, "err", err)
	}
}

// StopAll cancels every non-finished task (kill switch) and returns how many
// were cancelled. pauseTriggers is not handled here: the workflow/trigger
// engine is expected to observe the cancellations or expose its own switch.
func (e *Engine) StopAll(ctx context.Context, pauseTriggers bool) (int, error) {
	ids, err := e.nonTerminalIDs(ctx, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		ok, err := e.cancel(ctx, id, "stopped by kill switch")
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	if pauseTriggers {
		if v, ok := e.h.Lookup("workflows"); ok {
			if p, ok := v.(interface{ PauseTriggers(bool) }); ok {
				p.PauseTriggers(true)
			}
		}
	}
	e.log.Warn("kill switch: all tasks stopped", "cancelled", n, "pause_triggers", pauseTriggers)
	return n, nil
}

// EscalateTask starts a new run of the task on the next tier ("Try harder").
// The current run (if active) is cancelled on its node and the new run gets
// the hint and the previous attempt's summary/output in its context.
//
// Agent choice: if the current agent's tier already reaches the next tier it
// is kept; otherwise the enabled agent with the lowest tier ≥ next tier is
// used (same runtime preferred, then id). With no such agent the current agent
// is re-run with the higher tier in its context.
func (e *Engine) EscalateTask(ctx context.Context, id, hint string) (*nexusv1.Task, error) {
	r, err := e.loadTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.task.AgentId == "" || r.task.CurrentRunId == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("task has not run yet; nothing to escalate"))
	}
	next := r.task.Tier + 1
	if next > nexusv1.Tier_TIER_HEAVY {
		next = nexusv1.Tier_TIER_HEAVY
	}
	agentID := e.escalationAgent(ctx, r.task.AgentId, next)

	var prev *runRec
	wasActive := false
	oldNode := ""
	if _, err := e.mutate(ctx, id, actorUser, func(r *taskRec) error {
		prevState := r.task.State
		if err := r.to(stEscalating); err != nil {
			return err
		}
		wasActive = !IsTerminal(prevState) && r.task.NodeId != ""
		oldNode = r.task.NodeId
		if err := e.setRunState(ctx, r.task.CurrentRunId, runSuperseded, ""); err != nil {
			return err
		}
		p, err := e.loadRun(ctx, r.task.CurrentRunId)
		if err != nil {
			return err
		}
		prev = p
		r.task.Summary, r.task.Result, r.questionID, r.approvalID = "", nil, "", ""
		return nil
	}); err != nil {
		return nil, err
	}
	if wasActive {
		e.cancelOnNode(ctx, oldNode, prev.id, "escalated")
	}
	return e.assign(ctx, id, agentID, assignOpts{tier: next, hint: hint, previousRun: prev})
}

func (e *Engine) escalationAgent(ctx context.Context, current string, next nexusv1.Tier) string {
	cat, ok := e.agents()
	if !ok {
		return current
	}
	cur, _, _, err := cat.GetAgent(ctx, current)
	if err == nil && cur.Tier >= next {
		return current
	}
	list, err := cat.ListAgents(ctx, false)
	if err != nil {
		return current
	}
	runtime := ""
	if cur != nil {
		runtime = cur.Runtime
	}
	var cands []*nexusv1.Agent
	for _, a := range list {
		if a.Enabled && a.Tier >= next {
			cands = append(cands, a)
		}
	}
	if len(cands) == 0 {
		return current
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		if (a.Runtime == runtime) != (b.Runtime == runtime) {
			return a.Runtime == runtime
		}
		return a.Id < b.Id
	})
	return cands[0].Id
}

// FollowTask publishes a marker Notice (data.type = "follow_task") for the
// task. Pushing a Live Activity to devices is future work for the notifier.
func (e *Engine) FollowTask(ctx context.Context, taskID string, deviceIDs []string) error {
	if _, err := e.loadTask(ctx, taskID); err != nil {
		return err
	}
	devs := make([]any, len(deviceIDs))
	for i, d := range deviceIDs {
		devs[i] = d
	}
	data, err := structpb.NewStruct(map[string]any{"type": "follow_task", "device_ids": devs})
	if err != nil {
		return err
	}
	e.publish(ctx, &nexusv1.Event{TaskId: taskID, Actor: actorUser, Body: &nexusv1.Event_Notice{Notice: &nexusv1.Notice{
		Id: newID("ntc_"), Title: "Following task", Data: data,
	}}})
	return nil
}

// ReassignTask hands a task to another agent (router correction). A task
// still waiting for routing (queued/routing/needs_input) is assigned in place;
// otherwise the task is cancelled if still active and a new task with the
// same request (and parent) is created for agentID, which is returned.
func (e *Engine) ReassignTask(ctx context.Context, id, agentID string) (*nexusv1.Task, error) {
	if agentID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent_id required"))
	}
	r, err := e.loadTask(ctx, id)
	if err != nil {
		return nil, err
	}
	switch r.task.State {
	case stNeedsInput:
		if _, err := e.mutate(ctx, id, actorUser, func(r *taskRec) error {
			r.questionID = ""
			return r.to(stRouting)
		}); err != nil {
			return nil, err
		}
		fallthrough
	case stQueued, stRouting:
		return e.assign(ctx, id, agentID, assignOpts{})
	}
	if _, err := e.cancel(ctx, id, "reassigned to "+agentID); err != nil {
		return nil, err
	}
	req := proto.Clone(r.req).(*nexusv1.CreateTaskRequest)
	req.AgentId, req.IdempotencyKey, req.DryRun = agentID, "", false
	var parent *taskRec
	if r.task.ParentId != "" {
		if parent, err = e.loadTask(ctx, r.task.ParentId); err != nil {
			return nil, err
		}
	}
	return e.create(ctx, req, parent)
}
