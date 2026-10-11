package node

import (
	"context"
	"errors"
	"log/slog"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// SvcApprovalVerifier is the service name under which the security module
// (node side) publishes an ApprovalVerifier.
const SvcApprovalVerifier = "node.approval_verifier"

// ApprovalVerifier implements edge verification (SECURITY.md §1 Layer 5):
// before an approval decision reaches a runtime the node checks the
// device signature, the action hash binding, revocation and freshness.
type ApprovalVerifier interface {
	VerifyApproval(ctx context.Context, res *nexusv1.ApprovalResolution) error
}

// DevApprovalVerifier accepts every resolution and logs a warning.
// It is used when no verifier service is installed and strict_approvals is off.
type DevApprovalVerifier struct{ Log *slog.Logger }

func (v DevApprovalVerifier) VerifyApproval(_ context.Context, res *nexusv1.ApprovalResolution) error {
	if v.Log != nil {
		v.Log.Warn("approval accepted WITHOUT edge verification (dev mode)",
			"run", res.GetRunId(), "approval", res.GetApproval().GetId())
	}
	return nil
}

// rejectVerifier is used in strict mode without a real verifier.
type rejectVerifier struct{}

func (rejectVerifier) VerifyApproval(context.Context, *nexusv1.ApprovalResolution) error {
	return errors.New("no approval verifier installed (strict_approvals)")
}
