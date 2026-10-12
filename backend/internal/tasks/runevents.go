package tasks

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// HandleRunEvent ingests an event produced by a runtime on a node.
//
// The event is normalised (task/run ids from the hub's records, actor
// node:<id>, seq/at cleared for the bus) and re-published. Events of the
// task's current run also drive the state machine:
//
//	any event while assigned      → running (the node accepted the run)
//	RunOutput                     → appended to the run output (escalation context)
//	Question                      → waiting_input
//	Approval                      → registered with the ApprovalBroker → waiting_approval
//	                                (no broker: the task fails closed and the run is cancelled)
//	CostRecorded                  → accumulated into run and task cost
//	TaskUpdated running           → running (resume)
//	TaskUpdated succeeded/failed/cancelled → terminal, with summary/result/cost
//
// Events of stale runs (superseded by escalation, or of finished tasks) are
// re-published for audit but never change state.
func (e *Engine) HandleRunEvent(ctx context.Context, nodeID string, re *nexusv1.RunEvent) error {
	ev := re.GetEvent()
	if ev == nil || ev.Body == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("run event without body"))
	}
	if ev.RunId == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("run event without run_id"))
	}
	run, err := e.loadRun(ctx, ev.RunId)
	if err != nil {
		return err
	}
	if run.nodeID != "" && run.nodeID != nodeID {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("run %s does not belong to node %s", run.id, nodeID))
	}
	if ev.TaskId != "" && ev.TaskId != run.taskID {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run %s belongs to task %s, not %s", run.id, run.taskID, ev.TaskId))
	}
	ev = proto.Clone(ev).(*nexusv1.Event)
	ev.Seq, ev.At = 0, nil
	ev.TaskId, ev.RunId = run.taskID, run.id
	actor := "node:" + nodeID
	ev.Actor = actor

	// Approvals are registered with the broker before taking the engine lock.
	if a := ev.GetApproval(); a != nil {
		return e.handleApproval(ctx, nodeID, run, a)
	}

	_, err = e.mutate(ctx, run.taskID, actor, func(r *taskRec) error {
		if run.nodeID == "" {
			if err := e.setRunNode(ctx, run.id, nodeID); err != nil {
				return err
			}
		}
		current := r.task.CurrentRunId == run.id && !IsTerminal(r.task.State)
		if tu := ev.GetTaskUpdated(); tu != nil {
			// The hub publishes its own TaskUpdated with the full task.
			if !current {
				return e.endRun(ctx, run.id, tu.GetTask())
			}
			return e.applyNodeState(ctx, r, run.id, tu.GetTask())
		}
		r.extra = append(r.extra, ev)
		if !current {
			return nil
		}
		if err := e.accept(ctx, r, run.id); err != nil {
			return err
		}
		switch b := ev.Body.(type) {
		case *nexusv1.Event_RunOutput:
			return e.appendRunOutput(ctx, run.id, b.RunOutput.GetText())
		case *nexusv1.Event_Question:
			q := b.Question
			if q.QuestionId == "" {
				q.QuestionId = newID("q_")
			}
			r.questionID = q.QuestionId
			return r.to(stWaitingInput)
		case *nexusv1.Event_Cost:
			return e.addCost(ctx, r, run.id, b.Cost.GetCost())
		}
		return nil
	})
	return err
}

// accept moves an assigned task to running on the first event from its node.
func (e *Engine) accept(ctx context.Context, r *taskRec, runID string) error {
	if r.task.State != stAssigned {
		return nil
	}
	if _, err := e.db.ExecContext(ctx, `UPDATE runs SET state = ? WHERE id = ? AND state = ?`, runRunning, runID, runAssigned); err != nil {
		return err
	}
	return r.to(stRunning)
}

