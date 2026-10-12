package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/triggers"
	"github.com/danielpina1/nexus/backend/kernel"
)

// MaxDepth limits nested `workflow:` calls.
const MaxDepth = 5

var (
	errShutdown  = errors.New("workflow engine stopping")
	errCancelled = errors.New("workflow run cancelled")
	errTimeout   = errors.New("step timed out")
)

// permanentError is not retried (configuration problems, denials).
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func permanent(format string, a ...any) error { return permanentError{fmt.Errorf(format, a...)} }

// stepFailed is returned up the tree when a step failed (and on_error is fail).
type stepFailed struct {
	path string
	err  string
}

func (e *stepFailed) Error() string { return fmt.Sprintf("step %s failed: %s", e.path, e.err) }

func newID(prefix string) string { return prefix + ulid.Make().String() }

// ---------------------------------------------------------------- scopes

type stepVal struct {
	Output any
	Result any
	State  string
	Error  string
}

func (v stepVal) m() map[string]any {
	out, res := v.Output, v.Result
	if out == nil {
		out = ""
	}
	if res == nil {
		res = map[string]any{}
	}
	return map[string]any{"output": out, "result": res, "state": v.State, "error": v.Error}
}

type scope struct {
	parent *scope
	mu     sync.Mutex
	steps  map[string]stepVal
	vars   map[string]any
}

func newScope(parent *scope, vars map[string]any) *scope {
	return &scope{parent: parent, steps: map[string]stepVal{}, vars: vars}
}

func (s *scope) set(id string, v stepVal) {
	s.mu.Lock()
	s.steps[id] = v
	s.mu.Unlock()
}

func (s *scope) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]any, len(s.steps))
	for k, v := range s.steps {
		out[k] = v.m()
	}
	return out
}

// flatten merges the chain root → leaf.
func (s *scope) flatten() (steps map[string]any, vars map[string]any) {
	var chain []*scope
	for c := s; c != nil; c = c.parent {
		chain = append(chain, c)
	}
	steps, vars = map[string]any{}, map[string]any{}
	for i := len(chain) - 1; i >= 0; i-- {
		c := chain[i]
		c.mu.Lock()
		for k, v := range c.steps {
			steps[k] = v.m()
		}
		for k, v := range c.vars {
			vars[k] = v
		}
		c.mu.Unlock()
	}
	return steps, vars
}

// ---------------------------------------------------------------- runner

type runner struct {
	s   *Service
	run *RunInfo
	wf  *Workflow
}

func (r *runner) vars(sc *scope, extra map[string]any) Vars {
	steps, local := sc.flatten()
	v := Vars{
		"inputs":  r.run.Inputs,
		"steps":   steps,
		"trigger": r.run.Trigger,
		"run": map[string]any{"id": r.run.ID, "workflow_id": r.run.WorkflowID, "dry_run": r.run.DryRun,
			"source": r.run.Source, "parent_run_id": r.run.ParentRunID},
		"now": r.s.now(),
	}
	for k, x := range local {
		v[k] = x
	}
	for k, x := range extra {
		v[k] = x
	}
	return v
}

// execute runs (or resumes) the whole workflow.
func (r *runner) execute(ctx context.Context) {
	root := newScope(nil, nil)
	for _, id := range r.wf.StepIDs() {
		root.steps[id] = stepVal{State: StepPending}
	}
	last, err := r.execSeq(ctx, r.wf.Steps, root, "")
	cause := context.Cause(ctx)
	if errors.Is(cause, errShutdown) {
		r.s.log.Info("workflow run suspended for shutdown", "run", r.run.ID)
		return
	}
	fctx := context.WithoutCancel(ctx)
	state, msg := RunSucceeded, ""
	switch {
	case errors.Is(cause, errCancelled):
		state, msg = RunCancelled, "cancelled"
	case err != nil:
		state, msg = RunFailed, err.Error()
	}
	if err := r.s.st.finishRun(fctx, r.run.ID, state, last.Output, msg); err != nil {
		r.s.log.Error("workflow: finish run", "run", r.run.ID, "err", err)
	}
	r.run.State, r.run.Error, r.run.Output = state, msg, last.Output
	r.s.log.Info("workflow run finished", "run", r.run.ID, "workflow", r.wf.ID, "state", state, "err", msg)
	r.s.runFinished(fctx, r.run, r.wf)
}

