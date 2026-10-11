package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

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
}

// RunManager starts runs on local runtimes and routes inputs to them.
type RunManager struct {
	RunManagerConfig

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu   sync.Mutex
	runs map[string]*activeRun
}

type activeRun struct {
	a         *nexusv1.RunAssignment
	workdir   string
	run       kernel.Run
	mu        sync.Mutex
	cancelled bool
	timer     *time.Timer
}

// NewRunManager creates a manager whose runs live until Close.
func NewRunManager(c RunManagerConfig) *RunManager {
	rm := &RunManager{RunManagerConfig: c}
	rm.ctx, rm.cancel = context.WithCancel(context.Background())
	rm.runs = map[string]*activeRun{}
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
// ignored. Failures are reported as a FAILED TaskUpdated event.
func (m *RunManager) Assign(a *nexusv1.RunAssignment) {
	if a.GetRunId() == "" {
		m.Log.Warn("assignment without run id ignored")
		return
	}
	m.mu.Lock()
	if _, dup := m.runs[a.RunId]; dup {
		m.mu.Unlock()
		return
	}
	ar := &activeRun{a: a}
	m.runs[a.RunId] = ar // reserve
	m.mu.Unlock()

	if err := m.start(ar); err != nil {
		m.Log.Warn("run failed to start", "run", a.RunId, "err", err)
		m.mu.Lock()
		delete(m.runs, a.RunId)
		m.mu.Unlock()
		m.emit(a, m.taskEvent(a, nexusv1.TaskState_TASK_STATE_FAILED, "node: "+err.Error()))
	}
}

func (m *RunManager) start(ar *activeRun) error {
	a := ar.a
	name := a.GetAgent().GetRuntime()
	rt, ok := m.Runtimes.Get(name)
	if !ok {
		return fmt.Errorf("runtime %q not available on this node", name)
	}
	ar.workdir = filepath.Join(m.WorkspacesDir, safeID.ReplaceAllString(a.RunId, "_"))
	if err := os.MkdirAll(ar.workdir, 0o700); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	spec := kernel.RunSpec{Assignment: a, Workdir: ar.workdir, Tools: m.runTools(a), Env: map[string]string{}}
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
	m.mu.Lock()
	delete(m.runs, ar.a.RunId)
	m.mu.Unlock()
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

// Close cancels every run and waits for their final events.
func (m *RunManager) Close(ctx context.Context) {
	m.mu.Lock()
	var all []*activeRun
	for _, ar := range m.runs {
		all = append(all, ar)
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
