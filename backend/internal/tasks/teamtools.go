package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Team tools (docs/SWARMS.md §3). They are hub tools, injected into the
// allow-list of team runs at assignment (OrchestratorTools for the root,
// MemberTools for members, BlackboardTools when the team has a blackboard)
// and scoped by the calling run: the hub resolves the caller's task from
// the run, so a run can only act on its own team run.

const (
	classRead    = nexusv1.ActionClass_ACTION_CLASS_READ
	classCompute = nexusv1.ActionClass_ACTION_CLASS_COMPUTE
)

func (e *Engine) teamTools() []kernel.Tool {
	tool := func(name, desc, schema string, class nexusv1.ActionClass, f func(context.Context, *teamCall) (any, error), orchOnly bool) kernel.Tool {
		return kernel.ToolFunc{S: kernel.ToolSpec{Name: name, Description: desc, InputSchema: json.RawMessage(schema), ActionClass: class},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				tc, err := e.teamCaller(ctx, c, name, orchOnly)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				out, err := f(ctx, tc)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				return kernel.ToolOutput(out)
			}}
	}
	const waitProps = `"wait":{"type":"boolean","description":"block until finished and return the result(s)"},"timeout":{"type":"integer","description":"max seconds to wait (default 600)"}`
	return []kernel.Tool{
		tool("team.members", "List the team's members: roles, agents, what they are for, their input/output schemas and current load (active/queued/finished).",
			`{"type":"object","properties":{}}`, classRead, e.ttMembers, true),
		tool("team.delegate", "Give a self-contained task to a team member (by role). Returns the new task id at once, or its result with wait: true.",
			`{"type":"object","properties":{"role":{"type":"string","description":"member role (see team.members)"},"agent":{"type":"string","description":"member agent id (alternative to role)"},"task":{"type":"string","description":"complete instructions: members do not see your conversation"},"inputs":{"type":"object","description":"structured inputs (member's input schema)"},`+waitProps+`},"required":["task"]}`,
			classCompute, e.ttDelegate, true),
		tool("team.delegate_many", "Fan out several tasks to members in parallel (each item like team.delegate). Returns the task ids, or all results with wait: true.",
			`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"role":{"type":"string"},"agent":{"type":"string"},"task":{"type":"string"},"inputs":{"type":"object"}},"required":["task"]}},`+waitProps+`},"required":["items"]}`,
			classCompute, e.ttDelegateMany, true),
		tool("team.await", "Wait until member tasks finish (all, or the first with any: true) and return their state, summary and structured result. Pending tasks are listed when the timeout elapses: call again. Without task_ids it covers every sub-task whose result this run has not received yet (also after a hub restart).",
			`{"type":"object","properties":{"task_ids":{"type":"array","items":{"type":"string"},"description":"default: your sub-tasks that are unfinished or whose result you have not received yet"},"any":{"type":"boolean"},"timeout":{"type":"integer","description":"max seconds to wait (default 600)"}}}`,
			classRead, e.ttAwait, true),
		tool("team.status", "The team run's task tree: every task with role, state, phase, cost and a short summary, plus totals and limits.",
			`{"type":"object","properties":{}}`, classRead, e.ttStatus, true),
		tool("team.cancel", "Cancel a member task of this team run (and its sub-tasks).",
			`{"type":"object","properties":{"task_id":{"type":"string"},"reason":{"type":"string"}},"required":["task_id"]}`,
			classCompute, e.ttCancel, true),
		tool("team.spawn", "Create a temporary specialist (no agent note) for a task no member fits. It gets your policy and at most your own tools and runtimes.",
			`{"type":"object","properties":{"role":{"type":"string","description":"short name, e.g. translator"},"instructions":{"type":"string","description":"its system instructions"},"task":{"type":"string"},"runtime":{"type":"string","description":"one of your runtimes (default: yours)"},"tools":{"type":"array","items":{"type":"string"},"description":"subset of your tools (default: all of yours except coding-only ones)"},"coding":{"type":"boolean","description":"a coding specialist (only from a coding orchestrator; needed for anthrex.* tools)"},"inputs":{"type":"object"},`+waitProps+`},"required":["role","instructions","task"]}`,
			classCompute, e.ttSpawn, true),
		tool("team.run_strategy", "Let the hub run a fixed collaboration pattern with the members and return all results: parallel (all do the task), pipeline (members in order, each builds on the previous output) or debate (members argue in rounds; a judge member, if any, decides).",
			`{"type":"object","properties":{"strategy":{"type":"string","enum":["parallel","pipeline","debate"]},"task":{"type":"string","description":"default: your own task"},"roles":{"type":"array","items":{"type":"string"},"description":"members to involve, in order (default: all)"},"timeout":{"type":"integer","description":"max seconds per wait (default 600)"}},"required":["strategy"]}`,
			classCompute, e.ttRunStrategy, true),
		tool("team.message", "Send a short message to team members: to = a role, a task id, \"orchestrator\" or \"all\". Running members receive it at once, others when they start.",
			`{"type":"object","properties":{"to":{"type":"string"},"text":{"type":"string"}},"required":["to","text"]}`,
			classCompute, e.ttMessage, false),
		tool("team.report", "Attach a structured result (any JSON) to your answer for the orchestrator. Your final reply is still your summary.",
			`{"type":"object","properties":{"result":{"description":"any JSON value"},"summary":{"type":"string"}},"required":["result"]}`,
			classCompute, e.ttReport, false),
		tool("team.blackboard.read", "Read the team's shared blackboard: one key, or all keys with their values.",
			`{"type":"object","properties":{"key":{"type":"string"}}}`, classRead, e.ttBBRead, false),
		tool("team.blackboard.write", "Write a key on the team's shared blackboard (replaces the value). Values are any JSON.",
			`{"type":"object","properties":{"key":{"type":"string"},"value":{"description":"any JSON value"}},"required":["key","value"]}`,
			classCompute, e.ttBBWrite, false),
		tool("team.blackboard.append", "Append to a blackboard key: to a list, or a new line of text.",
			`{"type":"object","properties":{"key":{"type":"string"},"value":{"description":"any JSON value"}},"required":["key","value"]}`,
			classCompute, e.ttBBAppend, false),
	}
}

