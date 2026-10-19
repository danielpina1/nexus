package tasks

import (
	"context"
	"fmt"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// A task never finishes before its sub-tasks (docs/SWARMS.md §6.1). This
// holds for every child, whatever created it: spawn_agent, team delegation
// (members and nested team runs), hub strategies, critics, workflow or
// sandbox children. When a run of a task ends (SUCCEEDED) and the task
//
//   - has children that have not finished, or
//   - has finished children whose results its agent never received (they
//     were not returned by team.await / delegate wait / a hub strategy, nor
//     handed to an earlier resumed run) — e.g. a sub-task that failed,
//
// the task does not succeed: it stays running in phase "waiting_children"
// (its answer so far is the summary) until every child has ended. Then:
//
//  1. preferred: the parent agent is resumed with a new run whose context
//     carries `sub_task_results` (state, summary and result of each child
//     it has not seen, failures included) and whose hint asks for the final
//     answer; that answer goes through the usual checks (output schema,
//     review). The resumed run may start more children; after
//     MaxChildResumes rounds the fallback applies.
//  2. fallback (no run allowed any more: tree budget / runs / deadline used
//     up, no agent (hub strategy roots), agent gone, resume rounds
//     exhausted): the task succeeds with its last answer and the unseen
//     child results appended.
//
// The same gate applies when a reviewed answer is approved and when a hub
// strategy finishes a team run without orchestrator. Cancelling the parent
// cancels its children; tree limits apply to the resumed run like to any
// other run. Critic tasks are reported through the review loop.

// phaseWaitingChildren is Task.phase while a task waits for its sub-tasks.
const phaseWaitingChildren = "waiting_children"

// MaxChildResumes bounds how many times a task is resumed after its
// sub-tasks ended (each resumed run may start new sub-tasks).
const MaxChildResumes = 3

// sourceSpawn is the Task.source of spawn_agent sub-tasks.
const sourceSpawn = "agent"

// childState is what a task's children say about whether it may finish.
type childState struct {
	running    int             // children not finished yet
	unreported []*nexusv1.Task // finished children whose result the agent has not received
}

func (c childState) blocks() bool { return c.running > 0 || len(c.unreported) > 0 }

// children inspects a task's children (critics excluded from unreported:
// the review loop hands their verdicts over).
func (e *Engine) children(ctx context.Context, parentID string) childState {
	var c childState
	kids, err := e.ChildTasks(ctx, parentID)
	if err != nil || len(kids) == 0 {
		if err != nil {
			e.log.Warn("inspect sub-tasks", "task", parentID, "err", err)
		}
		return c
	}
	x, err := e.loadExt(ctx, parentID)
	if err != nil {
		e.log.Warn("inspect sub-tasks", "task", parentID, "err", err)
		x = &taskExt{}
	}
	seen := map[string]bool{}
	for _, id := range x.ChildrenReported {
		seen[id] = true
	}
	for _, k := range kids {
		switch {
		case !IsTerminal(k.State):
			c.running++
		case !seen[k.Id] && k.Role != RoleCritic:
			c.unreported = append(c.unreported, k)
		}
	}
	return c
}

// markReported records that the task's agent received these children's
// results (team.await, delegate wait, strategies, resumed runs).
func (e *Engine) markReported(ctx context.Context, parentID string, ids []string) {
	if len(ids) == 0 {
		return
	}
	if _, err := e.updateExt(ctx, parentID, func(x *taskExt) {
		seen := map[string]bool{}
		for _, id := range x.ChildrenReported {
			seen[id] = true
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				x.ChildrenReported = append(x.ChildrenReported, id)
			}
		}
	}); err != nil {
		e.log.Warn("record reported sub-tasks", "task", parentID, "err", err)
	}
}

// parkForChildren puts a task (under the engine lock, its answer already in
// r.task) into phase waiting_children and schedules the check that resumes
// or finishes it.
func (e *Engine) parkForChildren(r *taskRec, c childState) {
	r.task.Phase = phaseWaitingChildren
	r.notify = true
	text := fmt.Sprintf("Waiting for %d sub-task%s to finish before the final answer…\n", c.running, plural(c.running))
	if c.running == 0 {
		text = fmt.Sprintf("%d sub-task%s finished without the agent seeing the result; handing %s over…\n",
			len(c.unreported), plural(len(c.unreported)), map[bool]string{true: "it", false: "them"}[len(c.unreported) == 1])
	}
	r.extra = append(r.extra, hubOutput(r.task.CurrentRunId, text))
	id, root := r.task.Id, r.task.RootId
	r.post = append(r.post, func(ctx context.Context) {
		// The task no longer occupies a slot: held sub-tasks may start.
		e.pump(ctx, root)
		// A child may have ended between the check and this mutation.
		e.childrenSettled(ctx, id)
	})
}

