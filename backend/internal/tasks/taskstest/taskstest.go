// Package taskstest provides small in-memory fakes of kernel services used by
// the orchestration modules' tests (tasks, approvals, policy, router, command).
package taskstest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/sqlitex"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

// Env bundles a fake host with the fakes it provides.
type Env struct {
	Host  *kerneltest.Host
	Store *Store
	Bus   *Bus
}

// New returns a hub host providing only "store" and "events".
func New(t testing.TB) *Env {
	t.Helper()
	h := kerneltest.New(t)
	st := NewStore(t)
	bus := NewBus()
	h.Provide(kernel.SvcStore, st)
	h.Provide(kernel.SvcEventBus, bus)
	return &Env{Host: h, Store: st, Bus: bus}
}

// ---------------------------------------------------------------- Store

// Store is a kernel.Store over an in-memory SQLite database.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// NewStore opens a private in-memory database closed at test cleanup.
func NewStore(t testing.TB) *Store {
	t.Helper()
	db, err := sqlitex.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE schema_migrations (module TEXT NOT NULL, idx INTEGER NOT NULL, PRIMARY KEY (module, idx))`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	return &Store{db: db}
}

func (s *Store) DB() *sql.DB { return s.db }

// Migrate applies each statement once, keyed by (module, index).
func (s *Store) Migrate(module string, statements []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, stmt := range statements {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE module = ? AND idx = ?`, module, i).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate %s[%d]: %w", module, i, err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_migrations (module, idx) VALUES (?, ?)`, module, i); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- EventBus

// Bus is an in-memory kernel.EventBus that records everything published.
type Bus struct {
	mu     sync.Mutex
	seq    int64
	events []*nexusv1.Event
	subs   map[int]*sub
	nextID int
}

type sub struct {
	ch chan *nexusv1.Event
	f  kernel.EventFilter
}

func NewBus() *Bus { return &Bus{subs: map[int]*sub{}} }

func (b *Bus) Publish(_ context.Context, ev *nexusv1.Event) (*nexusv1.Event, error) {
	if ev == nil || ev.Body == nil {
		return nil, errors.New("taskstest: event without body")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e := proto.Clone(ev).(*nexusv1.Event)
	e.Seq = b.seq
	e.At = timestamppb.Now()
	b.events = append(b.events, e)
	for _, s := range b.subs {
		if match(e, s.f) {
			select {
			case s.ch <- e:
			default:
			}
		}
	}
	return e, nil
}

func match(e *nexusv1.Event, f kernel.EventFilter) bool { return f.Match(e) }

func (b *Bus) Subscribe(ctx context.Context, since int64, f kernel.EventFilter) (<-chan *nexusv1.Event, error) {
	b.mu.Lock()
	ch := make(chan *nexusv1.Event, 1024)
	for _, e := range b.events {
		if e.Seq > since && match(e, f) {
			ch <- e
		}
	}
	id := b.nextID
	b.nextID++
	b.subs[id] = &sub{ch: ch, f: f}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, id)
		close(ch)
		b.mu.Unlock()
	}()
	return ch, nil
}

func (b *Bus) LastSeq(context.Context) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq, nil
}

// Events returns a copy of all published events.
func (b *Bus) Events() []*nexusv1.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*nexusv1.Event(nil), b.events...)
}

// TaskStates returns the sequence of states published in TaskUpdated events for a task.
func (b *Bus) TaskStates(taskID string) []nexusv1.TaskState {
	var out []nexusv1.TaskState
	for _, e := range b.Events() {
		if tu := e.GetTaskUpdated(); tu != nil && e.TaskId == taskID {
			out = append(out, tu.GetTask().GetState())
		}
	}
	return out
}

// Filter returns events for which keep returns true.
func (b *Bus) Filter(keep func(*nexusv1.Event) bool) []*nexusv1.Event {
	var out []*nexusv1.Event
	for _, e := range b.Events() {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

// ---------------------------------------------------------------- Dispatcher

// Assignment records one Dispatcher.Assign call.
type Assignment struct {
	Selector   kernel.NodeSelector
	Assignment *nexusv1.RunAssignment
}

// Dispatcher is a recording kernel.Dispatcher.
type Dispatcher struct {
	mu          sync.Mutex
	NodeID      string // returned by Assign (default "node-1")
	AssignErr   error
	SendErr     error
	Assigned    []Assignment
	Inputs      []*nexusv1.RunInput
	Resolutions []*nexusv1.ApprovalResolution
	Cancels     []*nexusv1.RunCancel
}

func (d *Dispatcher) Assign(_ context.Context, sel kernel.NodeSelector, a *nexusv1.RunAssignment) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.AssignErr != nil {
		return "", d.AssignErr
	}
	d.Assigned = append(d.Assigned, Assignment{Selector: sel, Assignment: a})
	if d.NodeID == "" {
		return "node-1", nil
	}
	return d.NodeID, nil
}

func (d *Dispatcher) SendInput(_ context.Context, _ string, in *nexusv1.RunInput) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.SendErr != nil {
		return d.SendErr
	}
	d.Inputs = append(d.Inputs, in)
	return nil
}