// execSeq runs steps in order and returns the last executed step's value.
func (r *runner) execSeq(ctx context.Context, steps []*Step, sc *scope, prefix string) (stepVal, error) {
	var last stepVal
	for _, st := range steps {
		if err := context.Cause(ctx); ctx.Err() != nil {
			return last, err
		}
		v, err := r.execStep(ctx, st, sc, prefix+st.ID)
		if v.State != StepSkipped {
			last = v
		}
		if err != nil {
			return last, err
		}
	}
	return last, nil
}

func (r *runner) save(ctx context.Context, row *StepInfo) {
	if err := r.s.st.putStep(context.WithoutCancel(ctx), r.run.ID, row); err != nil {
		r.s.log.Error("workflow: save step", "run", r.run.ID, "step", row.Path, "err", err)
	}
}

func valOf(row *StepInfo) stepVal {
	return stepVal{Output: row.Output, Result: row.Result, State: row.State, Error: row.Error}
}

func (r *runner) execStep(ctx context.Context, st *Step, sc *scope, path string) (stepVal, error) {
	row, err := r.s.st.getStep(ctx, r.run.ID, path)
	if err != nil {
		return stepVal{State: StepFailed, Error: err.Error()}, err
	}
	if row != nil && stepDone(row.State) {
		v := valOf(row)
		sc.set(st.ID, v)
		if (row.State == StepFailed || row.State == StepCancelled) && st.OnError != "continue" {
			return v, &stepFailed{path: path, err: row.Error}
		}
		return v, nil
	}
	if row == nil {
		row = &StepInfo{Path: path, StepID: st.ID, Kind: st.Kind, State: StepPending}
	}
	now := r.s.now()

	// Guard.
	if st.If != "" && st.Kind != KindIf {
		ok, err := EvalBool(st.If, r.vars(sc, nil))
		if err != nil {
			return r.fail(ctx, st, sc, row, fmt.Errorf("if: %w", err))
		}
		if !ok {
			row.State, row.Output, row.Result, row.Error = StepSkipped, nil, nil, ""
			row.StartedAt, row.FinishedAt = now, now
			row.Detail = "condition false: " + st.If
			r.save(ctx, row)
			v := valOf(row)
			sc.set(st.ID, v)
			r.s.progress(ctx, r.run, r.wf, row)
			return v, nil
		}
	}

	resuming := row.State == StepRunning || row.State == StepWaiting
	if !resuming {
		if row.State == StepRetry {
			row.TaskID, row.ApprovalID, row.QuestionID, row.ChildRunID, row.Detail = "", "", "", "", ""
		}
		row.Attempt++
		row.StartedAt, row.FinishedAt = now, time.Time{}
		row.Error = ""
	}
	row.State = StepRunning
	r.save(ctx, row)
	sc.set(st.ID, stepVal{State: StepRunning})

	sctx, cancel := ctx, context.CancelFunc(func() {})
	if st.Timeout > 0 {
		sctx, cancel = context.WithTimeoutCause(ctx, time.Duration(st.Timeout), errTimeout)
	}
	defer cancel()

	var v stepVal
	for try := 0; ; try++ {
		v, err = r.do(sctx, st, sc, path, row, resuming)
		if err == nil || try >= st.Retries || sctx.Err() != nil || errors.As(err, new(permanentError)) {
			break
		}
		r.s.log.Warn("workflow step failed, retrying", "run", r.run.ID, "step", path, "try", try+1, "err", err)
		if serr := r.s.sleep(sctx, backoff(try)); serr != nil {
			err = serr
			break
		}
		resuming = false
		row.TaskID, row.ApprovalID, row.QuestionID, row.ChildRunID, row.Detail = "", "", "", "", ""
		row.Attempt++
		row.State = StepRunning
		r.save(ctx, row)
	}
	if err != nil {
		if errors.Is(context.Cause(ctx), errShutdown) {
			return stepVal{State: row.State}, context.Cause(ctx)
		}
		if errors.Is(context.Cause(ctx), errCancelled) {
			row.State, row.Error, row.FinishedAt = StepCancelled, "cancelled", r.s.now()
			r.save(ctx, row)
			v := valOf(row)
			sc.set(st.ID, v)
			return v, errCancelled
		}
		if errors.Is(context.Cause(sctx), errTimeout) {
			err = fmt.Errorf("timed out after %s", time.Duration(st.Timeout))
		}
		return r.fail(ctx, st, sc, row, err)
	}
	if v.State == "" {
		v.State = StepSucceeded
	}
	row.State, row.Output, row.Result, row.Error = v.State, v.Output, v.Result, ""
	row.FinishedAt = r.s.now()
	r.save(ctx, row)
	sc.set(st.ID, v)
	r.s.progress(ctx, r.run, r.wf, row)
	return v, nil
}

