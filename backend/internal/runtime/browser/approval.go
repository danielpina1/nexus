package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/danielpina1/nexus/backend/kernel"
)

// ApprovalTool is the hub tool the model uses to ask for explicit approval.
const ApprovalTool = "request_approval"

// Checkout gating
//
// The agent loop has no per-call approval gate: policy acts on a tool's
// static ActionClass, and browser.click is routine (COMPUTE). Whether a click
// spends money depends on the element, so browser.click classifies the target
// at call time. A click on a checkout/payment element (see Classifier) is
// refused unless the call carries an approval_id that this run obtained from
// request_approval and that the user APPROVED. The runtime wraps the run's
// request_approval tool to observe decisions (ledger below); ids the model
// invents, denied/expired approvals and approvals from other runs never
// unlock a click. Each approval unlocks exactly one checkout click.

// ledger records approvals granted in this run.
type ledger struct {
	mu       sync.Mutex
	approved map[string]bool // id → unused
	n        int
}

func newLedger() *ledger { return &ledger{approved: map[string]bool{}} }

func (l *ledger) add(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.approved[id] = true
}

// consume uses an approval once. It returns an explanation when the id
// cannot be used.
func (l *ledger) consume(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	unused, ok := l.approved[id]
	switch {
	case id == "":
		return fmt.Errorf("missing approval_id")
	case !ok:
		return fmt.Errorf("approval %q was not granted in this run", id)
	case !unused:
		return fmt.Errorf("approval %q was already used for a checkout click", id)
	}
	l.approved[id] = false
	return nil
}

func (l *ledger) synthID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n++
	return fmt.Sprintf("apr_browser_%d", l.n)
}

// approvalTool wraps request_approval to record approved decisions.
type approvalTool struct {
	kernel.Tool
	l *ledger
}

func (t approvalTool) Invoke(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	res, err := t.Tool.Invoke(ctx, c)
	if err != nil {
		return res, err
	}
	id, ok := parseApproval(res.Output)
	if !ok {
		return res, nil
	}
	if id == "" {
		id = t.l.synthID()
	}
	t.l.add(id)
	// Tell the model which id unlocks the checkout click.
	var m map[string]any
	if json.Unmarshal(res.Output, &m) != nil || m == nil {
		m = map[string]any{"result": json.RawMessage(res.Output)}
	}
	m["browser_approval_id"] = id
	if b, err := json.Marshal(m); err == nil {
		res.Output = b
	}
	return res, nil
}

// wrapTools returns tools with request_approval wrapped.
func wrapTools(tools []kernel.Tool, l *ledger) (out []kernel.Tool, hasApproval bool) {
	for _, t := range tools {
		if t.Spec().Name == ApprovalTool {
			t = approvalTool{Tool: t, l: l}
			hasApproval = true
		}
		out = append(out, t)
	}
	return out, hasApproval
}

// parseApproval extracts the approval id and whether it was approved from a
// request_approval result. It tolerates the shapes a hub may return:
// {"approved":true,"approval_id":"apr_…"}, {"id":…,"state":"APPROVAL_STATE_APPROVED"},
// {"decision":"approved"}, {"approval":{…}}, state as enum number 2.
// It is conservative: any negative signal (denied, pending, approved:false…)
// means not approved.
func parseApproval(out []byte) (id string, approved bool) {
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return "", false
	}
	id, pos, neg := approvalFrom(m, 0)
	return id, pos && !neg
}

func approvalFrom(m map[string]any, depth int) (id string, pos, neg bool) {
	for _, k := range []string{"approval_id", "id"} {
		if s, ok := m[k].(string); ok && s != "" {
			id = s
			break
		}
	}
	for _, k := range []string{"approved", "approve"} {
		if b, ok := m[k].(bool); ok {
			pos = pos || b
			neg = neg || !b
		}
	}
	for _, k := range []string{"state", "decision", "status", "result"} {
		switch v := m[k].(type) {
		case string:
			s := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(v)), "APPROVAL_STATE_")
			switch s {
			case "APPROVED", "APPROVE", "ALLOW", "ALLOWED", "GRANTED", "YES":
				pos = true
			case "DENIED", "DENY", "REJECTED", "EXPIRED", "PENDING", "NO", "UNSPECIFIED", "CANCELLED":
				neg = true
			}
		case float64:
			if k == "state" {
				pos = pos || v == 2
				neg = neg || v != 2
			}
		}
	}
	if depth == 0 {
		if sub, ok := m["approval"].(map[string]any); ok {
			sid, sp, sn := approvalFrom(sub, 1)
			if id == "" {
				id = sid
			}
			pos, neg = pos || sp, neg || sn
		}
	}
	return id, pos, neg
}
