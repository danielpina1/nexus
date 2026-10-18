package anthrexrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/anthrex"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Node tools. Action classes drive the caller's approval gate:
//
//	anthrex.status, windows, conversation, profile, stats   READ
//	anthrex.message, approve, answer                        WRITE_INTERNAL
//	anthrex.cancel, new_window                              COMPUTE
//	anthrex.start_goal, send_input                          WRITE_EXTERNAL (approve by default)
//	anthrex.accept                                          CODE_PUSH (approve; biometric per policy)
//
// start_goal and send_input are compute-like, but their effect leaves Nexus
// (agents editing a repository; keystrokes into a terminal that may be a
// shell), so they use WRITE_EXTERNAL, whose default level is APPROVE.
func (r *Runtime) Tools() []kernel.Tool {
	read := nexusv1.ActionClass_ACTION_CLASS_READ
	internal := nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL
	compute := nexusv1.ActionClass_ACTION_CLASS_COMPUTE
	external := nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL
	push := nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH
	t := func(name, desc string, class nexusv1.ActionClass, schema string, f func(context.Context, json.RawMessage) (any, error)) kernel.Tool {
		return kernel.ToolFunc{
			S: kernel.ToolSpec{Name: name, Description: desc, InputSchema: json.RawMessage(schema), ActionClass: class},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				v, err := f(ctx, c.Input)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				return kernel.ToolOutput(v)
			},
		}
	}
	return []kernel.Tool{
		t("anthrex.status", "List Anthrex coding runs (state, tasks, pending decisions), or one run in full with run_id.", read,
			`{"type":"object","properties":{"run_id":{"type":"string","description":"a run id (or unique prefix/suffix); omit to list all"}}}`, r.toolStatus),
		t("anthrex.start_goal", "Start an Anthrex coding goal in an allowed repository: Anthrex plans it into tasks, runs coding agents in worktrees, reviews and merges them into a run branch. The final merge needs anthrex.accept.", external,
			`{"type":"object","properties":{"goal":{"type":"string"},"repo":{"type":"string","description":"repository directory (must be in anthrex.allowed_repos; optional when exactly one is allowed)"},"delivery":{"type":"string","enum":["local","pr"]},"design":{"type":"string","enum":["off","full"]},"orchestrator":{"type":"string","description":"claude or codex[:model]"},"approve_plan":{"type":"boolean","description":"skip the plan gate (--yes)"}},"required":["goal"]}`, r.toolStartGoal),
		t("anthrex.message", "Send a message to an Anthrex run's workers (to: task ids \"t1,t2\", \"stage:<n>\", \"running\") or type it to the run's orchestrator (to: \"orchestrator\").", internal,
			`{"type":"object","properties":{"run_id":{"type":"string"},"to":{"type":"string","default":"running"},"text":{"type":"string"},"kind":{"type":"string","enum":["info","change","stop_and_wait"]}},"required":["run_id","text"]}`, r.toolMessage),
		t("anthrex.approve", "Decide an Anthrex gate: the plan (no hold/gate), an approval hold (hold), or a design document gate (gate: brainstorm|spec|plan, with action approve|changes|back|rethink|reject and a note).", internal,
			`{"type":"object","properties":{"run_id":{"type":"string"},"hold":{"type":"string"},"gate":{"type":"string","enum":["brainstorm","spec","plan"]},"version":{"type":"integer"},"action":{"type":"string","enum":["approve","reject","changes","back","rethink"],"default":"approve"},"note":{"type":"string"}},"required":["run_id"]}`, r.toolApprove),
		t("anthrex.answer", "Answer an Anthrex run's orchestrator question (choice: option number from 1, or option text; text: free answer typed into its window), or a blocked task's question (task_id + text).", internal,
			`{"type":"object","properties":{"run_id":{"type":"string"},"choice":{"type":"string"},"text":{"type":"string"},"task_id":{"type":"string"}},"required":["run_id"]}`, r.toolAnswer),
		t("anthrex.accept", "Merge a complete Anthrex run into its base branch (local delivery). If the base moved, the call fails with the new head: call again with base set to it.", push,
			`{"type":"object","properties":{"run_id":{"type":"string"},"base":{"type":"string","description":"the moved base head to merge onto"}},"required":["run_id"]}`, r.toolAccept),
		t("anthrex.cancel", "Cancel an Anthrex run.", compute,
			`{"type":"object","properties":{"run_id":{"type":"string"}},"required":["run_id"]}`, r.toolCancel),
		t("anthrex.windows", "List Anthrex windows (agent terminals: claude, codex, shell; and headless run sessions).", read,
			`{"type":"object","properties":{}}`, r.toolWindows),
		t("anthrex.new_window", "Open an Anthrex window (claude, codex or shell) in an allowed repository, optionally on a new git worktree branch and with an initial prompt.", compute,
			`{"type":"object","properties":{"runtime":{"type":"string","enum":["claude","codex","shell"]},"repo":{"type":"string"},"name":{"type":"string"},"worktree":{"type":"string","description":"create a worktree on this branch"},"model":{"type":"string"},"prompt":{"type":"string"}},"required":["runtime"]}`, r.toolNewWindow),
		t("anthrex.send_input", "Type text into an Anthrex window's terminal (Enter appended unless enter=false).", external,
			`{"type":"object","properties":{"window_id":{"type":"integer"},"text":{"type":"string"},"enter":{"type":"boolean","default":true}},"required":["window_id","text"]}`, r.toolSendInput),
		t("anthrex.conversation", "Read an Anthrex window's agent conversation (last turns, text and tool calls).", read,
			`{"type":"object","properties":{"window_id":{"type":"integer"},"agent_id":{"type":"string"},"last":{"type":"integer","default":20}},"required":["window_id"]}`, r.toolConversation),
		t("anthrex.profile", "Show the Anthrex profile status of an allowed repository (commands, checks, setup state).", read,
			`{"type":"object","properties":{"repo":{"type":"string"}}}`, r.toolProfile),
		t("anthrex.stats", "Summarise an allowed repository's Anthrex run history (read-only).", read,
			`{"type":"object","properties":{"repo":{"type":"string"}}}`, r.toolStats),
	}
}

