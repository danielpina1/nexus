// Package architect designs new agents (and workflow / team notes) from a
// request, validates them against the live tool registry and the policy,
// tests them in a sandbox and publishes them to the vault once the owner
// approves (docs/ARCHITECT.md).
//
// Module "architect" (hub, default enabled). Requires store. Provides
// SvcArchitect (*Service), mounts ArchitectService and registers the hub
// tools architect.list_tools, architect.list_agents, architect.validate and
// architect.tune. Optional services, resolved per call: llm (drafting with
// a strong model), tasks (the agent-architect task path, sandbox test runs),
// agents / teams / skills / vault (validation and publishing), approvals
// (publishing), events (draft progress and approval decisions).
//
// Drafting has two paths:
//   - "llm": a strong model via kernel.LLM (architect.model, e.g. "best"
//     mapped to a cloud model) answers with the draft JSON;
//   - "agent" (the default with no model configured, e.g. on Claude/ChatGPT
//     subscriptions): the agent-architect agent runs as an ordinary task
//     (runtime auto → Claude Code) and its final answer is the draft JSON.
//
// Config (nexus.yaml):
//
//	architect:
//	  mode: auto            # auto (llm when model is set, else agent) | llm | agent
//	  model: ""             # strong model alias for the llm path
//	  agent: agent-architect
//	  test_model: ""        # model of the in-process sandbox loop (default model, else "smart")
//	  draft_timeout: 20m
//	  test_timeout: 10m
//	  publish_class: write_external   # action class of the publish approval
//	  publish_level: ""     # "" = policy (at least approve) | approve | biometric
package architect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() { kernel.RegisterModule(func() kernel.Module { return &Module{} }) }

// SvcArchitect is the service name of *Service.
const SvcArchitect = "architect"

// ChoiceCreateAgent is the command-bar choice offered when no agent fits a
// request (route params no_fit). Agent ids cannot contain spaces, so a
// forced route whose agent_id is this text is unambiguous.
const ChoiceCreateAgent = "Create a new agent for this"

// DefaultAgent is the agent that drafts on the agent path.
const DefaultAgent = "agent-architect"

// Config is the "architect" config section.
type Config struct {
	Mode         string        `yaml:"mode"`
	Model        string        `yaml:"model"`
	Agent        string        `yaml:"agent"`
	TestModel    string        `yaml:"test_model"`
	DraftTimeout time.Duration `yaml:"draft_timeout"`
	TestTimeout  time.Duration `yaml:"test_timeout"`
	PublishClass string        `yaml:"publish_class"`
	PublishLevel string        `yaml:"publish_level"`
	// Agent tuning (architect.tune / workflows/agent-tuning.md).
	TuneWindow    time.Duration `yaml:"tune_window"`
	TuneMinTasks  int           `yaml:"tune_min_tasks"`
	TuneFailRate  float64       `yaml:"tune_fail_rate"`
	TuneMaxDrafts int           `yaml:"tune_max_drafts"`
}

func (c *Config) defaults() {
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = "auto"
	}
	if c.Agent == "" {
		c.Agent = DefaultAgent
	}
	if c.DraftTimeout <= 0 {
		c.DraftTimeout = 20 * time.Minute
	}
	if c.TestTimeout <= 0 {
		c.TestTimeout = 10 * time.Minute
	}
	if c.PublishClass == "" {
		c.PublishClass = "write_external"
	}
	if c.TuneWindow <= 0 {
		c.TuneWindow = 7 * 24 * time.Hour
	}
	if c.TuneMinTasks <= 0 {
		c.TuneMinTasks = 3
	}
	if c.TuneFailRate <= 0 {
		c.TuneFailRate = 0.3
	}
	if c.TuneMaxDrafts <= 0 {
		c.TuneMaxDrafts = 3
	}
}

var migrations = []string{
	`CREATE TABLE architect_drafts (
		id          TEXT PRIMARY KEY,
		state       INTEGER NOT NULL,
		task_id     TEXT NOT NULL DEFAULT '',
		approval_id TEXT NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL,
		draft       BLOB NOT NULL
	)`,
	`CREATE INDEX architect_drafts_approval ON architect_drafts(approval_id)`,
}

// Short state names.
const (
	stDrafting  = nexusv1.DraftState_DRAFT_STATE_DRAFTING
	stReady     = nexusv1.DraftState_DRAFT_STATE_READY
	stInvalid   = nexusv1.DraftState_DRAFT_STATE_INVALID
	stTesting   = nexusv1.DraftState_DRAFT_STATE_TESTING
	stPending   = nexusv1.DraftState_DRAFT_STATE_PENDING_APPROVAL
	stPublished = nexusv1.DraftState_DRAFT_STATE_PUBLISHED
	stDiscarded = nexusv1.DraftState_DRAFT_STATE_DISCARDED
	stFailed    = nexusv1.DraftState_DRAFT_STATE_FAILED
)

