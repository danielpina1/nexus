package agentloop

import (
	"context"

	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Approver adapts a run's approval gate to kernel.Approver, so run tools
// (e.g. the browser's checkout click) can ask through the gate that the
// run's Resolve serves. After a refusal it emits TaskUpdated RUNNING (Resume)
// before returning.
func Approver(g *approvalgate.Gate) kernel.Approver {
	if g == nil {
		return nil
	}
	return gateApprover{g}
}

type gateApprover struct{ g *approvalgate.Gate }

func (a gateApprover) Approve(ctx context.Context, req kernel.ApprovalRequest) (kernel.ApprovalOutcome, error) {
	out, err := a.g.Check(ctx, approvalgate.Request{Class: req.Class, Title: req.Title, Summary: req.Summary, Payload: req.Payload})
	res := kernel.ApprovalOutcome{Approved: out.Approved, Level: out.Level, ApprovalID: out.ApprovalID, Reason: out.Reason}
	if err != nil {
		res.Approved = false
		return res, err
	}
	if !out.Approved {
		a.g.Resume()
	}
	return res, nil
}

// BindGate binds g to the run's late-bound gate (spec.Gate; nil-safe).
func BindGate(rg *kernel.RunGate, g *approvalgate.Gate) {
	if rg != nil && g != nil {
		rg.Set(Approver(g))
	}
}