func backoff(try int) time.Duration {
	d := time.Second << try
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func (r *runner) fail(ctx context.Context, st *Step, sc *scope, row *StepInfo, err error) (stepVal, error) {
	var sf *stepFailed
	msg := err.Error()
	if errors.As(err, &sf) {
		msg = sf.Error()
	}
	row.State, row.Error, row.FinishedAt = StepFailed, msg, r.s.now()
	if row.StartedAt.IsZero() {
		row.StartedAt = row.FinishedAt
	}
	r.save(ctx, row)
	v := valOf(row)
	sc.set(st.ID, v)
	r.s.progress(ctx, r.run, r.wf, row)
	if st.OnError == "continue" {
		return v, nil
	}
	return v, &stepFailed{path: row.Path, err: msg}
}

func (r *runner) do(ctx context.Context, st *Step, sc *scope, path string, row *StepInfo, resuming bool) (stepVal, error) {
	switch st.Kind {
	case KindAgent:
		return r.doAgent(ctx, st, sc, row)
	case KindTool:
		return r.doTool(ctx, st, sc, row, resuming)
	case KindApprove:
		return r.doApprove(ctx, st, sc, row)
	case KindAsk:
		return r.doAsk(ctx, st, sc, row)
	case KindWait:
		return r.doWait(ctx, st, sc, row)
	case KindWorkflow:
		return r.doWorkflow(ctx, st, sc, row)
	case KindForeach:
		return r.doForeach(ctx, st, sc, path)
	case KindParallel:
		return r.doParallel(ctx, st, sc, path)
	case KindIf:
		ok, err := EvalBool(st.If, r.vars(sc, nil))
		if err != nil {
			return stepVal{}, permanentError{err}
		}
		branch, name := st.Then, "then"
		if !ok {
			branch, name = st.Else, "else"
		}
		last, err := r.execSeq(ctx, branch, sc, path+"/")
		return stepVal{Output: last.Output, Result: map[string]any{"branch": name, "condition": ok}}, err
	case KindGroup:
		last, err := r.execSeq(ctx, st.Do, sc, path+"/")
		return stepVal{Output: last.Output, Result: last.Result}, err
	}
	return stepVal{}, permanent("unknown step kind %q", st.Kind)
}

// ---------------------------------------------------------------- agent

func isTerminalTask(s nexusv1.TaskState) bool {
	return s == nexusv1.TaskState_TASK_STATE_SUCCEEDED || s == nexusv1.TaskState_TASK_STATE_FAILED ||
		s == nexusv1.TaskState_TASK_STATE_CANCELLED
}

func (r *runner) doAgent(ctx context.Context, st *Step, sc *scope, row *StepInfo) (stepVal, error) {
	prompt, err := RenderString(st.Task, r.vars(sc, nil))
	if err != nil {
		return stepVal{}, permanentError{fmt.Errorf("task: %w", err)}
	}
	if r.run.DryRun {
		row.Detail = mustJSON(map[string]any{"would_run": "agent", "agent": st.Agent, "model": st.Model, "task": prompt})
		return stepVal{State: StepDryRun, Output: fmt.Sprintf("[dry run: agent %s]", st.Agent),
			Result: map[string]any{"dry_run": true, "agent": st.Agent, "task": prompt}}, nil
	}
	te, ok := r.s.tasks()
	if !ok {
		return stepVal{}, permanent("no task engine available")
	}
	if row.TaskID == "" {
		params := map[string]any{"workflow_id": r.wf.ID, "workflow_run_id": r.run.ID, "workflow_step": row.Path}
		if st.Model != "" {
			params["model"] = st.Model
		}
		ps, err := structpb.NewStruct(params)
		if err != nil {
			return stepVal{}, permanentError{err}
		}
		t, err := te.CreateTask(ctx, &nexusv1.CreateTaskRequest{
			Prompt: prompt, AgentId: st.Agent, Params: ps, Source: "workflow",
			IdempotencyKey: fmt.Sprintf("%s/%s#%d", r.run.ID, row.Path, row.Attempt),
		})
		if err != nil {
			return stepVal{}, fmt.Errorf("create task for agent %s: %w", st.Agent, err)
		}
		row.TaskID, row.State = t.GetId(), StepWaiting
		r.save(ctx, row)
	}
	var task *nexusv1.Task
	err = r.s.waitFor(ctx, func(c context.Context) (bool, error) {
		t, err := te.GetTask(c, row.TaskID)
		if err != nil {
			return false, err
		}
		task = t
		return isTerminalTask(t.GetState()), nil
	})
	if err != nil {
		if errors.Is(context.Cause(ctx), errCancelled) || errors.Is(context.Cause(ctx), errTimeout) {
			if cerr := te.CancelTask(context.WithoutCancel(ctx), row.TaskID); cerr != nil {
				r.s.log.Warn("workflow: cancel agent task", "task", row.TaskID, "err", cerr)
			}
		}
		return stepVal{}, err
	}
	res := map[string]any{}
	if task.GetResult() != nil {
		res = task.GetResult().AsMap()
	}
	res["task_id"] = task.GetId()
	if task.GetState() != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
		state := strings.ToLower(strings.TrimPrefix(task.GetState().String(), "TASK_STATE_"))
		msg := fmt.Sprintf("agent %s task %s %s", st.Agent, task.GetId(), state)
		if task.GetSummary() != "" {
			msg += ": " + task.GetSummary()
		}
		return stepVal{}, errors.New(msg)
	}
	return stepVal{Output: task.GetSummary(), Result: res}, nil
}