// waitForChildren parks a task whose run succeeded while its children block
// it (under the engine lock). The answer so far becomes the summary;
// childrenSettled finishes or resumes the task.
func (e *Engine) waitForChildren(ctx context.Context, r *taskRec, runID string, nt *nexusv1.Task, c childState) error {
	if err := e.accept(ctx, r, runID); err != nil {
		return err
	}
	if r.task.State == stWaitingInput || r.task.State == stWaitingApproval {
		r.questionID, r.approvalID = "", ""
		if err := r.to(stRunning); err != nil {
			return err
		}
	}
	if err := e.setRunState(ctx, runID, runSucceeded, nt.GetSummary()); err != nil {
		return err
	}
	if nt.GetSummary() != "" {
		r.task.Summary = nt.Summary
	}
	if nt.GetResult() != nil {
		r.task.Result = nt.Result
	}
	if nt.GetCost() != nil && r.task.Cost == nil {
		r.task.Cost = nt.Cost
	}
	e.parkForChildren(r, c)
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// childrenSettled resumes or finishes a task in phase waiting_children once
// none of its sub-tasks runs any more. It is idempotent.
func (e *Engine) childrenSettled(ctx context.Context, parentID string) {
	p, err := e.loadTask(ctx, parentID)
	if err != nil || p.task.Phase != phaseWaitingChildren || IsTerminal(p.task.State) {
		return
	}
	c := e.children(ctx, parentID)
	if c.running > 0 {
		return // still waiting
	}
	x, err := e.loadExt(ctx, parentID)
	if err != nil {
		e.log.Warn("sub-task results", "task", parentID, "err", err)
		return
	}
	fresh := c.unreported
	if len(fresh) == 0 {
		e.finishWithChildren(ctx, parentID, nil, "nothing new to report")
		return
	}
	results := waitResults(fresh)

	// Can the agent run again?
	why := ""
	switch {
	case x.ChildResumes >= MaxChildResumes:
		why = fmt.Sprintf("the agent was already resumed %d times", x.ChildResumes)
	case p.task.AgentId == "":
		why = "the task has no agent"
	}
	if why == "" {
		if _, err := e.resolveAgent(ctx, parentID, p.task.AgentId); err != nil {
			why = err.Error()
		}
	}
	if why == "" {
		if _, err := e.checkRun(ctx, p.task.RootId); err != nil {
			why = err.Error()
		}
	}
	if why == "" {
		if _, ok := e.dispatcher(); !ok {
			why = "no dispatcher available"
		}
	}
	if why != "" {
		e.finishWithChildren(ctx, parentID, fresh, why)
		return
	}

	// Claim the resume (several children may end at once).
	var prev *runRec
	var agentID string
	var tier nexusv1.Tier
	if _, err := e.mutate(ctx, parentID, actorHub, func(r *taskRec) error {
		if r.task.Phase != phaseWaitingChildren || IsTerminal(r.task.State) {
			return nil
		}
		pr, err := e.loadRun(ctx, r.task.CurrentRunId)
		if err != nil {
			return err
		}
		prev, agentID, tier = pr, r.task.AgentId, r.task.Tier
		r.task.Phase = ""
		r.extra = append(r.extra, hubOutput(r.task.CurrentRunId,
			fmt.Sprintf("All sub-tasks finished; resuming the agent with %d result%s to assemble the final answer.\n", len(fresh), plural(len(fresh)))))
		return r.to(stEscalating)
	}); err != nil || prev == nil {
		if err != nil {
			e.log.Warn("sub-task results: resume", "task", parentID, "err", err)
		}
		return
	}
	ids := make([]string, len(fresh))
	for i, k := range fresh {
		ids[i] = k.Id
	}
	if _, err := e.updateExt(ctx, parentID, func(x *taskExt) {
		x.ChildResumes++
		x.ChildrenReported = append(x.ChildrenReported, ids...)
		if x.Context == nil {
			x.Context = map[string]any{}
		}
		x.Context["sub_task_results"] = resultsJSON(results)
	}); err != nil {
		e.log.Warn("sub-task results: save", "task", parentID, "err", err)
	}
	hint := "Your sub-tasks have finished; their results are in the context under sub_task_results " +
		"(task_id, agent_id, state, summary, result). Do not start them again. Using those results and your previous answer " +
		"(previous_attempt.output), reply with the complete final answer for the original task. Report failed sub-tasks honestly; never invent their results."
	if _, err := e.assign(ctx, parentID, agentID, assignOpts{tier: tier, hint: hint, previousRun: prev}); err != nil {
		e.log.Warn("sub-task results: resume run", "task", parentID, "err", err)
	}
}

// finishWithChildren ends a waiting task with its last answer plus the
// results of its sub-tasks (when it cannot be resumed).
func (e *Engine) finishWithChildren(ctx context.Context, parentID string, kids []*nexusv1.Task, why string) {
	e.log.Info("sub-tasks finished; parent not resumed", "task", parentID, "reason", why)
	_, err := e.mutate(ctx, parentID, actorHub, func(r *taskRec) error {
		if r.task.Phase != phaseWaitingChildren || IsTerminal(r.task.State) {
			return nil
		}
		r.task.Phase = ""
		if len(kids) > 0 {
			var b strings.Builder
			b.WriteString(strings.TrimSpace(r.task.Summary))
			b.WriteString("\n\n## Sub-task results\n")
			for _, k := range kids {
				fmt.Fprintf(&b, "\n### %s (%s)\n%s\n", nonEmpty(k.Title, k.Id), stateName(k.State), strings.TrimSpace(truncate(k.Summary, 8<<10)))
			}
			r.task.Summary = strings.TrimSpace(b.String())
			res := map[string]any{}
			if r.task.Result != nil {
				res = r.task.Result.AsMap()
			}
			res["sub_tasks"] = resultsJSON(waitResults(kids))
			r.task.Result = toStruct(res)
		}
		if len(kids) > 0 {
			r.extra = append(r.extra, hubOutput(r.task.CurrentRunId, "All sub-tasks finished; their results were added to the answer ("+why+").\n"))
		}
		if err := r.to(stSucceeded); err != nil {
			return err
		}
		if wantsResultNotice(r) {
			r.extra = append(r.extra, resultNotice(r.task))
		}
		return nil
	})
	if err != nil {
		e.log.Warn("finish task after sub-tasks", "task", parentID, "err", err)
	}
}
