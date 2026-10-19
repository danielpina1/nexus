package tasks

import (
	"context"
	"fmt"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Waiting for spawn_agent children (docs/SWARMS.md §6). A run that used
// spawn_agent may end while its sub-tasks still run. The task does not
// finish then: it stays running in phase "waiting_children" (its answer so
// far is the summary) until every such child has ended. Then:
//
//  1. preferred: the parent agent is resumed with a new run whose context
//     carries `sub_task_results` (state, summary and result of each child)
//     and whose hint asks for the final answer; that answer goes through the
//     usual checks (output schema, review). The resumed run may spawn more
//     children; after MaxChildResumes rounds the next fallback applies.
//  2. fallback (no run allowed any more: tree budget / runs / deadline used
//     up, agent gone, resume rounds exhausted): the task succeeds with its
//     last answer and the child results appended.
//
// Cancelling the parent cancels its children (cancel cascades by
// parent_id); tree limits apply to the resumed run like to any other run.
// Team members delegated with team.delegate are not waited for (when a team
// root ends its members are cancelled, see onTerminal); critic tasks are
// handled by the review loop.

// phaseWaitingChildren is Task.phase while a task waits for its spawn_agent
// sub-tasks.
const phaseWaitingChildren = "waiting_children"

// MaxChildResumes bounds how many times a task is resumed after its
// sub-tasks ended (each resumed run may spawn new sub-tasks).
const MaxChildResumes = 3

// sourceSpawn is the Task.source of spawn_agent sub-tasks.
const sourceSpawn = "agent"

// spawnedChildren returns the spawn_agent sub-tasks of a task.
func (e *Engine) spawnedChildren(ctx context.Context, parentID string) ([]*nexusv1.Task, error) {
	kids, err := e.ChildTasks(ctx, parentID)
	if err != nil {
		return nil, err
	}
	out := kids[:0]
	for _, k := range kids {
		if k.Source == sourceSpawn && k.RootId != k.Id {
			out = append(out, k)
		}
	}
	return out, nil
}

// runningChildren counts the unfinished spawn_agent sub-tasks of a task.
func (e *Engine) runningChildren(ctx context.Context, parentID string) int {
	kids, err := e.spawnedChildren(ctx, parentID)
	if err != nil {
		e.log.Warn("count sub-tasks", "task", parentID, "err", err)
		return 0
	}
	n := 0
	for _, k := range kids {
		if !IsTerminal(k.State) {
			n++
		}
	}
	return n
}

// waitForChildren parks a task whose run succeeded while n of its
// spawn_agent sub-tasks still run (under the engine lock). The answer so far
// becomes the summary; childrenSettled finishes or resumes the task.
func (e *Engine) waitForChildren(ctx context.Context, r *taskRec, runID string, nt *nexusv1.Task, n int) error {
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
	r.task.Phase = phaseWaitingChildren
	r.notify = true
	r.extra = append(r.extra, hubOutput(runID, fmt.Sprintf("Waiting for %d sub-task%s to finish before the final answer…\n", n, plural(n))))
	id, root := r.task.Id, r.task.RootId
	r.post = append(r.post, func(ctx context.Context) {
		// The task no longer occupies a slot: held sub-tasks may start.
		e.pump(ctx, root)
		// A child may have ended between the count and this mutation.
		e.childrenSettled(ctx, id)
	})
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// childrenSettled resumes or finishes a task in phase waiting_children once
// none of its spawn_agent sub-tasks runs any more. It is idempotent.
func (e *Engine) childrenSettled(ctx context.Context, parentID string) {
	p, err := e.loadTask(ctx, parentID)
	if err != nil || p.task.Phase != phaseWaitingChildren || IsTerminal(p.task.State) {
		return
	}
	kids, err := e.spawnedChildren(ctx, parentID)
	if err != nil {
		e.log.Warn("sub-task results", "task", parentID, "err", err)
		return
	}
	x, err := e.loadExt(ctx, parentID)
	if err != nil {
		e.log.Warn("sub-task results", "task", parentID, "err", err)
		return
	}
	reported := map[string]bool{}
	for _, id := range x.ChildrenReported {
		reported[id] = true
	}
	var fresh []*nexusv1.Task
	for _, k := range kids {
		if !IsTerminal(k.State) {
			return // still waiting
		}
		if !reported[k.Id] {
			fresh = append(fresh, k)
		}
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
			fmt.Sprintf("All %d sub-task%s finished; resuming the agent to assemble the final answer.\n", len(kids), plural(len(kids)))))
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
	hint := "The sub-tasks you started with spawn_agent have finished; their results are in the context under sub_task_results " +
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
		r.extra = append(r.extra, hubOutput(r.task.CurrentRunId, "All sub-tasks finished; their results were added to the answer ("+why+").\n"))
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
