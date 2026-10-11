// Package approvals implements the approval broker (kernel.ApprovalBroker)
// and nexus.v1.ApprovalService.
//
// Module "approvals" (hub, default enabled). Requires store and events.
// Provides kernel.SvcApprovals. Optional services, resolved per call:
//   - "policy"     (kernel.PolicyEngine) + "agents" to fill an unspecified level;
//   - "security"   implementing Verifier, to verify signed decisions;
//   - "dispatcher" to forward resolutions to the executing node;
//   - "tasks"      implementing ResolutionObserver, to resume/fail the task.
//
// Config (nexus.yaml):
//
//	approvals:
//	  default_ttl: 2h       # expiry when the request has none
//	  sweep_interval: 30s   # how often pending approvals are expired
package approvals

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	secapproval "github.com/danielpina1/nexus/backend/internal/security/approval"
	"log/slog"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() { kernel.RegisterModule(func() kernel.Module { return &Module{} }) }

// Verifier verifies a device-signed decision against the approval it decides.
// The security module provides it (service name "security"); nodes verify
// again on their side (edge verification).
type Verifier interface {
	VerifyApproval(ctx context.Context, d *nexusv1.SignedApproval, a *nexusv1.Approval) error
}

// ResolutionObserver is implemented by the task engine to react to decided
// or expired approvals.
type ResolutionObserver interface {
	OnApprovalResolved(ctx context.Context, a *nexusv1.Approval) error
}

// Config is the "approvals" config section.
type Config struct {
	DefaultTTL    time.Duration `yaml:"default_ttl"`
	SweepInterval time.Duration `yaml:"sweep_interval"`
}

func (c *Config) defaults() {
	if c.DefaultTTL <= 0 {
		c.DefaultTTL = 2 * time.Hour
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = 30 * time.Second
	}
}

const (
	statePending  = nexusv1.ApprovalState_APPROVAL_STATE_PENDING
	stateApproved = nexusv1.ApprovalState_APPROVAL_STATE_APPROVED
	stateDenied   = nexusv1.ApprovalState_APPROVAL_STATE_DENIED
	stateExpired  = nexusv1.ApprovalState_APPROVAL_STATE_EXPIRED
)

var migrations = []string{
	`CREATE TABLE approvals (
		id          TEXT PRIMARY KEY,
		task_id     TEXT NOT NULL DEFAULT '',
		run_id      TEXT NOT NULL DEFAULT '',
		node_id     TEXT NOT NULL DEFAULT '',
		state       INTEGER NOT NULL,
		expires_at  INTEGER NOT NULL,
		created_at  INTEGER NOT NULL,
		approval    BLOB NOT NULL,
		decision    BLOB,
		comment     TEXT NOT NULL DEFAULT '',
		remember    INTEGER NOT NULL DEFAULT 0,
		decided_at  INTEGER
	)`,
	`CREATE INDEX approvals_state ON approvals(state, expires_at)`,
	`CREATE INDEX approvals_task ON approvals(task_id)`,
}

// Broker implements kernel.ApprovalBroker.
type Broker struct {
	h   kernel.Host
	log *slog.Logger
	db  *sql.DB
	bus kernel.EventBus
	cfg Config
	now func() time.Time

	mu sync.Mutex // serialises state changes
}

var _ kernel.ApprovalBroker = (*Broker)(nil)

// NewBroker migrates the store and returns a broker.
func NewBroker(h kernel.Host, st kernel.Store, bus kernel.EventBus, cfg Config) (*Broker, error) {
	if err := st.Migrate("approvals", migrations); err != nil {
		return nil, fmt.Errorf("approvals: migrate: %w", err)
	}
	cfg.defaults()
	return &Broker{h: h, log: h.Logger(), db: st.DB(), bus: bus, cfg: cfg, now: time.Now}, nil
}

// PayloadHash is SHA-256 over the RFC 8785 (JCS) canonical JSON of payload
// (nil payload = "{}"). It delegates to the security package so the hub,
// nodes (owner.EdgeVerifier) and phones all hash identically; see
// docs/WIRE_FORMATS.md.
func PayloadHash(payload *structpb.Struct) ([]byte, error) {
	if payload == nil {
		payload = &structpb.Struct{}
	}
	return secapproval.ActionHash(payload)
}