// ---------------------------------------------------------------- tool

func actionClass(name string) (nexusv1.ActionClass, bool) {
	n := "ACTION_CLASS_" + strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
	v, ok := nexusv1.ActionClass_value[n]
	return nexusv1.ActionClass(v), ok
}

// isExternal reports whether a tool class acts outside Nexus (these get
// idempotency protection on resume and are skipped in dry runs).
func isExternal(c nexusv1.ActionClass) bool {
	switch c {
	case nexusv1.ActionClass_ACTION_CLASS_READ, nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
		nexusv1.ActionClass_ACTION_CLASS_COMPUTE:
		return false
	}
	return true // WRITE_EXTERNAL, CODE_PUSH, SPEND, DESTRUCTIVE, DEVICE_CONTROL, UNSPECIFIED
}

func (r *runner) idemKey(st *Step, sc *scope, row *StepInfo) (string, error) {
	if st.IdempotencyKey != "" {
		return RenderString(st.IdempotencyKey, r.vars(sc, nil))
	}
	return "wf:" + r.run.ID + "/" + row.Path, nil
}

func (r *runner) doTool(ctx context.Context, st *Step, sc *scope, row *StepInfo, resuming bool) (stepVal, error) {
	tool, ok := r.s.h.Tools().Get(st.Tool)
	if !ok {
		return stepVal{}, permanent("unknown tool %q", st.Tool)
	}
	in, err := RenderValue(st.With, r.vars(sc, nil))
	if err != nil {
		return stepVal{}, permanentError{fmt.Errorf("with: %w", err)}
	}
	if in == nil {
		in = map[string]any{}
	}
	class := tool.Spec().ActionClass
	if r.run.DryRun && isExternal(class) {
		row.Detail = mustJSON(map[string]any{"would_run": "tool", "tool": st.Tool, "input": in, "action_class": class.String()})
		return stepVal{State: StepDryRun, Output: fmt.Sprintf("[dry run: tool %s]", st.Tool),
			Result: map[string]any{"dry_run": true, "tool": st.Tool, "input": in}}, nil
	}
	key, err := r.idemKey(st, sc, row)
	if err != nil {
		return stepVal{}, permanentError{fmt.Errorf("idempotency_key: %w", err)}
	}
	if isExternal(class) && resuming && row.IdempotencyKey != "" {
		return stepVal{}, permanent("interrupted while calling external tool %s (idempotency key %s); the outcome is unknown — check it, then retry the run to call it again with the same key", st.Tool, row.IdempotencyKey)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return stepVal{}, permanentError{err}
	}
	row.IdempotencyKey = key
	r.save(ctx, row)
	res, err := tool.Invoke(ctx, kernel.ToolCall{ID: key, AgentID: "workflow:" + r.wf.ID, Input: raw})
	if err != nil {
		return stepVal{}, fmt.Errorf("tool %s: %w", st.Tool, err)
	}
	var out any
	if len(res.Output) > 0 {
		if err := json.Unmarshal(res.Output, &out); err != nil {
			out = string(res.Output)
		}
	}
	return stepVal{Output: out, Result: out}, nil
}