// teamCall is a validated team tool call.
type teamCall struct {
	call   kernel.ToolCall
	caller *taskRec
	tree   *treeRec
}

func (tc *teamCall) input(v any) error {
	if err := kernel.ToolInput(tc.call, v); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid input: %w", err))
	}
	return nil
}

// teamCaller resolves and checks the calling task.
func (e *Engine) teamCaller(ctx context.Context, c kernel.ToolCall, tool string, orchOnly bool) (*teamCall, error) {
	taskID, err := e.callTask(ctx, c)
	if err != nil {
		return nil, err
	}
	r, err := e.loadTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if c.RunID != "" && r.task.CurrentRunId != "" && c.RunID != r.task.CurrentRunId {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("this run is no longer the task's current run"))
	}
	if IsTerminal(r.task.State) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the calling task already finished"))
	}
	tree, err := e.loadTree(ctx, r.task.RootId)
	if err != nil {
		return nil, err
	}
	if tree == nil || tree.team == nil || r.task.TeamId == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s: this task is not part of a team run", tool))
	}
	if orchOnly && r.task.Id != tree.rootID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s is only available to the team's orchestrator", tool))
	}
	return &teamCall{call: c, caller: r, tree: tree}, nil
}

// waitTimeout converts a tool's timeout (seconds) to a bounded duration.
func (e *Engine) waitTimeout(sec int) time.Duration {
	d := e.cfg.Teams.AwaitDefault
	if sec > 0 {
		d = time.Duration(sec) * time.Second
	}
	return min(d, e.cfg.Teams.AwaitMax)
}

func (e *Engine) ttMembers(ctx context.Context, tc *teamCall) (any, error) {
	tasks, err := e.treeTasks(ctx, tc.tree.rootID)
	if err != nil {
		return nil, err
	}
	var spawned []map[string]any
	for _, t := range tasks {
		if IsEphemeral(t.AgentId) && t.Id != tc.tree.rootID {
			spawned = append(spawned, map[string]any{"task_id": t.Id, "role": t.Role, "state": stateName(t.State)})
		}
	}
	return map[string]any{"team": tc.tree.team.Name, "strategy": strategyName(tc.tree.team.Strategy),
		"members": e.roster(ctx, tc.tree, tasks), "spawned": spawned, "limits": limitsMap(tc.tree.limits)}, nil
}

type waitArgs struct {
	Wait    bool `json:"wait"`
	Timeout int  `json:"timeout"`
}