// applyNodeState handles a TaskUpdated reported by the node for the current run.
func (e *Engine) applyNodeState(ctx context.Context, r *taskRec, runID string, nt *nexusv1.Task) error {
	switch s := nt.GetState(); s {
	case stRunning:
		if r.task.State == stAssigned {
			return e.accept(ctx, r, runID)
		}
		if r.task.State == stWaitingInput || r.task.State == stWaitingApproval {
			r.questionID, r.approvalID = "", ""
			return r.to(stRunning)
		}
		return nil
	case stSucceeded, stFailed, stCancelled:
		if err := e.accept(ctx, r, runID); err != nil {
			return err
		}
		if nt.GetSummary() != "" {
			r.task.Summary = nt.Summary
		}
		if nt.GetResult() != nil {
			r.task.Result = nt.Result
		}
		if nt.GetEpisode() != nil {
			r.task.Episode = nt.Episode
		}
		if nt.GetCost() != nil && r.task.Cost == nil {
			r.task.Cost = nt.Cost
		}
		r.questionID, r.approvalID = "", ""
		if err := e.endRun(ctx, runID, nt); err != nil {
			return err
		}
		if err := r.to(s); err != nil {
			return err
		}
		if r.task.ParentId == "" && s != stCancelled {
			r.extra = append(r.extra, resultNotice(r.task))
		}
		return nil
	default:
		e.log.Debug("ignoring node task state", "task", r.task.Id, "state", stateName(s))
		return nil
	}
}

func (e *Engine) endRun(ctx context.Context, runID string, nt *nexusv1.Task) error {
	state := ""
	switch nt.GetState() {
	case stSucceeded:
		state = runSucceeded
	case stFailed:
		state = runFailed
	case stCancelled:
		state = runCancelled
	default:
		return nil
	}
	return e.setRunState(ctx, runID, state, nt.GetSummary())
}

func resultNotice(t *nexusv1.Task) *nexusv1.Event {
	title := t.Title
	if t.State == stFailed {
		title = "Failed: " + title
	}
	return &nexusv1.Event{TaskId: t.Id, RunId: t.CurrentRunId, Body: &nexusv1.Event_Notice{Notice: &nexusv1.Notice{
		Id: newID("ntc_"), Kind: nexusv1.NoticeKind_NOTICE_KIND_RESULT, Title: title, Body: t.Summary,
		Notes: noteRefs(t.Episode),
	}}}
}

func noteRefs(n *nexusv1.NoteRef) []*nexusv1.NoteRef {
	if n == nil {
		return nil
	}
	return []*nexusv1.NoteRef{n}
}

// addCost accumulates c into the run's and the task's cost.
func (e *Engine) addCost(ctx context.Context, r *taskRec, runID string, c *nexusv1.Cost) error {
	if c == nil {
		return nil
	}
	rc, err := e.runCost(ctx, runID)
	if err != nil {
		return err
	}
	if err := e.setRunCost(ctx, runID, sumCost(rc, c)); err != nil {
		return err
	}
	r.task.Cost = sumCost(r.task.Cost, c)
	return nil
}

// sumCost returns a+b. Amounts in a different currency than a's are not added
// (single-currency accounting; the first currency seen wins).
func sumCost(a, b *nexusv1.Cost) *nexusv1.Cost {
	if a == nil {
		return proto.Clone(b).(*nexusv1.Cost)
	}
	out := proto.Clone(a).(*nexusv1.Cost)
	out.InputTokens += b.InputTokens
	out.OutputTokens += b.OutputTokens
	if b.Model != "" {
		out.Model = b.Model
	}
	if b.Runtime != "" {
		out.Runtime = b.Runtime
	}
	if b.Amount != nil {
		switch {
		case out.Amount == nil:
			out.Amount = proto.Clone(b.Amount).(*nexusv1.Money)
		case out.Amount.Currency == b.Amount.Currency || b.Amount.Currency == "":
			out.Amount.MinorUnits += b.Amount.MinorUnits
		case out.Amount.Currency == "":
			out.Amount.Currency = b.Amount.Currency
			out.Amount.MinorUnits += b.Amount.MinorUnits
		}
	}
	return out
}