// ---------------------------------------------------------------- approve

type approvalGetter interface {
	Get(ctx context.Context, id string) (*nexusv1.Approval, error)
}

func getApproval(ctx context.Context, b kernel.ApprovalBroker, id string) (*nexusv1.Approval, error) {
	if g, ok := b.(approvalGetter); ok {
		return g.Get(ctx, id)
	}
	list, err := b.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		if a.GetId() == id {
			return a, nil
		}
	}
	return nil, fmt.Errorf("%w: approval %s", ErrNotFound, id)
}

func (r *runner) doApprove(ctx context.Context, st *Step, sc *scope, row *StepInfo) (stepVal, error) {
	a := st.Approve
	vars := r.vars(sc, nil)
	title, err := RenderString(a.Title, vars)
	if err != nil {
		return stepVal{}, permanentError{fmt.Errorf("title: %w", err)}
	}
	summary, err := RenderString(a.Summary, vars)
	if err != nil {
		return stepVal{}, permanentError{fmt.Errorf("summary: %w", err)}
	}
	pv, err := RenderValue(a.Preview, vars)
	if err != nil {
		return stepVal{}, permanentError{fmt.Errorf("preview: %w", err)}
	}
	if r.run.DryRun {
		row.Detail = mustJSON(map[string]any{"would_run": "approve", "title": title, "summary": summary, "preview": pv})
		return stepVal{State: StepDryRun, Output: "approved", Result: map[string]any{"state": "approved", "dry_run": true}}, nil
	}
	broker, ok := r.s.approvals()
	if !ok {
		return stepVal{}, permanent("approval required but the approvals module is not enabled")
	}
	if row.ApprovalID == "" {
		payload := map[string]any{}
		if m, ok := pv.(map[string]any); ok {
			for k, v := range m {
				payload[k] = v
			}
		}
		payload["workflow"] = map[string]any{"id": r.wf.ID, "run_id": r.run.ID, "step": row.Path}
		ps, err := structpb.NewStruct(jsonClean(payload).(map[string]any))
		if err != nil {
			return stepVal{}, permanentError{fmt.Errorf("preview: %w", err)}
		}
		req := &nexusv1.Approval{Title: title, Summary: summary, Payload: ps, AgentId: "workflow:" + r.wf.ID}
		if c, ok := actionClass(a.ActionClass); ok && a.ActionClass != "" {
			req.ActionClass = c
		}
		switch strings.ToLower(a.Level) {
		case "biometric":
			req.Level = nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC
		case "approve":
			req.Level = nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
		}
		if a.Expires > 0 {
			req.ExpiresAt = timestamppb.New(r.s.now().Add(time.Duration(a.Expires)))
		}
		created, err := broker.Request(ctx, req)
		if err != nil {
			return stepVal{}, fmt.Errorf("request approval: %w", err)
		}
		row.ApprovalID, row.State = created.GetId(), StepWaiting
		r.save(ctx, row)
	}
	var got *nexusv1.Approval
	err = r.s.waitFor(ctx, func(c context.Context) (bool, error) {
		a, err := getApproval(c, broker, row.ApprovalID)
		if err != nil {
			return false, err
		}
		got = a
		return a.GetState() != nexusv1.ApprovalState_APPROVAL_STATE_PENDING, nil
	})
	if err != nil {
		return stepVal{}, err
	}
	state := strings.ToLower(strings.TrimPrefix(got.GetState().String(), "APPROVAL_STATE_"))
	if got.GetState() != nexusv1.ApprovalState_APPROVAL_STATE_APPROVED {
		return stepVal{}, permanent("approval %s: %s", state, title)
	}
	return stepVal{Output: "approved", Result: map[string]any{"state": state, "approval_id": got.GetId()}}, nil
}