func (e *Engine) ttDelegate(ctx context.Context, tc *teamCall) (any, error) {
	var in struct {
		delegation
		waitArgs
	}
	if err := tc.input(&in); err != nil {
		return nil, err
	}
	child, err := e.delegate(ctx, tc.caller, tc.tree, in.delegation)
	if err != nil {
		return nil, err
	}
	if !in.Wait {
		return map[string]any{"task_id": child.Id, "role": child.Role, "state": stateName(child.State), "phase": child.Phase,
			"note": "use team.await to collect the result"}, nil
	}
	return e.awaitOut(ctx, tc, []string{child.Id}, false, e.waitTimeout(in.Timeout))
}

func (e *Engine) ttDelegateMany(ctx context.Context, tc *teamCall) (any, error) {
	var in struct {
		Items []delegation `json:"items"`
		waitArgs
	}
	if err := tc.input(&in); err != nil {
		return nil, err
	}
	if len(in.Items) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("items is empty"))
	}
	var ids []string
	var created []map[string]any
	var errs []map[string]any
	for i, d := range in.Items {
		child, err := e.delegate(ctx, tc.caller, tc.tree, d)
		if err != nil {
			errs = append(errs, map[string]any{"index": i, "role": nonEmpty(d.Role, d.Agent), "error": err.Error()})
			continue
		}
		ids = append(ids, child.Id)
		created = append(created, map[string]any{"index": i, "task_id": child.Id, "role": child.Role, "state": stateName(child.State), "phase": child.Phase})
	}
	if len(ids) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no task could be delegated: %v", errs))
	}
	if !in.Wait {
		return map[string]any{"tasks": created, "errors": errs, "note": "use team.await to collect the results"}, nil
	}
	out, err := e.awaitOut(ctx, tc, ids, false, e.waitTimeout(in.Timeout))
	if err != nil {
		return nil, err
	}
	out["errors"] = errs
	return out, nil
}

// awaitOut waits and formats the team.await output.
func (e *Engine) awaitOut(ctx context.Context, tc *teamCall, ids []string, anyDone bool, timeout time.Duration) (map[string]any, error) {
	idsAny := make([]any, len(ids))
	for i, id := range ids {
		idsAny[i] = id
	}
	e.teamEvent(ctx, tc.tree, nexusv1.TeamEventKind_TEAM_EVENT_KIND_AWAITING, tc.caller.task, fmt.Sprintf("waiting for %d task(s)", len(ids)),
		map[string]any{"task_ids": idsAny, "timeout_seconds": int(timeout / time.Second)})
	done, pending, err := e.waitTasks(ctx, ids, anyDone, timeout)
	if err != nil {
		if e.bg.Err() != nil {
			// The hub is stopping: nothing is marked as received, so the
			// same call after the restart returns every result.
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("the hub is restarting; call team.await again (no result is lost)"))
		}
		if len(done) == 0 {
			return nil, err
		}
	}
	sortTasksByID(done)
	e.markCollected(ctx, tc.caller.task, done)
	out := map[string]any{"done": waitResults(done), "timed_out": len(pending) > 0 && !anyDone}
	var p []map[string]any
	for _, t := range pending {
		p = append(p, map[string]any{"task_id": t.Id, "role": t.Role, "state": stateName(t.State), "phase": t.Phase})
	}
	if len(p) > 0 {
		out["pending"] = p
		if !anyDone {
			out["note"] = "some tasks are still running: call team.await again with their ids"
		}
	}
	return out, nil
}

func (e *Engine) ttAwait(ctx context.Context, tc *teamCall) (any, error) {
	var in struct {
		TaskIDs []string `json:"task_ids"`
		Any     bool     `json:"any"`
		Timeout int      `json:"timeout"`
	}
	if err := tc.input(&in); err != nil {
		return nil, err
	}
	ids := in.TaskIDs
	if len(ids) == 0 {
		var err error
		if ids, err = e.uncollected(ctx, tc.caller.task); err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return map[string]any{"done": []any{}, "note": "you have no unfinished sub-tasks and every result was already returned to you"}, nil
		}
	}
	for _, id := range ids {
		t, err := e.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		if t.RootId != tc.tree.rootID && !(isSubRoot(t) && t.ParentId == tc.caller.task.Id) {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("task %s is not part of this team run", id))
		}
	}
	return e.awaitOut(ctx, tc, ids, in.Any, e.waitTimeout(in.Timeout))
}