func decode(in json.RawMessage, v any) error {
	if len(in) == 0 {
		return nil
	}
	if err := json.Unmarshal(in, v); err != nil {
		return fmt.Errorf("anthrex: bad input: %w", err)
	}
	return nil
}

// resolveRun accepts a full id or a unique prefix/suffix (like the CLI).
func (r *Runtime) resolveRun(ctx context.Context, api anthrex.API, id string) (*anthrex.RunInfo, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("anthrex: run_id is required")
	}
	snap, err := api.ListRuns(ctx)
	if err != nil {
		return nil, err
	}
	if run := snap.Run(id); run != nil {
		return run, nil
	}
	var match []*anthrex.RunInfo
	for i := range snap.Runs {
		if strings.HasPrefix(snap.Runs[i].RunID, id) || strings.HasSuffix(snap.Runs[i].RunID, id) {
			match = append(match, &snap.Runs[i])
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return nil, fmt.Errorf("anthrex: no run matches %q", id)
	}
	ids := make([]string, len(match))
	for i, m := range match {
		ids[i] = m.RunID
	}
	return nil, fmt.Errorf("anthrex: %q matches several runs: %s", id, strings.Join(ids, ", "))
}

// RunBrief is the compact view of a run the tools return.
type RunBrief struct {
	RunID     string         `json:"run_id"`
	Title     string         `json:"title"`
	State     string         `json:"state"`
	Project   string         `json:"project"`
	Branch    string         `json:"run_branch"`
	Base      string         `json:"base_branch"`
	Tasks     map[string]int `json:"tasks"`
	Attention []string       `json:"attention,omitempty"`
	Pending   []string       `json:"pending,omitempty"`
	Tokens    uint64         `json:"tokens"`
}

func brief(info *anthrex.RunInfo) RunBrief {
	b := RunBrief{RunID: info.RunID, Title: info.DisplayTitle(), State: info.State, Project: info.Project,
		Branch: info.RunBranch, Base: info.BaseBranch, Tasks: map[string]int{}, Attention: info.Attention,
		Tokens: runUsage(info).Billable()}
	for i := range info.Tasks {
		b.Tasks[info.Tasks[i].State]++
	}
	b.Pending = pending(info)
	return b
}

