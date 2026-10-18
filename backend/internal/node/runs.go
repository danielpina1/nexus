package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/nodelink/wire"
	"github.com/danielpina1/nexus/backend/kernel"
)

// RunManagerConfig wires a RunManager.
type RunManagerConfig struct {
	NodeID        string
	WorkspacesDir string
	Runtimes      *kernel.RuntimeRegistry
	Tools         *kernel.ToolRegistry
	// Hub proxies hub-side tools; may be nil.
	Hub kernel.HubToolClient
	// Verifier returns the approval verifier to use for each resolution.
	Verifier func() ApprovalVerifier
	// Emit forwards an event to the hub (sequenced by the caller).
	Emit func(*nexusv1.Event)
	Log  *slog.Logger
	// Providers returns the run tool providers (browser…) consulted for
	// every run; nil = none.
	Providers func() []kernel.RunToolProvider
	// MaxConcurrent overrides the per-runtime concurrency limit (runtime
	// name → max parallel runs; 0 = unlimited). Runtimes without an entry
	// use kernel.ConcurrencyLimited when they implement it.
	MaxConcurrent map[string]int
}

// RunManager starts runs on local runtimes and routes inputs to them.
type RunManager struct {
	RunManagerConfig

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	runs    map[string]*activeRun
	running map[string]int          // runtime → started runs holding a slot
	queue   map[string][]*activeRun // runtime → runs waiting for a slot (FIFO)
	closing bool
}

type activeRun struct {
	a         *nexusv1.RunAssignment
	runtime   string
	workdir   string
	run       kernel.Run
	mu        sync.Mutex
	cancelled bool
	queued    bool
	slot      bool // holds a concurrency slot of its runtime
	timer     *time.Timer
	cleanup   []func()
}

// NewRunManager creates a manager whose runs live until Close.
func NewRunManager(c RunManagerConfig) *RunManager {
	rm := &RunManager{RunManagerConfig: c}
	rm.ctx, rm.cancel = context.WithCancel(context.Background())
	rm.runs = map[string]*activeRun{}
	rm.running = map[string]int{}
	rm.queue = map[string][]*activeRun{}
	if rm.Log == nil {
		rm.Log = slog.Default()
	}
	if rm.Verifier == nil {
		rm.Verifier = func() ApprovalVerifier { return DevApprovalVerifier{Log: rm.Log} }
	}
	return rm
}

var safeID = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// Active is the number of running runs.
func (m *RunManager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runs)
}

// Workdir returns the workspace of a run.
func (m *RunManager) Workdir(runID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return "", false
	}
	return r.workdir, true
}

func (m *RunManager) taskEvent(a *nexusv1.RunAssignment, state nexusv1.TaskState, summary string) *nexusv1.Event {
	return &nexusv1.Event{
		TaskId: a.TaskId, RunId: a.RunId,
		Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{
			Id: a.TaskId, State: state, CurrentRunId: a.RunId, NodeId: m.NodeID,
			AgentId: a.GetAgent().GetId(), Summary: summary, UpdatedAt: timestamppb.Now(),
		}}},
	}
}

func (m *RunManager) emit(a *nexusv1.RunAssignment, ev *nexusv1.Event) {
	if ev.TaskId == "" {
		ev.TaskId = a.TaskId
	}
	ev.RunId = a.RunId
	ev.Seq = 0
	if ev.Actor == "" {
		if id := a.GetAgent().GetId(); id != "" {
			ev.Actor = "agent:" + id
		} else {
			ev.Actor = "node:" + m.NodeID
		}
	}
	if ev.At == nil {
		ev.At = timestamppb.Now()
	}
	m.Emit(ev)
}

// Assign starts a run. Duplicate assignments (re-sent after reconnect) are
// ignored. Failures are reported as a FAILED TaskUpdated event. A run whose
// runtime is at its concurrency limit is queued (FIFO per runtime) and
// reported as a RunOutput "waiting for a free … slot"; it starts when a run
// of that runtime ends.
func (m *RunManager) Assign(a *nexusv1.RunAssignment) {
	if a.GetRunId() == "" {
		m.Log.Warn("assignment without run id ignored")
		return
	}
	name := a.GetAgent().GetRuntime()
	m.mu.Lock()
	if _, dup := m.runs[a.RunId]; dup {
		m.mu.Unlock()
		return
	}
	ar := &activeRun{a: a, runtime: name}
	m.runs[a.RunId] = ar // reserve
	limit := m.limit(name)
	if limit > 0 && m.running[name] >= limit {
		ar.queued = true
		m.queue[name] = append(m.queue[name], ar)
		pos, busy := len(m.queue[name]), m.running[name]
		m.mu.Unlock()
		m.Log.Info("run queued: runtime at its concurrency limit", "run", a.RunId, "runtime", name, "limit", limit, "position", pos)
		m.emit(a, &nexusv1.Event{Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{
			Text: fmt.Sprintf("Waiting for a free %s slot (%d running, limit %d; position %d in the queue)…\n", RuntimeLabel(name), busy, limit, pos),
		}}})
		return
	}
	if limit > 0 {
		m.running[name]++
		ar.slot = true
	}
	m.mu.Unlock()
	m.launch(ar)
}

