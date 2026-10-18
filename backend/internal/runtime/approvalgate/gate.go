// Package approvalgate implements "ask before acting" for node runtimes.
//
// A Gate belongs to one run. Before a risky action the runtime calls
// Check with the action's class, a human title/summary and the exact action
// payload. The gate computes the trust level (docs/AGENTS.md §7 defaults,
// mirrored from internal/policy; the agent's policy map can only tighten
// them) and:
//
//	AUTO       → proceeds immediately;
//	NOTIFY     → proceeds and (unless QuietNotify) emits a Notice;
//	APPROVE+   → emits an Approval event (id "apr_<ulid>", payload Struct,
//	             payload_hash = security/approval.ActionHash(payload),
//	             level, action class, 1h expiry, PENDING) followed by a
//	             TaskUpdated WAITING_APPROVAL, then blocks until Resolve
//	             delivers the matching ApprovalResolution (matched by
//	             approval id; the hub keeps node-supplied ids). After an
//	             approval it emits TaskUpdated RUNNING.
//
// Resolutions reaching Resolve have already passed the node's edge
// verification (internal/node ApprovalVerifier). The gate additionally binds
// the decision to the action it is about to run: a decision whose
// action_sha256 (or an approval whose payload_hash) differs from the hash of
// the payload the gate emitted is treated as a denial (SECURITY.md Layer 5).
//
// Check returns an error only when the wait is abandoned: ctx done or the
// gate closed (run cancelled/finished). Callers treat that as cancellation.
package approvalgate

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	secapproval "github.com/danielpina1/nexus/backend/internal/security/approval"
)

// DefaultTTL is how long an approval stays pending.
const DefaultTTL = time.Hour

// expiryGrace is how long past expiry the gate waits for the hub's expiry
// resolution before giving up locally.
const expiryGrace = time.Minute

// Defaults is the built-in level per action class (docs/AGENTS.md §7). It
// mirrors internal/policy.Defaults: where the table lists two levels the
// stricter one is used.
var Defaults = map[nexusv1.ActionClass]nexusv1.TrustLevel{
	nexusv1.ActionClass_ACTION_CLASS_READ:           nexusv1.TrustLevel_TRUST_LEVEL_AUTO,
	nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL: nexusv1.TrustLevel_TRUST_LEVEL_NOTIFY,
	nexusv1.ActionClass_ACTION_CLASS_COMPUTE:        nexusv1.TrustLevel_TRUST_LEVEL_AUTO,
	nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL: nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
	nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH:      nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
	nexusv1.ActionClass_ACTION_CLASS_SPEND:          nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC,
	nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE:    nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC,
	nexusv1.ActionClass_ACTION_CLASS_DEVICE_CONTROL: nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
}

// UnknownLevel applies to ACTION_CLASS_UNSPECIFIED and unknown classes (fail safe).
const UnknownLevel = nexusv1.TrustLevel_TRUST_LEVEL_APPROVE

// HumanInTheLoopTools are tools that themselves ask the user (a question or
// an explicit approval); gating them would ask twice, so runtimes call them
// without a gate check.
var HumanInTheLoopTools = map[string]bool{"ask_user": true, "request_approval": true}

// ErrClosed is returned by Check when the gate is closed while waiting.
var ErrClosed = errors.New("approvalgate: run ended while waiting for approval")

// ErrUnknownApproval is returned by Resolve for a resolution that matches no
// pending approval of this run.
var ErrUnknownApproval = errors.New("approvalgate: no pending approval matches the resolution")

// Level returns the trust level for class: the default (or levels[class] when
// levels is non-nil) tightened by the agent's policy map. Agent keys that are
// not action classes are ignored; an agent can never loosen a level.
func Level(agent *nexusv1.Agent, class nexusv1.ActionClass, levels map[nexusv1.ActionClass]nexusv1.TrustLevel) nexusv1.TrustLevel {
	if levels == nil {
		levels = Defaults
	}
	level, ok := levels[class]
	if !ok || level == nexusv1.TrustLevel_TRUST_LEVEL_UNSPECIFIED {
		level = UnknownLevel
	}
	for k, override := range agent.GetPolicy() {
		if c, ok := ParseActionClass(k); ok && c == class && override > level && override <= nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
			level = override
		}
	}
	return level
}

// WithMin applies a tool's explicit minimum level (kernel.ToolSpec.MinLevel)
// to a class level: the stricter one wins. Unknown values are ignored.
func WithMin(level, min nexusv1.TrustLevel) nexusv1.TrustLevel {
	if min > level && min <= nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
		return min
	}
	return level
}