// jsonClean round-trips through JSON so structpb accepts every value.
func jsonClean(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// ---------------------------------------------------------------- ask

// Answer is the stored answer to an ask step.
type Answer struct {
	Choice string `json:"choice,omitempty"`
	Text   string `json:"text,omitempty"`
}

func (r *runner) doAsk(ctx context.Context, st *Step, sc *scope, row *StepInfo) (stepVal, error) {
	a := st.Ask
	q, err := RenderString(a.Question, r.vars(sc, nil))
	if err != nil {
		return stepVal{}, permanentError{fmt.Errorf("ask: %w", err)}
	}
	if r.run.DryRun {
		ans := a.Default
		if ans == "" && len(a.Choices) > 0 {
			ans = a.Choices[0]
		}
		row.Detail = mustJSON(map[string]any{"would_run": "ask", "question": q, "choices": a.Choices, "assumed": ans})
		return stepVal{State: StepDryRun, Output: ans, Result: map[string]any{"choice": ans, "dry_run": true}}, nil
	}
	if row.QuestionID == "" {
		row.QuestionID, row.State = newID("q_"), StepWaiting
		r.save(ctx, row)
		r.s.publish(ctx, &nexusv1.Event{RunId: r.run.ID, Actor: "workflow:" + r.wf.ID,
			Body: &nexusv1.Event_Question{Question: &nexusv1.Question{
				QuestionId: row.QuestionID, Text: q, Choices: a.Choices, AllowFreeText: a.AllowFreeText,
			}}})
	}
	var raw string
	err = r.s.waitFor(ctx, func(c context.Context) (bool, error) {
		s, err := r.s.st.stepAnswer(c, r.run.ID, row.Path)
		raw = s
		return s != "", err
	})
	if err != nil {
		return stepVal{}, err
	}
	var ans Answer
	if json.Unmarshal([]byte(raw), &ans) != nil {
		ans.Text = raw
	}
	out := ans.Choice
	if out == "" {
		out = ans.Text
	}
	row.Answer = raw
	return stepVal{Output: out, Result: map[string]any{"choice": ans.Choice, "text": ans.Text}}, nil
}

// ---------------------------------------------------------------- wait

func (r *runner) doWait(ctx context.Context, st *Step, sc *scope, row *StepInfo) (stepVal, error) {
	w := st.Wait
	if r.run.DryRun {
		row.Detail = mustJSON(map[string]any{"would_run": "wait", "duration": time.Duration(w.Duration).String(), "event": w.Event, "until": w.Until})
		return stepVal{State: StepDryRun, Output: "", Result: map[string]any{"dry_run": true}}, nil
	}
	row.State = StepWaiting
	r.save(ctx, row)
	if w.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, time.Duration(w.Timeout), errTimeout)
		defer cancel()
	}
	switch {
	case w.Duration > 0:
		// Measured from the step's first start, so a restart does not reset it.
		left := row.StartedAt.Add(time.Duration(w.Duration)).Sub(r.s.now())
		if left > 0 {
			if err := r.s.sleep(ctx, left); err != nil {
				return stepVal{}, err
			}
		}
		return stepVal{Output: "", Result: map[string]any{"waited": time.Duration(w.Duration).String()}}, nil
	case w.Until != "":
		err := r.s.waitFor(ctx, func(context.Context) (bool, error) {
			return EvalBool(w.Until, r.vars(sc, nil))
		})
		if err != nil {
			return stepVal{}, err
		}
		return stepVal{Output: true, Result: map[string]any{"condition": true}}, nil
	default:
		return r.waitEvent(ctx, w, sc, row)
	}
}