// Service implements ArchitectService and the architect hub tools.
type Service struct {
	h   kernel.Host
	log *slog.Logger
	db  *sql.DB
	cfg Config
	now func() time.Time

	mu      sync.Mutex // serialises draft read-modify-write
	bgCtx   context.Context
	bgStop  context.CancelFunc
	wg      sync.WaitGroup
	waiters sync.Map // draft id → chan struct{} closed when it leaves DRAFTING/TESTING
}

var _ nexusv1connect.ArchitectServiceHandler = (*Service)(nil)

// New migrates the store and returns the service.
func New(h kernel.Host, st kernel.Store, cfg Config) (*Service, error) {
	cfg.defaults()
	if err := st.Migrate("architect", migrations); err != nil {
		return nil, fmt.Errorf("architect: migrate: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{h: h, log: h.Logger(), db: st.DB(), cfg: cfg, now: time.Now, bgCtx: ctx, bgStop: cancel}, nil
}

// Close stops background work (drafting / testing goroutines, watchers).
func (s *Service) Close() {
	s.bgStop()
	s.wg.Wait()
}

func (s *Service) goBG(fn func(ctx context.Context)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn(s.bgCtx)
	}()
}

func lookup[T any](h kernel.Host, name string) (T, bool) {
	v, err := kernel.Resolve[T](h, name)
	return v, err == nil
}

func newID() string { return "drf_" + ulid.Make().String() }

// ---------------------------------------------------------------- storage

func (s *Service) insert(ctx context.Context, d *nexusv1.AgentDraft) error {
	b, err := proto.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO architect_drafts (id, state, task_id, approval_id, created_at, updated_at, draft)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, d.Id, int32(d.State), d.TaskId, d.ApprovalId,
		d.CreatedAt.AsTime().UnixMilli(), d.UpdatedAt.AsTime().UnixMilli(), b)
	return err
}

func (s *Service) save(ctx context.Context, d *nexusv1.AgentDraft) error {
	d.UpdatedAt = timestamppb.New(s.now())
	b, err := proto.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE architect_drafts SET state = ?, task_id = ?, approval_id = ?, updated_at = ?, draft = ? WHERE id = ?`,
		int32(d.State), d.TaskId, d.ApprovalId, d.UpdatedAt.AsTime().UnixMilli(), b, d.Id)
	return err
}

// ErrNotFound is returned for unknown draft ids.
var ErrNotFound = errors.New("architect: draft not found")

func (s *Service) load(ctx context.Context, id string) (*nexusv1.AgentDraft, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT draft FROM architect_drafts WHERE id = ?`, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: %s", ErrNotFound, id))
	}
	if err != nil {
		return nil, err
	}
	d := &nexusv1.AgentDraft{}
	return d, proto.Unmarshal(b, d)
}

// update loads a draft, applies fn and saves it, under the service lock.
func (s *Service) update(ctx context.Context, id string, fn func(d *nexusv1.AgentDraft) error) (*nexusv1.AgentDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := fn(d); err != nil {
		return nil, err
	}
	if err := s.save(ctx, d); err != nil {
		return nil, err
	}
	if d.State != stDrafting && d.State != stTesting {
		if ch, ok := s.waiters.LoadAndDelete(d.Id); ok {
			close(ch.(chan struct{}))
		}
	}
	return proto.Clone(d).(*nexusv1.AgentDraft), nil
}