// CanonicalJSON renders payload as RFC 8785 canonical JSON (see PayloadHash).
func CanonicalJSON(payload *structpb.Struct) ([]byte, error) {
	if payload == nil {
		payload = &structpb.Struct{}
	}
	return secapproval.CanonicalJSON(payload)
}

// ---------------------------------------------------------------- lazy services

func lookup[T any](h kernel.Host, name string) (T, bool) {
	v, err := kernel.Resolve[T](h, name)
	return v, err == nil
}

// ---------------------------------------------------------------- Request

// Request registers a pending approval: assigns id and challenge, computes
// payload_hash, fills level and expiry, persists and publishes it.
func (b *Broker) Request(ctx context.Context, in *nexusv1.Approval) (*nexusv1.Approval, error) {
	if in == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("approval required"))
	}
	a := proto.Clone(in).(*nexusv1.Approval)
	hash, err := PayloadHash(a.Payload)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("payload: %w", err))
	}
	if len(a.PayloadHash) > 0 && !bytes.Equal(a.PayloadHash, hash) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("payload_hash does not match the canonical payload"))
	}
	a.PayloadHash = hash
	// Keep a well-formed id chosen by the node/runtime so its pending
	// approval matches the resolution it receives; otherwise mint one.
	if !strings.HasPrefix(a.Id, "apr_") || len(a.Id) > 64 {
		a.Id = "apr_" + ulid.Make().String()
	} else if _, err := b.Get(ctx, a.Id); err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("approval %s already exists", a.Id))
	}
	a.Challenge = make([]byte, 32)
	if _, err := rand.Read(a.Challenge); err != nil {
		return nil, err
	}
	a.State = statePending
	now := b.now()
	a.CreatedAt = timestamppb.New(now)
	if a.ExpiresAt == nil || !a.ExpiresAt.AsTime().After(now) {
		a.ExpiresAt = timestamppb.New(now.Add(b.cfg.DefaultTTL))
	}
	if a.Level == nexusv1.TrustLevel_TRUST_LEVEL_UNSPECIFIED {
		a.Level = b.policyLevel(ctx, a)
	}
	// An explicit approval request always needs at least a tap.
	if a.Level < nexusv1.TrustLevel_TRUST_LEVEL_APPROVE {
		a.Level = nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
	}
	if a.Title == "" {
		a.Title = "Approve " + strings.ToLower(strings.TrimPrefix(a.ActionClass.String(), "ACTION_CLASS_"))
	}

	data, err := proto.Marshal(a)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	_, err = b.db.ExecContext(ctx, `INSERT INTO approvals (id, task_id, run_id, node_id, state, expires_at, created_at, approval)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Id, a.TaskId, a.RunId, a.NodeId, int32(a.State), a.ExpiresAt.AsTime().UnixMilli(), now.UnixMilli(), data)
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	b.publish(ctx, a)
	return a, nil
}

// policyLevel asks the policy engine (with the agent's overrides when the
// catalog is available). Without a policy engine, approvals require a tap.
func (b *Broker) policyLevel(ctx context.Context, a *nexusv1.Approval) nexusv1.TrustLevel {
	pe, ok := lookup[kernel.PolicyEngine](b.h, kernel.SvcPolicy)
	if !ok {
		return nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
	}
	var agent *nexusv1.Agent
	if cat, ok := lookup[kernel.AgentCatalog](b.h, kernel.SvcAgents); ok && a.AgentId != "" {
		if ag, _, _, err := cat.GetAgent(ctx, a.AgentId); err == nil {
			agent = ag
		}
	}
	l, err := pe.Level(ctx, agent, a.ActionClass)
	if err != nil {
		b.log.Warn("policy level", "err", err)
		return nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
	}
	return l
}

func (b *Broker) publish(ctx context.Context, a *nexusv1.Approval) {
	ev := &nexusv1.Event{TaskId: a.TaskId, RunId: a.RunId, Actor: "hub",
		Body: &nexusv1.Event_Approval{Approval: a}}
	if _, err := b.bus.Publish(ctx, ev); err != nil {
		b.log.Error("publish approval", "approval", a.Id, "err", err)
	}
}

// ---------------------------------------------------------------- queries

// Get returns one approval.
func (b *Broker) Get(ctx context.Context, id string) (*nexusv1.Approval, error) {
	var data []byte
	err := b.db.QueryRowContext(ctx, `SELECT approval FROM approvals WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("approval %q not found", id))
	}
	if err != nil {
		return nil, err
	}
	a := &nexusv1.Approval{}
	return a, proto.Unmarshal(data, a)
}