// RuntimeLabel is the human name of a runtime ("Claude Code").
func RuntimeLabel(name string) string {
	switch name {
	case "claude-code":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "ollama":
		return "Ollama"
	}
	return name
}

// limit returns the concurrency limit of a runtime (caller may hold m.mu).
func (m *RunManager) limit(name string) int {
	if n, ok := m.MaxConcurrent[name]; ok {
		return max(n, 0)
	}
	if m.Runtimes != nil {
		if rt, ok := m.Runtimes.Get(name); ok {
			if cl, ok := rt.(kernel.ConcurrencyLimited); ok {
				return max(cl.MaxConcurrent(), 0)
			}
		}
	}
	return 0
}

// launch starts a reserved run; on failure it reports FAILED and frees the
// reservation (and slot).
func (m *RunManager) launch(ar *activeRun) {
	if err := m.start(ar); err != nil {
		a := ar.a
		m.Log.Warn("run failed to start", "run", a.RunId, "err", err)
		m.finish(ar)
		m.emit(a, m.taskEvent(a, nexusv1.TaskState_TASK_STATE_FAILED, "node: "+err.Error()))
	}
}

// finish forgets a run, runs its cleanups, releases its slot and starts the
// next queued run of the same runtime.
func (m *RunManager) finish(ar *activeRun) {
	ar.mu.Lock()
	cleanups := ar.cleanup
	ar.cleanup = nil
	ar.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
	m.mu.Lock()
	delete(m.runs, ar.a.RunId)
	var next *activeRun
	if ar.slot {
		ar.slot = false
		m.running[ar.runtime]--
		if q := m.queue[ar.runtime]; len(q) > 0 && !m.closing && m.running[ar.runtime] < max(m.limit(ar.runtime), 1) {
			next, m.queue[ar.runtime] = q[0], q[1:]
			next.queued = false
			next.slot = true
			m.running[ar.runtime]++
		}
	}
	m.mu.Unlock()
	if next != nil {
		m.Log.Info("starting queued run", "run", next.a.RunId, "runtime", next.runtime)
		m.launch(next)
	}
}

// Queued is the number of runs waiting for a concurrency slot.
func (m *RunManager) Queued() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, q := range m.queue {
		n += len(q)
	}
	return n
}

func (m *RunManager) start(ar *activeRun) error {
	a := ar.a
	name := ar.runtime
	rt, ok := m.Runtimes.Get(name)
	if !ok {
		return fmt.Errorf("runtime %q not available on this node", name)
	}
	ar.workdir = filepath.Join(m.WorkspacesDir, safeID.ReplaceAllString(a.RunId, "_"))
	if err := os.MkdirAll(ar.workdir, 0o700); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	spec := kernel.RunSpec{
		Assignment: a, Workdir: ar.workdir, Tools: m.runTools(a),
		Env:  map[string]string{"NEXUS_NODE_ID": m.NodeID},
		Gate: kernel.NewRunGate(),
		Emit: func(ev *nexusv1.Event) { m.emit(a, ev) },
	}
	spec = m.providerTools(ar, spec)
	spec.Tools = codeOnlyTools(a, spec.Tools)
	run, err := rt.Start(m.ctx, spec)
	if err != nil {
		return err
	}
	ar.mu.Lock()
	ar.run = run
	if s := a.GetAgent().GetLimits().GetMaxRuntimeSeconds(); s > 0 {
		ar.timer = time.AfterFunc(time.Duration(s)*time.Second, func() {
			m.Log.Warn("run exceeded max runtime", "run", a.RunId)
			_ = m.cancelRun(ar, "max runtime exceeded")
		})
	}
	ar.mu.Unlock()
	m.emit(a, m.taskEvent(a, nexusv1.TaskState_TASK_STATE_RUNNING, ""))
	m.wg.Add(1)
	go m.pump(ar)
	return nil
}