// ParseActionClass accepts "spend", "SPEND", "write-external" or
// "ACTION_CLASS_SPEND" (same rules as internal/policy).
func ParseActionClass(s string) (nexusv1.ActionClass, bool) {
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", "_"))
	if !strings.HasPrefix(n, "ACTION_CLASS_") {
		n = "ACTION_CLASS_" + n
	}
	v, ok := nexusv1.ActionClass_value[n]
	if !ok || v == 0 {
		return 0, false
	}
	return nexusv1.ActionClass(v), true
}

// ClassName is the short lower-case name of a class ("compute").
func ClassName(c nexusv1.ActionClass) string {
	return strings.ToLower(strings.TrimPrefix(c.String(), "ACTION_CLASS_"))
}

// Config configures a Gate.
type Config struct {
	TaskID, RunID string
	// Agent supplies the id and the policy map (may be nil: defaults only).
	Agent  *nexusv1.Agent
	NodeID string
	// Emit sends an event on the run's event stream. Required. It must be
	// safe to call from any goroutine that calls Check.
	Emit func(*nexusv1.Event)
	// TTL of approvals (default DefaultTTL).
	TTL time.Duration
	// Levels replaces Defaults (node-local owner policy); nil = Defaults.
	Levels map[nexusv1.ActionClass]nexusv1.TrustLevel
	// QuietNotify suppresses the Notice emitted at NOTIFY level.
	QuietNotify bool
	Log         *slog.Logger
	Now         func() time.Time
}

// Request describes one action to gate.
type Request struct {
	Class nexusv1.ActionClass
	// MinLevel (optional) is the tool's explicit minimum level
	// (kernel.ToolSpec.MinLevel): the stricter of it and the class level
	// applies.
	MinLevel nexusv1.TrustLevel
	Title    string
	Summary  string
	// Payload is the exact action preview; it is hashed (RFC 8785) and the
	// hash is what a device signs. Values must be JSON-compatible.
	Payload map[string]any
}

// Outcome is the result of Check.
type Outcome struct {
	Level nexusv1.TrustLevel
	// Approved: the action may proceed (always true below APPROVE).
	Approved bool
	// Asked: an approval was requested.
	Asked      bool
	ApprovalID string
	// Reason explains a refusal: "denied", "expired", "action hash mismatch"…
	Reason string
}

type pending struct {
	id   string
	hash []byte
	ch   chan *nexusv1.ApprovalResolution
}

// Gate gates the actions of one run. Safe for concurrent use.
type Gate struct {
	cfg Config

	mu      sync.Mutex
	pending map[string]*pending
	closed  bool
	done    chan struct{}
	grace   time.Duration
}

// New creates a gate.
func New(cfg Config) *Gate {
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Gate{cfg: cfg, pending: map[string]*pending{}, done: make(chan struct{}), grace: expiryGrace}
}

// Level returns the effective level for class for this run's agent.
func (g *Gate) Level(class nexusv1.ActionClass) nexusv1.TrustLevel {
	return Level(g.cfg.Agent, class, g.cfg.Levels)
}

// NeedsApproval reports whether class requires a human decision.
func (g *Gate) NeedsApproval(class nexusv1.ActionClass) bool {
	return g.Level(class) >= nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
}

func (g *Gate) emit(ev *nexusv1.Event) {
	ev.TaskId, ev.RunId = g.cfg.TaskID, g.cfg.RunID
	if ev.At == nil {
		ev.At = timestamppb.New(g.cfg.Now())
	}
	g.cfg.Emit(ev)
}

func (g *Gate) state(s nexusv1.TaskState) {
	g.emit(&nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{
		Id: g.cfg.TaskID, CurrentRunId: g.cfg.RunID, State: s,
	}}}})
}

// Resume emits TaskUpdated RUNNING. Callers that continue after a denial
// (e.g. returning an error to a model) use it to leave WAITING_APPROVAL.
func (g *Gate) Resume() { g.state(nexusv1.TaskState_TASK_STATE_RUNNING) }