func (r *runner) waitEvent(ctx context.Context, w *WaitSpec, sc *scope, row *StepInfo) (stepVal, error) {
	bus, ok := r.s.bus()
	if !ok {
		return stepVal{}, permanent("wait for event: no event bus")
	}
	since := int64(-1)
	if s, ok := strings.CutPrefix(row.Detail, "since:"); ok {
		since, _ = strconv.ParseInt(s, 10, 64)
	}
	if since < 0 {
		seq, err := bus.LastSeq(ctx)
		if err != nil {
			return stepVal{}, err
		}
		since = seq
		row.Detail = "since:" + strconv.FormatInt(seq, 10)
		r.save(ctx, row)
	}
	sub, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := bus.Subscribe(sub, since, kernel.EventFilter{})
	if err != nil {
		return stepVal{}, err
	}
	for {
		select {
		case <-ctx.Done():
			return stepVal{}, context.Cause(ctx)
		case ev, ok := <-ch:
			if !ok {
				if ctx.Err() != nil {
					return stepVal{}, context.Cause(ctx)
				}
				return stepVal{}, errors.New("event subscription closed")
			}
			hit := false
			for _, t := range triggers.Topics(ev) {
				if triggers.MatchTopic(w.Event, t) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			em := triggers.EventMap(ev)
			if w.Where != "" {
				ok, err := EvalBool(w.Where, r.vars(sc, map[string]any{"event": em}))
				if err != nil || !ok {
					continue
				}
			}
			return stepVal{Output: em, Result: em}, nil
		}
	}
}

// ---------------------------------------------------------------- workflow call

func (r *runner) doWorkflow(ctx context.Context, st *Step, sc *scope, row *StepInfo) (stepVal, error) {
	if row.ChildRunID == "" {
		if r.run.depth+1 > MaxDepth {
			return stepVal{}, permanent("workflow call depth exceeds %d", MaxDepth)
		}
		in, err := RenderValue(st.Inputs, r.vars(sc, nil))
		if err != nil {
			return stepVal{}, permanentError{fmt.Errorf("inputs: %w", err)}
		}
		inputs, _ := in.(map[string]any)
		id, err := r.s.StartWith(ctx, StartOptions{
			WorkflowID: st.Workflow, Inputs: inputs, Source: "workflow:" + r.wf.ID, Trigger: r.run.Trigger,
			DryRun: r.run.DryRun, ParentRunID: r.run.ID, depth: r.run.depth + 1,
			IdempotencyKey: fmt.Sprintf("%s/%s#%d", r.run.ID, row.Path, row.Attempt),
		})
		if err != nil {
			return stepVal{}, permanentError{fmt.Errorf("start workflow %s: %w", st.Workflow, err)}
		}
		row.ChildRunID, row.State = id, StepWaiting
		r.save(ctx, row)
	}
	var child *RunInfo
	err := r.s.waitFor(ctx, func(c context.Context) (bool, error) {
		run, err := r.s.st.getRun(c, row.ChildRunID)
		child = run
		return err == nil && runTerminal(run.State), err
	})
	if err != nil {
		if errors.Is(context.Cause(ctx), errCancelled) || errors.Is(context.Cause(ctx), errTimeout) {
			_ = r.s.Cancel(context.WithoutCancel(ctx), row.ChildRunID)
		}
		return stepVal{}, err
	}
	steps, _ := r.s.st.listSteps(ctx, child.ID)
	res := map[string]any{"run_id": child.ID, "state": child.State}
	for _, s := range steps {
		if !strings.Contains(s.Path, "/") && !strings.Contains(s.Path, "[") {
			res[s.StepID] = map[string]any{"output": s.Output, "result": s.Result, "state": s.State}
		}
	}
	if child.State != RunSucceeded {
		return stepVal{}, fmt.Errorf("workflow %s run %s %s: %s", st.Workflow, child.ID, child.State, child.Error)
	}
	return stepVal{Output: child.Output, Result: res}, nil
}

// ---------------------------------------------------------------- foreach / parallel

func (r *runner) doForeach(ctx context.Context, st *Step, sc *scope, path string) (stepVal, error) {
	v, err := Eval(st.Foreach, r.vars(sc, nil))
	if err != nil {
		return stepVal{}, permanentError{fmt.Errorf("foreach: %w", err)}
	}
	var items []any
	switch t := v.(type) {
	case nil:
	case []any:
		items = t
	default:
		return stepVal{}, permanent("foreach: expression is %T, want a list", v)
	}
	outputs := make([]any, len(items))
	results := make([]any, len(items))
	errs := make([]error, len(items))
	n := st.Concurrency
	if n < 1 {
		n = 1
	}
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := false
	for i, item := range items {
		mu.Lock()
		stop := failed
		mu.Unlock()
		if stop || ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, item any) {
			defer wg.Done()
			defer func() { <-sem }()
			isc := newScope(sc, map[string]any{st.As: item, "item": item, "index": i})
			last, err := r.execSeq(ctx, st.Do, isc, fmt.Sprintf("%s[%d]/", path, i))
			outputs[i] = last.Output
			results[i] = isc.snapshot()
			if err != nil {
				errs[i] = err
				mu.Lock()
				failed = true
				mu.Unlock()
			}
		}(i, item)
	}
	wg.Wait()
	res := map[string]any{"items": results, "count": len(items)}
	for _, e := range errs {
		if e != nil {
			return stepVal{Output: outputs, Result: res}, e
		}
	}
	if err := ctx.Err(); err != nil {
		return stepVal{}, context.Cause(ctx)
	}
	return stepVal{Output: outputs, Result: res}, nil
}

func (r *runner) doParallel(ctx context.Context, st *Step, sc *scope, path string) (stepVal, error) {
	vals := make([]stepVal, len(st.Do))
	errs := make([]error, len(st.Do))
	var wg sync.WaitGroup
	for i, b := range st.Do {
		wg.Add(1)
		go func(i int, b *Step) {
			defer wg.Done()
			vals[i], errs[i] = r.execStep(ctx, b, sc, path+"/"+b.ID)
		}(i, b)
	}
	wg.Wait()
	out := map[string]any{}
	for i, b := range st.Do {
		out[b.ID] = vals[i].Output
	}
	return stepVal{Output: out, Result: out}, errors.Join(errs...)
}