// pending lists the decisions the run waits on, as tool hints.
func pending(info *anthrex.RunInfo) []string {
	var p []string
	if info.State == anthrex.RunAwaitingApproval {
		p = append(p, "plan approval: anthrex.approve {run_id}")
	}
	for _, h := range info.Holds {
		if h.State == anthrex.HoldAwaiting {
			p = append(p, "hold "+h.ID+": anthrex.approve {run_id, hold: \""+h.ID+"\"}")
		}
	}
	if g := info.DocGate; g != nil && g.Revising == nil {
		p = append(p, fmt.Sprintf("%s v%d at its gate: anthrex.approve {run_id, gate: %q, action}", g.Kind, g.Version, g.Kind))
	}
	if o := info.Orchestrator; o != nil && o.Ask != nil {
		p = append(p, "orchestrator asks: "+o.Ask.Question+" (options: "+strings.Join(o.Ask.Options, " | ")+"): anthrex.answer")
	}
	for i := range info.Tasks {
		t := &info.Tasks[i]
		if t.State == anthrex.TaskBlocked && t.Block != nil && t.Block.Reason == "question" {
			p = append(p, "task "+t.ID+" asks: "+t.Block.Text+": anthrex.answer {run_id, task_id, text}")
		}
	}
	if info.State == anthrex.RunComplete && info.Delivery == nil {
		p = append(p, "complete: anthrex.accept {run_id} to merge into "+info.BaseBranch)
	}
	return p
}

func (r *Runtime) toolStatus(ctx context.Context, in json.RawMessage) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	if p.RunID != "" {
		info, err := r.resolveRun(ctx, api, p.RunID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"run": info, "pending": pending(info)}, nil
	}
	snap, err := api.ListRuns(ctx)
	if err != nil {
		return nil, err
	}
	runs := make([]RunBrief, 0, len(snap.Runs))
	for i := range snap.Runs {
		runs = append(runs, brief(&snap.Runs[i]))
	}
	out := map[string]any{"runs": runs, "mode": api.Mode()}
	if len(snap.QueuedGoals) > 0 {
		out["queued_goals"] = snap.QueuedGoals
	}
	return out, nil
}