func (d *Dispatcher) Resolve(_ context.Context, _ string, r *nexusv1.ApprovalResolution) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Resolutions = append(d.Resolutions, r)
	return nil
}

func (d *Dispatcher) Cancel(_ context.Context, _ string, c *nexusv1.RunCancel) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Cancels = append(d.Cancels, c)
	return nil
}

func (d *Dispatcher) Nodes(context.Context) ([]*nexusv1.Node, error) { return nil, nil }

// LastAssignment returns the most recent assignment (or nil).
func (d *Dispatcher) LastAssignment() *nexusv1.RunAssignment {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.Assigned) == 0 {
		return nil
	}
	return d.Assigned[len(d.Assigned)-1].Assignment
}

// ---------------------------------------------------------------- AgentCatalog

// Catalog is an in-memory kernel.AgentCatalog.
type Catalog struct {
	mu     sync.Mutex
	order  []string
	agents map[string]*nexusv1.Agent
	instr  map[string]string
}

// NewCatalog returns a catalog holding the given agents (instructions "be helpful").
func NewCatalog(agents ...*nexusv1.Agent) *Catalog {
	c := &Catalog{agents: map[string]*nexusv1.Agent{}, instr: map[string]string{}}
	for _, a := range agents {
		c.Add(a, "Instructions for "+a.Id)
	}
	return c
}

func (c *Catalog) Add(a *nexusv1.Agent, instructions string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.agents[a.Id]; !ok {
		c.order = append(c.order, a.Id)
	}
	c.agents[a.Id] = a
	c.instr[a.Id] = instructions
}

func (c *Catalog) ListAgents(_ context.Context, includeDisabled bool) ([]*nexusv1.Agent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*nexusv1.Agent
	for _, id := range c.order {
		if a := c.agents[id]; a.Enabled || includeDisabled {
			out = append(out, proto.Clone(a).(*nexusv1.Agent))
		}
	}
	return out, nil
}

func (c *Catalog) GetAgent(_ context.Context, id string) (*nexusv1.Agent, string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.agents[id]
	if !ok {
		return nil, "", "", fmt.Errorf("agent %q not found", id)
	}
	return proto.Clone(a).(*nexusv1.Agent), c.instr[id], "", nil
}

func (c *Catalog) UpsertAgent(context.Context, string, string) (*nexusv1.Agent, error) {
	return nil, errors.New("taskstest: UpsertAgent not supported")
}

// Agent is a convenience constructor for an enabled agent.
func Agent(id, name, description string, tier nexusv1.Tier) *nexusv1.Agent {
	return &nexusv1.Agent{Id: id, Name: name, Description: description, Tier: tier, Runtime: "echo", Enabled: true}
}

// ---------------------------------------------------------------- Router

// Router is a kernel.Router returning a fixed route.
type Router struct {
	mu     sync.Mutex
	Result *nexusv1.Route
	Err    error
	Inputs []string
}

func (r *Router) Route(_ context.Context, input string, _ []*nexusv1.Attachment, _ *nexusv1.CommandContext) (*nexusv1.Route, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Inputs = append(r.Inputs, input)
	if r.Err != nil {
		return nil, r.Err
	}
	return proto.Clone(r.Result).(*nexusv1.Route), nil
}

// ---------------------------------------------------------------- ApprovalBroker

// Broker is a recording kernel.ApprovalBroker.
type Broker struct {
	mu        sync.Mutex
	n         int
	Requested []*nexusv1.Approval
}

func (b *Broker) Request(_ context.Context, a *nexusv1.Approval) (*nexusv1.Approval, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	c := proto.Clone(a).(*nexusv1.Approval)
	c.Id = fmt.Sprintf("apr_%d", b.n)
	c.State = nexusv1.ApprovalState_APPROVAL_STATE_PENDING
	b.Requested = append(b.Requested, c)
	return c, nil
}

func (b *Broker) List(context.Context, []nexusv1.ApprovalState) ([]*nexusv1.Approval, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*nexusv1.Approval(nil), b.Requested...), nil
}

func (b *Broker) Decide(context.Context, *nexusv1.DecideRequest) (*nexusv1.Approval, error) {
	return nil, errors.New("taskstest: Decide not supported")
}

// ---------------------------------------------------------------- LLM

// LLM is a kernel.LLM whose replies come from Fn.
type LLM struct {
	mu    sync.Mutex
	Fn    func(req kernel.ChatRequest) (*kernel.ChatResponse, error)
	Calls []kernel.ChatRequest
}

// Reply builds a ChatResponse with the given assistant content.
func Reply(content string) *kernel.ChatResponse {
	return &kernel.ChatResponse{Message: kernel.ChatMessage{Role: "assistant", Content: content}}
}

func (l *LLM) Chat(_ context.Context, req kernel.ChatRequest) (*kernel.ChatResponse, error) {
	l.mu.Lock()
	l.Calls = append(l.Calls, req)
	fn := l.Fn
	l.mu.Unlock()
	if fn == nil {
		return nil, errors.New("taskstest: no LLM reply configured")
	}
	return fn(req)
}