func (s *Service) list(ctx context.Context, states []nexusv1.DraftState, limit int) ([]*nexusv1.AgentDraft, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT draft FROM architect_drafts`
	var args []any
	if len(states) > 0 {
		ph := make([]string, len(states))
		for i, st := range states {
			ph[i] = "?"
			args = append(args, int32(st))
		}
		q += ` WHERE state IN (` + strings.Join(ph, ",") + `)`
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var blobs [][]byte
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		blobs = append(blobs, b)
	}
	rows.Close()
	out := make([]*nexusv1.AgentDraft, 0, len(blobs))
	for _, b := range blobs {
		d := &nexusv1.AgentDraft{}
		if err := proto.Unmarshal(b, d); err == nil {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// waitFor blocks until the draft leaves DRAFTING / TESTING (or timeout).
func (s *Service) waitFor(ctx context.Context, id string, timeout time.Duration) (*nexusv1.AgentDraft, error) {
	ch := make(chan struct{})
	actual, _ := s.waiters.LoadOrStore(id, ch)
	d, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.State != stDrafting && d.State != stTesting {
		return d, nil
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-actual.(chan struct{}):
	case <-t.C:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.load(ctx, id)
}

// ---------------------------------------------------------------- RPCs

// Draft starts a draft (agent path: an agent-architect task; llm path: a
// model call) and returns it, waiting when req.wait is set.
func (s *Service) Draft(ctx context.Context, req *connect.Request[nexusv1.DraftRequest]) (*connect.Response[nexusv1.DraftResponse], error) {
	d, err := s.StartDraft(ctx, DraftInput{Request: req.Msg.Request, Kind: req.Msg.Kind, Context: req.Msg.Context,
		UpdateAgentID: req.Msg.UpdateAgentId})
	if err != nil {
		return nil, err
	}
	if req.Msg.Wait {
		if d, err = s.waitFor(ctx, d.Id, s.cfg.DraftTimeout); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&nexusv1.DraftResponse{Draft: d}), nil
}

// Refine iterates on a draft with feedback.
func (s *Service) Refine(ctx context.Context, req *connect.Request[nexusv1.RefineRequest]) (*connect.Response[nexusv1.RefineResponse], error) {
	d, err := s.StartRefine(ctx, req.Msg.DraftId, req.Msg.Feedback)
	if err != nil {
		return nil, err
	}
	if req.Msg.Wait {
		if d, err = s.waitFor(ctx, d.Id, s.cfg.DraftTimeout); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&nexusv1.RefineResponse{Draft: d}), nil
}

// TestRun runs the test cases in the sandbox.
func (s *Service) TestRun(ctx context.Context, req *connect.Request[nexusv1.TestRunRequest]) (*connect.Response[nexusv1.TestRunResponse], error) {
	d, err := s.StartTestRun(ctx, req.Msg.DraftId, req.Msg.Cases)
	if err != nil {
		return nil, err
	}
	if req.Msg.Wait {
		if d, err = s.waitFor(ctx, d.Id, s.cfg.TestTimeout); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&nexusv1.TestRunResponse{Draft: d}), nil
}

// Publish requests the owner's approval to write the notes.
func (s *Service) Publish(ctx context.Context, req *connect.Request[nexusv1.PublishRequest]) (*connect.Response[nexusv1.PublishResponse], error) {
	d, a, err := s.RequestPublish(ctx, req.Msg.DraftId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.PublishResponse{Draft: d, Approval: a}), nil
}

// ListDrafts lists drafts, newest first.
func (s *Service) ListDrafts(ctx context.Context, req *connect.Request[nexusv1.ListDraftsRequest]) (*connect.Response[nexusv1.ListDraftsResponse], error) {
	list, err := s.list(ctx, req.Msg.States, int(req.Msg.Limit))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.ListDraftsResponse{Drafts: list}), nil
}

// GetDraft returns one draft.
func (s *Service) GetDraft(ctx context.Context, req *connect.Request[nexusv1.GetDraftRequest]) (*connect.Response[nexusv1.GetDraftResponse], error) {
	d, err := s.load(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.GetDraftResponse{Draft: d}), nil
}

// Discard marks a draft discarded (a pending publish approval is left to
// expire; its decision is ignored).
func (s *Service) Discard(ctx context.Context, req *connect.Request[nexusv1.DiscardRequest]) (*connect.Response[nexusv1.DiscardResponse], error) {
	d, err := s.update(ctx, req.Msg.Id, func(d *nexusv1.AgentDraft) error {
		if d.State == stPublished {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("draft already published"))
		}
		d.State = stDiscarded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nexusv1.DiscardResponse{Draft: d}), nil
}

// ---------------------------------------------------------------- events

// notice publishes an Inbox notice about a draft.
func (s *Service) notice(ctx context.Context, d *nexusv1.AgentDraft, title, body string) {
	bus, ok := lookup[kernel.EventBus](s.h, kernel.SvcEventBus)
	if !ok {
		return
	}
	n := &nexusv1.Notice{Id: "ntc_" + ulid.Make().String(), Kind: nexusv1.NoticeKind_NOTICE_KIND_SUGGESTION, Title: title, Body: body}
	if st, err := structOf(map[string]any{"draft_id": d.Id, "state": stateName(d.State)}); err == nil {
		n.Data = st
	}
	if _, err := bus.Publish(ctx, &nexusv1.Event{Actor: "hub", Body: &nexusv1.Event_Notice{Notice: n}}); err != nil {
		s.log.Warn("architect: publish notice", "err", err)
	}
}

func stateName(st nexusv1.DraftState) string {
	return strings.ToLower(strings.TrimPrefix(st.String(), "DRAFT_STATE_"))
}

// ---------------------------------------------------------------- module

// Module is the architect module.
type Module struct {
	Service *Service
}

// Info implements kernel.Module.
func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "architect",
		Description:    "Agent Architect: drafts, validates, sandbox-tests and (with approval) publishes agents",
		Side:           kernel.SideHub,
		Requires:       []string{"store"},
		DefaultEnabled: true,
	}
}

// Init builds the service, registers tools and mounts the API.
func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("architect: config: %w", err)
	}
	st, err := kernel.Resolve[kernel.Store](h, kernel.SvcStore)
	if err != nil {
		return err
	}
	s, err := New(h, st, cfg)
	if err != nil {
		return err
	}
	for _, t := range s.Tools() {
		if err := h.Tools().Register(t); err != nil {
			return fmt.Errorf("architect: %w", err)
		}
	}
	m.Service = s
	h.Provide(SvcArchitect, s)
	h.Handle(nexusv1connect.NewArchitectServiceHandler(s))
	return nil
}

// Start watches approval decisions and resumes interrupted drafts.
func (m *Module) Start(ctx context.Context) error { return m.Service.Start(ctx) }

// Stop ends background work.
func (m *Module) Stop(context.Context) error {
	m.Service.Close()
	return nil
}