// uncollected returns the caller's sub-tasks that are unfinished or whose
// result the caller's current run has not received from team.await yet.
// After a hub restart (or when the orchestrator runs again) a plain
// team.await therefore still returns results that finished meanwhile.
func (e *Engine) uncollected(ctx context.Context, caller *nexusv1.Task) ([]string, error) {
	kids, err := e.ChildTasks(ctx, caller.Id)
	if err != nil {
		return nil, err
	}
	x, err := e.loadExt(ctx, caller.Id)
	if err != nil {
		return nil, err
	}
	got := map[string]bool{}
	if x.CollectedRun == caller.CurrentRunId {
		for _, id := range x.Collected {
			got[id] = true
		}
	}
	var ids []string
	for _, k := range kids {
		if !IsTerminal(k.State) || !got[k.Id] {
			ids = append(ids, k.Id)
		}
	}
	return ids, nil
}

// markCollected records that the caller's current run received the results
// of done.
func (e *Engine) markCollected(ctx context.Context, caller *nexusv1.Task, done []*nexusv1.Task) {
	if len(done) == 0 {
		return
	}
	if _, err := e.updateExt(ctx, caller.Id, func(x *taskExt) {
		if x.CollectedRun != caller.CurrentRunId {
			x.CollectedRun, x.Collected = caller.CurrentRunId, nil
		}
		seen := map[string]bool{}
		for _, id := range x.Collected {
			seen[id] = true
		}
		for _, t := range done {
			if !seen[t.Id] {
				x.Collected = append(x.Collected, t.Id)
				seen[t.Id] = true
			}
		}
	}); err != nil {
		e.log.Warn("team.await: record collected results", "task", caller.Id, "err", err)
	}
	ids := make([]string, len(done))
	for i, t := range done {
		ids[i] = t.Id
	}
	e.markReported(ctx, caller.Id, ids)
}

func (e *Engine) ttStatus(ctx context.Context, tc *teamCall) (any, error) {
	run, err := e.TeamRun(ctx, tc.tree.rootID)
	if err != nil {
		return nil, err
	}
	var tasks []map[string]any
	for _, rt := range run.Tasks {
		t := rt.Task
		row := map[string]any{"task_id": t.Id, "parent_id": t.ParentId, "role": t.Role, "agent": t.AgentId,
			"state": stateName(t.State), "runs": rt.Runs, "summary": truncate(t.Summary, 300)}
		if t.Phase != "" {
			row["phase"] = t.Phase
		}
		if t.Cost.GetAmount() != nil {
			row["cost"] = eur(t.Cost.Amount)
		}
		tasks = append(tasks, row)
	}
	out := map[string]any{"tasks": tasks, "total_tasks": run.TotalTasks, "total_runs": run.TotalRuns,
		"active": run.ActiveTasks, "queued": run.QueuedTasks, "limits": limitsMap(tc.tree.limits)}
	if c := run.TotalCost.GetAmount(); c != nil {
		out["total_cost"] = eur(c)
	}
	if run.Deadline != nil {
		out["time_left_seconds"] = int(time.Until(run.Deadline.AsTime()).Seconds())
	}
	return out, nil
}