func (l *LLM) ChatStream(ctx context.Context, req kernel.ChatRequest, onDelta func(string)) (*kernel.ChatResponse, error) {
	resp, err := l.Chat(ctx, req)
	if err == nil && onDelta != nil {
		onDelta(resp.Message.Content)
	}
	return resp, err
}

func (l *LLM) Embed(context.Context, string, []string) ([][]float32, error) {
	return nil, errors.New("taskstest: embeddings not supported")
}

// CallCount returns the number of Chat calls made.
func (l *LLM) CallCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.Calls)
}

// Eventually polls cond until it is true or the timeout elapses.
func Eventually(t testing.TB, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

// ---------------------------------------------------------------- TeamCatalog

// Teams is an in-memory kernel.TeamCatalog.
type Teams struct {
	mu      sync.Mutex
	teams   map[string]*nexusv1.Team
	mission map[string]string
	// Upserted records UpsertTeam markdown.
	Upserted []string
}

// NewTeams returns a catalog holding the given teams (mission "Mission of <id>").
func NewTeams(teams ...*nexusv1.Team) *Teams {
	c := &Teams{teams: map[string]*nexusv1.Team{}, mission: map[string]string{}}
	for _, t := range teams {
		c.Add(t, "Mission of "+t.Id)
	}
	return c
}

func (c *Teams) Add(t *nexusv1.Team, mission string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.teams[t.Id] = t
	c.mission[t.Id] = mission
}

func (c *Teams) ListTeams(_ context.Context, includeDisabled bool) ([]*nexusv1.Team, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*nexusv1.Team
	for _, t := range c.teams {
		if t.Enabled || includeDisabled {
			out = append(out, proto.Clone(t).(*nexusv1.Team))
		}
	}
	return out, nil
}

func (c *Teams) GetTeam(_ context.Context, id string) (*nexusv1.Team, string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.teams[id]
	if !ok {
		return nil, "", "", fmt.Errorf("team %q not found", id)
	}
	return proto.Clone(t).(*nexusv1.Team), c.mission[id], "---\nid: " + id + "\n---\n" + c.mission[id], nil
}

func (c *Teams) UpsertTeam(_ context.Context, markdown, _ string, validateOnly bool) (*nexusv1.Team, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Upserted = append(c.Upserted, markdown)
	return &nexusv1.Team{Id: "upserted", Enabled: !validateOnly}, nil
}

// Team is a convenience constructor for an enabled team.
func Team(id, orchestrator string, strategy nexusv1.TeamStrategy, members ...*nexusv1.TeamMember) *nexusv1.Team {
	return &nexusv1.Team{Id: id, Name: id, Description: "team " + id, OrchestratorAgentId: orchestrator,
		Strategy: strategy, Members: members, Blackboard: true, Enabled: true, DebateRounds: 2}
}

// Member is a convenience constructor for a team member.
func Member(agent, role string, count int32) *nexusv1.TeamMember {
	return &nexusv1.TeamMember{AgentId: agent, Role: role, Count: count}
}

// ---------------------------------------------------------------- SkillCatalog

// Skills is an in-memory kernel.SkillCatalog.
type Skills struct {
	mu     sync.Mutex
	skills map[string]*nexusv1.Skill
}

// NewSkills returns a catalog holding the given skills.
func NewSkills(skills ...*nexusv1.Skill) *Skills {
	c := &Skills{skills: map[string]*nexusv1.Skill{}}
	for _, s := range skills {
		c.skills[s.Id] = s
	}
	return c
}

func (c *Skills) ListSkills(_ context.Context, includeDisabled bool) ([]*nexusv1.Skill, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*nexusv1.Skill
	for _, s := range c.skills {
		if s.Enabled || includeDisabled {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c *Skills) GetSkill(_ context.Context, id string) (*nexusv1.Skill, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.skills[id]
	if !ok {
		return nil, "", fmt.Errorf("skill %q not found", id)
	}
	return s, "", nil
}

func (c *Skills) UpsertSkill(context.Context, string, string) (*nexusv1.Skill, error) {
	return nil, errors.New("taskstest: UpsertSkill not supported")
}

// AssignmentsFor returns the assignments made for a task, oldest first.
func (d *Dispatcher) AssignmentsFor(taskID string) []*nexusv1.RunAssignment {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*nexusv1.RunAssignment
	for _, a := range d.Assigned {
		if a.Assignment.GetTaskId() == taskID {
			out = append(out, a.Assignment)
		}
	}
	return out
}

// InputsSnapshot returns a copy of the inputs sent so far.
func (d *Dispatcher) InputsSnapshot() []*nexusv1.RunInput {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*nexusv1.RunInput(nil), d.Inputs...)
}

// CancelsSnapshot returns a copy of the cancels sent so far.
func (d *Dispatcher) CancelsSnapshot() []*nexusv1.RunCancel {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*nexusv1.RunCancel(nil), d.Cancels...)
}

// ResolutionsSnapshot returns a copy of the approval resolutions sent so far.
func (d *Dispatcher) ResolutionsSnapshot() []*nexusv1.ApprovalResolution {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*nexusv1.ApprovalResolution(nil), d.Resolutions...)
}