// Check gates one action; see the package doc.
func (g *Gate) Check(ctx context.Context, req Request) (Outcome, error) {
	level := WithMin(g.Level(req.Class), req.MinLevel)
	out := Outcome{Level: level, Approved: true}
	g.mu.Lock()
	closed := g.closed
	g.mu.Unlock()
	if closed {
		return Outcome{Level: level}, ErrClosed
	}
	if level < nexusv1.TrustLevel_TRUST_LEVEL_APPROVE {
		if level == nexusv1.TrustLevel_TRUST_LEVEL_NOTIFY && !g.cfg.QuietNotify {
			if err := g.notice(req); err != nil {
				return out, err
			}
		}
		return out, nil
	}

	payload, err := structpb.NewStruct(req.Payload)
	if err != nil {
		return Outcome{Level: level}, fmt.Errorf("approvalgate: payload: %w", err)
	}
	hash, err := secapproval.ActionHash(payload)
	if err != nil {
		return Outcome{Level: level}, fmt.Errorf("approvalgate: action hash: %w", err)
	}
	challenge := make([]byte, 32)
	_, _ = rand.Read(challenge)
	now := g.cfg.Now()
	p := &pending{id: "apr_" + ulid.Make().String(), hash: hash, ch: make(chan *nexusv1.ApprovalResolution, 1)}
	out = Outcome{Level: level, Asked: true, ApprovalID: p.id}

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return out, ErrClosed
	}
	g.pending[p.id] = p
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.pending, p.id)
		g.mu.Unlock()
	}()

	title := req.Title
	if title == "" {
		title = "Approve " + ClassName(req.Class)
	}
	expires := now.Add(g.cfg.TTL)
	g.emit(&nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: &nexusv1.Approval{
		Id: p.id, TaskId: g.cfg.TaskID, RunId: g.cfg.RunID, AgentId: g.cfg.Agent.GetId(), NodeId: g.cfg.NodeID,
		ActionClass: req.Class, Level: level, Title: title, Summary: req.Summary,
		Payload: payload, PayloadHash: hash, Challenge: challenge,
		State:     nexusv1.ApprovalState_APPROVAL_STATE_PENDING,
		CreatedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(expires),
	}}})
	g.state(nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL)

	timer := time.NewTimer(expires.Sub(g.cfg.Now()) + g.grace)
	defer timer.Stop()
	select {
	case res := <-p.ch:
		out.Approved, out.Reason = decide(res, p)
		if !out.Approved && out.Reason != "denied" && out.Reason != "expired" {
			g.cfg.Log.Warn("approval resolution rejected by gate", "approval", p.id, "reason", out.Reason)
		}
	case <-timer.C:
		out.Reason = "expired"
	case <-g.done:
		return out, ErrClosed
	case <-ctx.Done():
		return out, ctx.Err()
	}
	if out.Approved {
		g.Resume()
	}
	return out, nil
}

// decide interprets a resolution for p.
func decide(res *nexusv1.ApprovalResolution, p *pending) (bool, string) {
	a := res.GetApproval()
	if h := a.GetPayloadHash(); len(h) > 0 && !bytes.Equal(h, p.hash) {
		return false, "action hash mismatch"
	}
	if d := res.GetDecision(); d != nil {
		if d.ApprovalId != "" && d.ApprovalId != p.id {
			return false, "decision for another approval"
		}
		if !d.Approve {
			return false, "denied"
		}
		if !bytes.Equal(d.ActionSha256, p.hash) {
			return false, "action hash mismatch"
		}
		return true, ""
	}
	switch a.GetState() {
	case nexusv1.ApprovalState_APPROVAL_STATE_APPROVED:
		return true, ""
	case nexusv1.ApprovalState_APPROVAL_STATE_EXPIRED:
		return false, "expired"
	}
	return false, "denied"
}

func (g *Gate) notice(req Request) error {
	var data *structpb.Struct
	if req.Payload != nil {
		var err error
		if data, err = structpb.NewStruct(req.Payload); err != nil {
			return fmt.Errorf("approvalgate: payload: %w", err)
		}
	}
	title := req.Title
	if title == "" {
		title = "Agent action: " + ClassName(req.Class)
	}
	g.emit(&nexusv1.Event{Body: &nexusv1.Event_Notice{Notice: &nexusv1.Notice{
		Id: "ntc_" + ulid.Make().String(), Kind: nexusv1.NoticeKind_NOTICE_KIND_RESULT,
		Title: title, Body: req.Summary, Data: data,
	}}})
	return nil
}

// Resolve delivers a (node-verified) resolution to the waiting Check. A
// resolution without an approval id is delivered when exactly one approval
// is pending.
func (g *Gate) Resolve(ctx context.Context, res *nexusv1.ApprovalResolution) error {
	if res == nil {
		return errors.New("approvalgate: nil resolution")
	}
	id := res.GetApproval().GetId()
	if id == "" {
		id = res.GetDecision().GetApprovalId()
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return ErrClosed
	}
	var p *pending
	if id != "" {
		p = g.pending[id]
	} else if len(g.pending) == 1 {
		for _, v := range g.pending {
			p = v
		}
	}
	g.mu.Unlock()
	if p == nil {
		return fmt.Errorf("%w (%q)", ErrUnknownApproval, id)
	}
	select {
	case p.ch <- res:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fmt.Errorf("approvalgate: approval %s already resolved", p.id)
	}
}

// Pending returns the ids of approvals currently awaited.
func (g *Gate) Pending() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.pending))
	for id := range g.pending {
		out = append(out, id)
	}
	return out
}

// Close abandons every wait (Check returns ErrClosed) and refuses new ones.
// It is idempotent.
func (g *Gate) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		g.closed = true
		close(g.done)
	}
}