func (r *Runtime) toolStartGoal(ctx context.Context, in json.RawMessage) (any, error) {
	var p struct {
		Goal         string `json:"goal"`
		Repo         string `json:"repo"`
		Delivery     string `json:"delivery"`
		Design       string `json:"design"`
		Orchestrator string `json:"orchestrator"`
		ApprovePlan  bool   `json:"approve_plan"`
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	repo, err := r.cfg.AllowedRepo(p.Repo)
	if err != nil {
		return nil, err
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	res, err := api.StartGoal(ctx, anthrex.StartGoalRequest{
		Goal: p.Goal, Dir: repo, Yes: p.ApprovePlan, TrustProject: r.cfg.TrustProject,
		Delivery: first(p.Delivery, r.cfg.DefaultDelivery), Design: first(p.Design, r.cfg.Design),
		Orchestrator: first(p.Orchestrator, r.cfg.DefaultOrchestrator),
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (r *Runtime) toolMessage(ctx context.Context, in json.RawMessage) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
		To    string `json:"to"`
		Text  string `json:"text"`
		Kind  string `json:"kind"`
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	info, err := r.resolveRun(ctx, api, p.RunID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, errors.New("anthrex: empty message")
	}
	if p.To == "orchestrator" {
		o := info.Orchestrator
		if o == nil || o.WindowID == nil || !o.Live {
			return nil, errors.New("anthrex: the run has no live orchestrator window")
		}
		if err := api.SendInput(ctx, *o.WindowID, []byte(p.Text+"\r")); err != nil {
			return nil, err
		}
		return map[string]any{"sent": true, "to": "orchestrator"}, nil
	}
	msg, err := api.Message(ctx, info.RunID, first(p.To, "running"), p.Text, p.Kind)
	if err != nil {
		return nil, err
	}
	return map[string]any{"sent": true, "message": msg}, nil
}

func (r *Runtime) toolApprove(ctx context.Context, in json.RawMessage) (any, error) {
	var p struct {
		RunID   string  `json:"run_id"`
		Hold    string  `json:"hold"`
		Gate    string  `json:"gate"`
		Version *uint32 `json:"version"`
		Action  string  `json:"action"`
		Note    string  `json:"note"`
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	act := first(p.Action, "approve")
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	info, err := r.resolveRun(ctx, api, p.RunID)
	if err != nil {
		return nil, err
	}
	var msg string
	switch {
	case p.Hold != "" && act == "approve":
		msg, err = api.ApproveHold(ctx, info.RunID, p.Hold)
	case p.Hold != "" && act == "reject":
		msg, err = api.RejectHold(ctx, info.RunID, p.Hold)
	case p.Hold != "":
		return nil, fmt.Errorf("anthrex: a hold is approved or rejected, not %q", act)
	case p.Gate != "":
		if (act == "changes" || act == "back" || act == "rethink") && strings.TrimSpace(p.Note) == "" {
			return nil, fmt.Errorf("anthrex: %s needs a note", act)
		}
		msg, err = api.DocGate(ctx, info.RunID, p.Gate, anthrex.DocGateAction{Kind: act, Version: p.Version, Note: p.Note})
	case act == "approve":
		msg, err = api.Approve(ctx, info.RunID)
	case act == "reject":
		msg, err = api.Reject(ctx, info.RunID)
	default:
		return nil, fmt.Errorf("anthrex: action %q needs a gate", act)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "message": msg}, nil
}

func (r *Runtime) toolAnswer(ctx context.Context, in json.RawMessage) (any, error) {
	var p struct {
		RunID  string `json:"run_id"`
		Choice any    `json:"choice"`
		Text   string `json:"text"`
		TaskID string `json:"task_id"`
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	info, err := r.resolveRun(ctx, api, p.RunID)
	if err != nil {
		return nil, err
	}
	if p.TaskID != "" {
		if strings.TrimSpace(p.Text) == "" {
			return nil, errors.New("anthrex: a task answer needs text")
		}
		s, ok := api.(*anthrex.Supervisor)
		if !ok {
			return nil, fmt.Errorf("%w: task answers need the daemon socket", anthrex.ErrUnsupported)
		}
		msg, err := s.Edit(ctx, info.RunID, []any{anthrex.EditAnswer(p.TaskID, p.Text)})
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "message": msg}, nil
	}
	o := info.Orchestrator
	if o == nil || o.Ask == nil {
		return nil, errors.New("anthrex: the run's orchestrator asks nothing")
	}
	choice := strings.TrimSpace(fmt.Sprint(nilToEmpty(p.Choice)))
	for i, opt := range o.Ask.Options {
		if strings.EqualFold(opt, choice) || choice == strconv.Itoa(i+1) {
			c := uint32(i)
			msg, err := api.AnswerAsk(ctx, info.RunID, o.Ask.ID, &c)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "answered": opt, "message": msg}, nil
		}
	}
	text := strings.TrimSpace(first(p.Text, choice))
	if text == "" {
		return nil, fmt.Errorf("anthrex: give a choice (1-%d or the option text) or a text answer", len(o.Ask.Options))
	}
	if o.WindowID == nil {
		return nil, errors.New("anthrex: the orchestrator has no window for a free-text answer")
	}
	if err := api.SendInput(ctx, *o.WindowID, []byte(text+"\r")); err != nil {
		return nil, err
	}
	msg, err := api.AnswerAsk(ctx, info.RunID, o.Ask.ID, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "typed": true, "message": msg}, nil
}

func nilToEmpty(v any) any {
	if v == nil {
		return ""
	}
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return v
}

func (r *Runtime) toolAccept(ctx context.Context, in json.RawMessage) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
		Base  string `json:"base"`
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	info, err := r.resolveRun(ctx, api, p.RunID)
	if err != nil {
		return nil, err
	}
	if info.State != anthrex.RunComplete {
		return nil, fmt.Errorf("anthrex: run %s is %s; accept applies only to a complete run", info.RunID, info.State)
	}
	res, err := api.Finish(ctx, info.RunID, anthrex.FinishAccept, "")
	if err != nil {
		return nil, err
	}
	if !res.ConfirmNeeded {
		return res, nil
	}
	confirm := info.RunID
	if bm := res.BaseMoved; bm != nil {
		if p.Base == "" || !strings.HasPrefix(bm.To, strings.ToLower(p.Base)) || len(p.Base) < 7 {
			return nil, fmt.Errorf("anthrex: %s moved to %s (%d new commits: %s); not merged — call anthrex.accept again with base=%q to merge onto it",
				info.BaseBranch, bm.To, bm.Total, strings.Join(bm.Commits, "; "), bm.To)
		}
		confirm = info.RunID + "@" + bm.To
	}
	return api.Finish(ctx, info.RunID, anthrex.FinishAccept, confirm)
}

func (r *Runtime) toolCancel(ctx context.Context, in json.RawMessage) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	info, err := r.resolveRun(ctx, api, p.RunID)
	if err != nil {
		return nil, err
	}
	msg, err := api.Cancel(ctx, info.RunID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "message": msg}, nil
}

func (r *Runtime) toolWindows(ctx context.Context, _ json.RawMessage) (any, error) {
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	w, err := api.ListWindows(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"windows": w}, nil
}

func (r *Runtime) toolNewWindow(ctx context.Context, in json.RawMessage) (any, error) {
	var p struct {
		Runtime, Repo, Name, Worktree, Model, Prompt string
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	switch p.Runtime {
	case "claude", "codex", "shell":
	default:
		return nil, fmt.Errorf("anthrex: runtime %q (want claude, codex or shell)", p.Runtime)
	}
	repo, err := r.cfg.AllowedRepo(p.Repo)
	if err != nil {
		return nil, err
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	id, err := api.CreateWindow(ctx, anthrex.WindowSpec{Name: p.Name, Runtime: p.Runtime, Cwd: repo,
		WorktreeBranch: p.Worktree, Model: p.Model, InitialPrompt: p.Prompt})
	if err != nil {
		return nil, err
	}
	return map[string]any{"window_id": id}, nil
}

func (r *Runtime) toolSendInput(ctx context.Context, in json.RawMessage) (any, error) {
	var p struct {
		WindowID uint32 `json:"window_id"`
		Text     string `json:"text"`
		Enter    *bool  `json:"enter"`
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	b := p.Text
	if p.Enter == nil || *p.Enter {
		b += "\r"
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	if err := api.SendInput(ctx, p.WindowID, []byte(b)); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (r *Runtime) toolConversation(ctx context.Context, in json.RawMessage) (any, error) {
	var p struct {
		WindowID uint32 `json:"window_id"`
		AgentID  string `json:"agent_id"`
		Last     int    `json:"last"`
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	if p.Last <= 0 {
		p.Last = 20
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	c, err := api.Conversation(ctx, p.WindowID, p.AgentID)
	if err != nil {
		return nil, err
	}
	turns := c.Turns
	if len(turns) > p.Last {
		turns = turns[len(turns)-p.Last:]
	}
	return map[string]any{"window_id": c.WindowID, "runtime": c.Runtime, "rev": c.Rev, "degraded": c.Degraded,
		"dropped_turns": c.DroppedTurns, "turns": turns}, nil
}

func (r *Runtime) toolProfile(ctx context.Context, in json.RawMessage) (any, error) {
	return r.repoTool(ctx, in, func(api anthrex.API, dir string) (any, error) { return api.ProfileStatus(ctx, dir) })
}

func (r *Runtime) toolStats(ctx context.Context, in json.RawMessage) (any, error) {
	return r.repoTool(ctx, in, func(api anthrex.API, dir string) (any, error) { return api.Stats(ctx, dir) })
}

func (r *Runtime) repoTool(ctx context.Context, in json.RawMessage, f func(anthrex.API, string) (any, error)) (any, error) {
	var p struct {
		Repo string `json:"repo"`
	}
	if err := decode(in, &p); err != nil {
		return nil, err
	}
	repo, err := r.cfg.AllowedRepo(p.Repo)
	if err != nil {
		return nil, err
	}
	api, err := r.API(ctx)
	if err != nil {
		return nil, err
	}
	v, err := f(api, repo)
	if err != nil {
		return nil, err
	}
	return map[string]any{"repo": repo, "result": jsonSafe(v)}, nil
}

// jsonSafe turns []byte in a generic value into strings (json.Marshal would
// base64 them).
func jsonSafe(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case []any:
		for i := range t {
			t[i] = jsonSafe(t[i])
		}
	case map[string]any:
		for k := range t {
			t[k] = jsonSafe(t[k])
		}
	}
	return v
}