func (e *Engine) ttCancel(ctx context.Context, tc *teamCall) (any, error) {
	var in struct {
		TaskID string `json:"task_id"`
		Reason string `json:"reason"`
	}
	if err := tc.input(&in); err != nil {
		return nil, err
	}
	t, err := e.GetTask(ctx, in.TaskID)
	if err != nil {
		return nil, err
	}
	if t.RootId != tc.tree.rootID || t.Id == tc.tree.rootID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("you can only cancel member tasks of this team run"))
	}
	changed, err := e.cancel(ctx, t.Id, nonEmpty(in.Reason, "cancelled by the orchestrator"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": t.Id, "cancelled": changed}, nil
}

func (e *Engine) ttSpawn(ctx context.Context, tc *teamCall) (any, error) {
	var in struct {
		spawnReq
		waitArgs
	}
	if err := tc.input(&in); err != nil {
		return nil, err
	}
	child, err := e.spawn(ctx, tc.caller, tc.tree, in.spawnReq)
	if err != nil {
		return nil, err
	}
	if !in.Wait {
		return map[string]any{"task_id": child.Id, "role": child.Role, "agent": child.AgentId, "state": stateName(child.State)}, nil
	}
	return e.awaitOut(ctx, tc, []string{child.Id}, false, e.waitTimeout(in.Timeout))
}

func (e *Engine) ttRunStrategy(ctx context.Context, tc *teamCall) (any, error) {
	var in struct {
		Strategy string   `json:"strategy"`
		Task     string   `json:"task"`
		Roles    []string `json:"roles"`
		Timeout  int      `json:"timeout"`
	}
	if err := tc.input(&in); err != nil {
		return nil, err
	}
	s, ok := strategyByName[strings.ToLower(strings.TrimSpace(in.Strategy))]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("strategy must be parallel, pipeline or debate"))
	}
	task := nonEmpty(strings.TrimSpace(in.Task), tc.caller.task.Prompt)
	// Keyed by run and arguments: the same call repeated by the same run
	// (e.g. after the hub restarted mid-call) reuses the member tasks.
	h := sha256.Sum256([]byte(strategyName(s) + "\x00" + task + "\x00" + strings.Join(in.Roles, ",")))
	prefix := fmt.Sprintf("rs:%s:%x:", tc.caller.task.CurrentRunId, h[:6])
	res, final, err := e.runStrategy(ctx, tc.caller, tc.tree, s, task, in.Roles, e.waitTimeout(in.Timeout), prefix)
	e.markReported(ctx, tc.caller.task.Id, resultIDs(res))
	out := map[string]any{"strategy": strategyName(s), "results": res, "final": truncate(final, 32<<10)}
	if err != nil {
		out["error"] = err.Error()
	}
	return out, nil
}

func (e *Engine) ttMessage(ctx context.Context, tc *teamCall) (any, error) {
	var in struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if err := tc.input(&in); err != nil {
		return nil, err
	}
	m, err := e.teamMessage(ctx, tc.tree, tc.caller.task, in.To, in.Text)
	if err != nil {
		return nil, err
	}
	return map[string]any{"message_id": m.Id, "delivered_to": m.DeliveredTo, "note": "members that are not running get it when they start"}, nil
}

func (e *Engine) ttReport(ctx context.Context, tc *teamCall) (any, error) {
	var in struct {
		Result  any    `json:"result"`
		Summary string `json:"summary"`
	}
	if err := tc.input(&in); err != nil {
		return nil, err
	}
	if in.Result == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("result is required"))
	}
	b, _ := json.Marshal(in.Result)
	if len(b) > maxBlackboardValue*2 {
		return nil, limitErr("result too large (%d bytes)", len(b))
	}
	if _, err := e.updateExt(ctx, tc.caller.task.Id, func(x *taskExt) { x.Reported = in.Result }); err != nil {
		return nil, err
	}
	e.teamEvent(ctx, tc.tree, nexusv1.TeamEventKind_TEAM_EVENT_KIND_REPORTED, tc.caller.task, truncate(nonEmpty(in.Summary, string(b)), 300), nil)
	return map[string]any{"ok": true, "note": "attached; finish with your final answer"}, nil
}

func (e *Engine) ttBBRead(ctx context.Context, tc *teamCall) (any, error) {
	var in struct {
		Key string `json:"key"`
	}
	if err := tc.input(&in); err != nil {
		return nil, err
	}
	entries, err := e.blackboard(ctx, tc.tree.rootID)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, en := range entries {
		if in.Key != "" && en.Key != in.Key {
			continue
		}
		out = append(out, map[string]any{"key": en.Key, "value": en.Value.AsInterface(), "by": en.AuthorRole, "version": en.Version})
	}
	if in.Key != "" && len(out) == 0 {
		return map[string]any{"key": in.Key, "found": false}, nil
	}
	return map[string]any{"entries": out}, nil
}

func (e *Engine) ttBBWrite(ctx context.Context, tc *teamCall) (any, error) {
	return e.ttBB(ctx, tc, false)
}

func (e *Engine) ttBBAppend(ctx context.Context, tc *teamCall) (any, error) {
	return e.ttBB(ctx, tc, true)
}

func (e *Engine) ttBB(ctx context.Context, tc *teamCall, appendMode bool) (any, error) {
	var in struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	if err := tc.input(&in); err != nil {
		return nil, err
	}
	en, err := e.bbWrite(ctx, tc.tree, tc.caller.task, in.Key, in.Value, appendMode)
	if err != nil {
		return nil, err
	}
	return map[string]any{"key": en.Key, "version": en.Version}, nil
}