// handleApproval registers a node's approval request and parks the task.
func (e *Engine) handleApproval(ctx context.Context, nodeID string, run *runRec, a *nexusv1.Approval) error {
	r, err := e.loadTask(ctx, run.taskID)
	if err != nil {
		return err
	}
	if r.task.CurrentRunId != run.id || IsTerminal(r.task.State) {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %s is not active", run.id))
	}
	broker, ok := e.approvals()
	if !ok {
		_, err := e.fail(ctx, run.taskID, "approval required but the approvals module is not enabled")
		e.cancelOnNode(ctx, nodeID, run.id, "approval unavailable")
		return err
	}
	a = proto.Clone(a).(*nexusv1.Approval)
	a.TaskId, a.RunId, a.AgentId, a.NodeId = run.taskID, run.id, run.agentID, nodeID
	created, err := broker.Request(ctx, a)
	if err != nil {
		return fmt.Errorf("request approval: %w", err)
	}
	_, err = e.mutate(ctx, run.taskID, "node:"+nodeID, func(r *taskRec) error {
		if r.task.CurrentRunId != run.id || IsTerminal(r.task.State) {
			return nil
		}
		if err := e.accept(ctx, r, run.id); err != nil {
			return err
		}
		r.approvalID = created.Id
		if r.task.State == stWaitingInput {
			// The runtime moved on without an answer; drop the question.
			r.questionID = ""
			if err := r.to(stRunning); err != nil {
				return err
			}
		}
		return r.to(stWaitingApproval)
	})
	return err
}

// OnApprovalResolved is called by the approvals module when an approval is
// decided or expires. Either way the task returns to running: the node's
// runtime receives the decision and continues or ends the run itself.
func (e *Engine) OnApprovalResolved(ctx context.Context, a *nexusv1.Approval) error {
	if a.GetTaskId() == "" {
		return nil
	}
	_, err := e.mutate(ctx, a.TaskId, actorHub, func(r *taskRec) error {
		if r.approvalID != a.Id || r.task.State != stWaitingApproval {
			return nil
		}
		r.approvalID = ""
		switch a.State {
		case nexusv1.ApprovalState_APPROVAL_STATE_APPROVED:
			return r.to(stRunning)
		case nexusv1.ApprovalState_APPROVAL_STATE_DENIED, nexusv1.ApprovalState_APPROVAL_STATE_EXPIRED:
			// The runtime decides what a denial means: agent loops and coding
			// agents continue (e.g. try another way), the shell and echo
			// runtimes end the run as FAILED themselves. The node receives the
			// resolution through the dispatcher, so the hub only resumes here.
			return r.to(stRunning)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// AskUser parks the task in waiting_input and publishes a Question (used by
// the ask_user hub tool). It returns the question id.
func (e *Engine) AskUser(ctx context.Context, taskID, runID string, q *nexusv1.Question) (string, error) {
	q = proto.Clone(q).(*nexusv1.Question)
	if q.QuestionId == "" {
		q.QuestionId = newID("q_")
	}
	_, err := e.mutate(ctx, taskID, "agent", func(r *taskRec) error {
		if runID != "" && r.task.CurrentRunId != runID {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %s is not the task's current run", runID))
		}
		if IsTerminal(r.task.State) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("task already finished"))
		}
		if err := e.accept(ctx, r, r.task.CurrentRunId); err != nil {
			return err
		}
		r.questionID = q.QuestionId
		r.extra = append(r.extra, &nexusv1.Event{RunId: r.task.CurrentRunId, Actor: "agent:" + r.task.AgentId,
			Body: &nexusv1.Event_Question{Question: q}})
		return r.to(stWaitingInput)
	})
	if err != nil {
		return "", err
	}
	return q.QuestionId, nil
}