// List returns approvals in the given states (all when empty), newest first.
func (b *Broker) List(ctx context.Context, states []nexusv1.ApprovalState) ([]*nexusv1.Approval, error) {
	q := `SELECT approval FROM approvals`
	var args []any
	if len(states) > 0 {
		ph := make([]string, len(states))
		for i, s := range states {
			ph[i] = "?"
			args = append(args, int32(s))
		}
		q += ` WHERE state IN (` + strings.Join(ph, ",") + `)`
	}
	rows, err := b.db.QueryContext(ctx, q+` ORDER BY id DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*nexusv1.Approval
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		a := &nexusv1.Approval{}
		if err := proto.Unmarshal(data, a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- Decide

// Decide applies a signed decision.
//
// Checks: the approval is pending and not expired; action_sha256 equals
// payload_hash; the challenge matches (required for BIOMETRIC, checked when
// present otherwise); BIOMETRIC requires a signature; when a Verifier is
// available every signed decision is verified (BIOMETRIC without a Verifier
// is accepted with a warning — dev mode). The decision is then persisted,
// published, forwarded unchanged to the node and reported to the task engine.
func (b *Broker) Decide(ctx context.Context, req *nexusv1.DecideRequest) (*nexusv1.Approval, error) {
	d := req.GetDecision()
	if d.GetApprovalId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("decision.approval_id required"))
	}
	a, err := b.Get(ctx, d.ApprovalId)
	if err != nil {
		return nil, err
	}
	if a.State != statePending {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("approval is %s", stateLabel(a.State)))
	}
	now := b.now()
	if !now.Before(a.ExpiresAt.AsTime()) {
		if _, err := b.expire(ctx, a.Id); err != nil {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("approval expired"))
	}
	if d.ExpiresAt != nil && !now.Before(d.ExpiresAt.AsTime()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("signed decision expired"))
	}
	if !bytes.Equal(d.ActionSha256, a.PayloadHash) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("action_sha256 does not match the approval payload"))
	}
	biometric := a.Level == nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC
	if (biometric || len(d.Challenge) > 0) && !bytes.Equal(d.Challenge, a.Challenge) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("challenge mismatch"))
	}
	if biometric && len(d.Signature) == 0 {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("biometric approval requires a signed decision"))
	}
	if len(d.Signature) > 0 || biometric {
		if v, ok := lookup[Verifier](b.h, kernel.SvcSecurity); ok {
			if err := v.VerifyApproval(ctx, d, a); err != nil {
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("signature verification failed: %w", err))
			}
		} else if biometric {
			b.log.Warn("no approval verifier (security module); accepting biometric decision unverified — dev mode only", "approval", a.Id)
		}
	}

	a.State = stateDenied
	if d.Approve {
		a.State = stateApproved
	}
	decision, err := proto.Marshal(d)
	if err != nil {
		return nil, err
	}
	data, err := proto.Marshal(a)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	res, err := b.db.ExecContext(ctx, `UPDATE approvals SET state = ?, approval = ?, decision = ?, comment = ?, remember = ?, decided_at = ?
		WHERE id = ? AND state = ?`,
		int32(a.State), data, decision, req.Comment, req.Remember, now.UnixMilli(), a.Id, int32(statePending))
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, connect.NewError(connect.CodeAborted, errors.New("approval was decided concurrently"))
	}
	b.resolved(ctx, a, d)
	return a, nil
}

func stateLabel(s nexusv1.ApprovalState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "APPROVAL_STATE_"))
}

// resolved publishes the new state, forwards it to the node and notifies tasks.
func (b *Broker) resolved(ctx context.Context, a *nexusv1.Approval, d *nexusv1.SignedApproval) {
	b.publish(ctx, a)
	if a.NodeId != "" {
		if disp, ok := lookup[kernel.Dispatcher](b.h, kernel.SvcDispatcher); ok {
			res := &nexusv1.ApprovalResolution{RunId: a.RunId, Approval: a, Decision: d}
			if err := disp.Resolve(ctx, a.NodeId, res); err != nil {
				b.log.Warn("forward approval resolution", "approval", a.Id, "node", a.NodeId, "err", err)
			}
		} else {
			b.log.Warn("no dispatcher; approval resolution not forwarded", "approval", a.Id)
		}
	}
	if obs, ok := lookup[ResolutionObserver](b.h, kernel.SvcTasks); ok {
		if err := obs.OnApprovalResolved(ctx, a); err != nil {
			b.log.Warn("task engine approval callback", "approval", a.Id, "err", err)
		}
	}
}

// ---------------------------------------------------------------- expiry

// expire marks one pending approval expired; it reports whether it changed.
func (b *Broker) expire(ctx context.Context, id string) (bool, error) {
	a, err := b.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if a.State != statePending {
		return false, nil
	}
	a.State = stateExpired
	data, err := proto.Marshal(a)
	if err != nil {
		return false, err
	}
	b.mu.Lock()
	res, err := b.db.ExecContext(ctx, `UPDATE approvals SET state = ?, approval = ?, decided_at = ? WHERE id = ? AND state = ?`,
		int32(stateExpired), data, b.now().UnixMilli(), id, int32(statePending))
	b.mu.Unlock()
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	// Nodes treat a resolution without a decision as a denial.
	b.resolved(ctx, a, nil)
	return true, nil
}

// ExpireDue expires every pending approval past its expiry and returns how many.
func (b *Broker) ExpireDue(ctx context.Context) (int, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT id FROM approvals WHERE state = ? AND expires_at <= ?`,
		int32(statePending), b.now().UnixMilli())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		ok, err := b.expire(ctx, id)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// ---------------------------------------------------------------- service

// Handler exposes the broker as nexus.v1.ApprovalService.
type Handler struct{ B *Broker }

var _ nexusv1connect.ApprovalServiceHandler = Handler{}

func (h Handler) ListApprovals(ctx context.Context, req *connect.Request[nexusv1.ListApprovalsRequest]) (*connect.Response[nexusv1.ListApprovalsResponse], error) {
	list, err := h.B.List(ctx, req.Msg.States)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.ListApprovalsResponse{Approvals: list}), nil
}

