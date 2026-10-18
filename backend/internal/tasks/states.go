package tasks

import (
	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Short aliases for the task states.
const (
	stQueued          = nexusv1.TaskState_TASK_STATE_QUEUED
	stRouting         = nexusv1.TaskState_TASK_STATE_ROUTING
	stNeedsInput      = nexusv1.TaskState_TASK_STATE_NEEDS_INPUT
	stAssigned        = nexusv1.TaskState_TASK_STATE_ASSIGNED
	stRunning         = nexusv1.TaskState_TASK_STATE_RUNNING
	stWaitingApproval = nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL
	stWaitingInput    = nexusv1.TaskState_TASK_STATE_WAITING_INPUT
	stEscalating      = nexusv1.TaskState_TASK_STATE_ESCALATING
	stSucceeded       = nexusv1.TaskState_TASK_STATE_SUCCEEDED
	stFailed          = nexusv1.TaskState_TASK_STATE_FAILED
	stCancelled       = nexusv1.TaskState_TASK_STATE_CANCELLED
)

// transitions is the validated task state machine.
//
// It is the lifecycle of docs/ARCHITECTURE.md §3 plus these documented
// extensions, which the diagram leaves implicit:
//   - every non-terminal state may go to cancelled (cancel / kill switch);
//   - every non-terminal state may go to failed (routing, catalog or dispatch
//     errors, node failure while waiting);
//   - waiting_input may end directly in succeeded (the runtime finished
//     without needing the answer);
//   - assigned / waiting_* / succeeded / failed may go to escalating ("Try
//     harder"): escalation is a new run on the same task, so it is the only
//     way out of a finished task. cancelled is final;
//   - routing may go back to queued when the router picked a team (the task
//     becomes the team run's root);
//   - queued may end in succeeded when a team run without orchestrator was
//     completed by a hub-driven strategy (parallel / pipeline / debate).
var transitions = map[nexusv1.TaskState][]nexusv1.TaskState{
	stQueued:          {stRouting, stAssigned, stCancelled, stFailed, stSucceeded},
	stRouting:         {stNeedsInput, stAssigned, stCancelled, stFailed, stQueued},
	stNeedsInput:      {stRouting, stCancelled, stFailed},
	stAssigned:        {stRunning, stEscalating, stCancelled, stFailed},
	stRunning:         {stWaitingApproval, stWaitingInput, stEscalating, stSucceeded, stFailed, stCancelled},
	stWaitingApproval: {stRunning, stEscalating, stFailed, stCancelled},
	stWaitingInput:    {stRunning, stEscalating, stSucceeded, stFailed, stCancelled},
	stEscalating:      {stAssigned, stFailed, stCancelled},
	stSucceeded:       {stEscalating},
	stFailed:          {stEscalating},
	stCancelled:       {},
}

// CanTransition reports whether from → to is a legal task state change.
func CanTransition(from, to nexusv1.TaskState) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// IsTerminal reports whether s is succeeded, failed or cancelled.
func IsTerminal(s nexusv1.TaskState) bool {
	return s == stSucceeded || s == stFailed || s == stCancelled
}

// stateName is the short lower-case name used in messages ("waiting_input").
func stateName(s nexusv1.TaskState) string {
	n := s.String()
	const p = "TASK_STATE_"
	if len(n) > len(p) {
		n = n[len(p):]
	}
	b := []byte(n)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