// providerTools asks every RunToolProvider for run-scoped tools and merges
// them into spec.Tools (see MergeProviderTools). Provider instructions are
// appended to the assignment's instructions (on a copy) when the provider's
// tools were added; cleanups run when the run ends. A failing provider is
// logged and skipped.
func (m *RunManager) providerTools(ar *activeRun, spec kernel.RunSpec) kernel.RunSpec {
	if m.Providers == nil {
		return spec
	}
	allow := ar.a.GetAgent().GetTools()
	for _, p := range m.Providers() {
		set, err := p.RunTools(m.ctx, spec)
		if err != nil {
			m.Log.Warn("run tool provider failed", "provider", p.Name(), "run", ar.a.RunId, "err", err)
			continue
		}
		if set.Cleanup != nil {
			ar.mu.Lock()
			ar.cleanup = append(ar.cleanup, set.Cleanup)
			ar.mu.Unlock()
		}
		spec.Tools = MergeProviderTools(spec.Tools, set.Tools, allow, func(t kernel.Tool) kernel.Tool {
			return boundTool{Tool: t, a: ar.a, nodeID: m.NodeID}
		})
		if added := addedAny(set.Tools, allow); added && strings.TrimSpace(set.Instructions) != "" {
			a := proto.Clone(spec.Assignment).(*nexusv1.RunAssignment)
			a.Instructions = strings.TrimSpace(strings.TrimSpace(a.Instructions) + "\n\n" + strings.TrimSpace(set.Instructions))
			spec.Assignment = a
		}
	}
	return spec
}

// addedAny reports whether one of tools passes the allow-list.
func addedAny(tools []kernel.Tool, allow []string) bool {
	for _, t := range tools {
		if kernel.ToolAllowed(allow, t.Spec().Name) {
			return true
		}
	}
	return false
}

// MergeProviderTools merges provider tools into a run's tools: a provider
// tool with the name of an existing tool replaces it; others are appended
// only when the agent's allow-list permits them (kernel.ToolAllowed). An
// existing tool named like the group of an appended tool ("browser" or
// "browser.*" placeholders proxied from the allow-list) is dropped. bind wraps added tools.
func MergeProviderTools(base, extra []kernel.Tool, allow []string, bind func(kernel.Tool) kernel.Tool) []kernel.Tool {
	if len(extra) == 0 {
		return base
	}
	out := append([]kernel.Tool(nil), base...)
	idx := map[string]int{}
	for i, t := range out {
		idx[t.Spec().Name] = i
	}
	groups := map[string]bool{}
	for _, t := range extra {
		name := t.Spec().Name
		if bind != nil {
			t = bind(t)
		}
		if i, ok := idx[name]; ok {
			out[i] = t
			continue
		}
		if !kernel.ToolAllowed(allow, name) {
			continue
		}
		idx[name] = len(out)
		out = append(out, t)
		if g, _, ok := strings.Cut(name, "."); ok {
			groups[g] = true
		}
	}
	if len(groups) == 0 {
		return out
	}
	kept := out[:0]
	for _, t := range out {
		if n := t.Spec().Name; !groups[n] && !groups[strings.TrimSuffix(n, ".*")] {
			kept = append(kept, t)
		}
	}
	return kept
}

// pump forwards runtime events and guarantees a terminal TaskUpdated.
func (m *RunManager) pump(ar *activeRun) {
	defer m.wg.Done()
	terminal := false
	for ev := range ar.run.Events() {
		if ev == nil {
			continue
		}
		if tu := ev.GetTaskUpdated(); tu != nil && tu.Task != nil {
			if tu.Task.Id == "" {
				tu.Task.Id = ar.a.TaskId
			}
			if tu.Task.CurrentRunId == "" {
				tu.Task.CurrentRunId = ar.a.RunId
			}
			if tu.Task.NodeId == "" {
				tu.Task.NodeId = m.NodeID
			}
			if wire.TerminalState(tu.Task.State) {
				terminal = true
			}
		}
		m.emit(ar.a, ev)
		if terminal {
			break
		}
	}
	// Drain anything after the terminal event so the runtime never blocks.
	go func() {
		for range ar.run.Events() {
		}
	}()
	ar.mu.Lock()
	cancelled := ar.cancelled
	if ar.timer != nil {
		ar.timer.Stop()
	}
	ar.mu.Unlock()
	if !terminal {
		state, summary := nexusv1.TaskState_TASK_STATE_FAILED, "run ended without a final state"
		if cancelled {
			state, summary = nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled"
		}
		m.emit(ar.a, m.taskEvent(ar.a, state, summary))
	}
	m.finish(ar)
}

func (m *RunManager) get(runID string) (*activeRun, kernel.Run, error) {
	m.mu.Lock()
	ar, ok := m.runs[runID]
	m.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("unknown run %q", runID)
	}
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.run == nil {
		return nil, nil, fmt.Errorf("run %q is starting", runID)
	}
	return ar, ar.run, nil
}

// Input delivers a user message / answer to a run.
func (m *RunManager) Input(ctx context.Context, in *nexusv1.RunInput) error {
	_, run, err := m.get(in.GetRunId())
	if err != nil {
		return err
	}
	return run.Send(ctx, in)
}

// ErrApprovalRejected is returned when edge verification fails.
var ErrApprovalRejected = errors.New("approval failed edge verification")