func (h Handler) Decide(ctx context.Context, req *connect.Request[nexusv1.DecideRequest]) (*connect.Response[nexusv1.DecideResponse], error) {
	a, err := h.B.Decide(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.DecideResponse{Approval: a}), nil
}

// ---------------------------------------------------------------- module

// Module is the approvals module.
type Module struct {
	Broker *Broker
	cancel context.CancelFunc
	done   chan struct{}
}

func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "approvals",
		Description:    "Approval broker: pending decisions, signed approvals, expiry",
		Side:           kernel.SideHub,
		Requires:       []string{"store", "events"},
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("approvals: config: %w", err)
	}
	st, err := kernel.Resolve[kernel.Store](h, kernel.SvcStore)
	if err != nil {
		return err
	}
	bus, err := kernel.Resolve[kernel.EventBus](h, kernel.SvcEventBus)
	if err != nil {
		return err
	}
	b, err := NewBroker(h, st, bus, cfg)
	if err != nil {
		return err
	}
	m.Broker = b
	h.Provide(kernel.SvcApprovals, b)
	h.Handle(nexusv1connect.NewApprovalServiceHandler(Handler{B: b}))
	return nil
}

// Start launches the background expirer.
func (m *Module) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.done = cancel, make(chan struct{})
	go func() {
		defer close(m.done)
		t := time.NewTicker(m.Broker.cfg.SweepInterval)
		defer t.Stop()
		for {
			if n, err := m.Broker.ExpireDue(ctx); err != nil && ctx.Err() == nil {
				m.Broker.log.Error("expire approvals", "err", err)
			} else if n > 0 {
				m.Broker.log.Info("approvals expired", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	select {
	case <-m.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}