// Resolve verifies an approval decision and delivers it to the run.
func (m *RunManager) Resolve(ctx context.Context, res *nexusv1.ApprovalResolution) error {
	ar, run, err := m.get(res.GetRunId())
	if err != nil {
		return err
	}
	if err := m.Verifier().VerifyApproval(ctx, res); err != nil {
		m.Log.Error("approval rejected by edge verification", "run", res.RunId,
			"approval", res.GetApproval().GetId(), "err", err)
		m.emit(ar.a, &nexusv1.Event{Actor: "node:" + m.NodeID, Body: &nexusv1.Event_Notice{Notice: &nexusv1.Notice{
			Kind:  nexusv1.NoticeKind_NOTICE_KIND_ALERT,
			Title: "Approval verification failed",
			Body:  fmt.Sprintf("Node %s refused approval %s: %v", m.NodeID, res.GetApproval().GetId(), err),
		}}})
		return fmt.Errorf("%w: %v", ErrApprovalRejected, err)
	}
	return run.Resolve(ctx, res)
}

// Cancel cancels a run.
func (m *RunManager) Cancel(ctx context.Context, c *nexusv1.RunCancel) error {
	m.mu.Lock()
	ar, ok := m.runs[c.GetRunId()]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown run %q", c.GetRunId())
	}
	return m.cancelRun(ar, c.GetReason())
}

func (m *RunManager) cancelRun(ar *activeRun, reason string) error {
	if m.dequeue(ar) {
		m.Log.Info("cancelling queued run", "run", ar.a.RunId, "reason", reason)
		m.mu.Lock()
		delete(m.runs, ar.a.RunId)
		m.mu.Unlock()
		m.emit(ar.a, m.taskEvent(ar.a, nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled"))
		return nil
	}
	ar.mu.Lock()
	ar.cancelled = true
	run := ar.run
	ar.mu.Unlock()
	if run == nil {
		return nil
	}
	m.Log.Info("cancelling run", "run", ar.a.RunId, "reason", reason)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return run.Cancel(ctx)
}

// dequeue removes a queued run; it reports whether it was queued.
func (m *RunManager) dequeue(ar *activeRun) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ar.queued {
		return false
	}
	q := m.queue[ar.runtime]
	for i, x := range q {
		if x == ar {
			m.queue[ar.runtime] = append(q[:i:i], q[i+1:]...)
			break
		}
	}
	ar.queued = false
	return true
}

// Close cancels every run and waits for their final events.
func (m *RunManager) Close(ctx context.Context) {
	m.mu.Lock()
	m.closing = true
	var all []*activeRun
	for _, ar := range m.runs {
		if ar.queued {
			all = append([]*activeRun{ar}, all...) // queued first: they must never start
		} else {
			all = append(all, ar)
		}
	}
	m.mu.Unlock()
	for _, ar := range all {
		_ = m.cancelRun(ar, "node shutting down")
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	m.cancel()
}

// runTools returns node-local tools plus proxied hub tools allowed for the
// agent, bound to this run (runtimes cannot spoof run/task ids).
func (m *RunManager) runTools(a *nexusv1.RunAssignment) []kernel.Tool {
	allow := a.GetAgent().GetTools()
	var out []kernel.Tool
	local := map[string]bool{}
	if m.Tools != nil {
		for _, t := range m.Tools.Select(allow) {
			local[t.Spec().Name] = true
			out = append(out, t)
		}
	}
	if m.Hub != nil {
		known := map[string]kernel.ToolSpec{}
		specs := m.Hub.HubTools()
		for _, s := range specs {
			known[s.Name] = s
		}
		for _, name := range allow {
			if local[name] {
				continue
			}
			spec, ok := known[name]
			if !ok {
				if len(specs) > 0 {
					continue // hub list known and tool absent
				}
				spec = kernel.ToolSpec{Name: name, Description: "hub tool"}
			}
			out = append(out, hubTool{spec: spec, hub: m.Hub})
		}
	}
	for i, t := range out {
		out[i] = boundTool{Tool: t, a: a, nodeID: m.NodeID}
	}
	return out
}

type hubTool struct {
	spec kernel.ToolSpec
	hub  kernel.HubToolClient
}

func (t hubTool) Spec() kernel.ToolSpec { return t.spec }
func (t hubTool) Invoke(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	out, err := t.hub.CallHubTool(ctx, c.RunID, t.spec.Name, c.Input)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	return kernel.ToolResult{Output: out}, nil
}

type boundTool struct {
	kernel.Tool
	a      *nexusv1.RunAssignment
	nodeID string
}

func (t boundTool) Invoke(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	c.RunID, c.TaskID, c.AgentID, c.NodeID = t.a.RunId, t.a.TaskId, t.a.GetAgent().GetId(), t.nodeID
	return t.Tool.Invoke(ctx, c)
}
