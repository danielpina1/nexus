// Package node is the nexus-node daemon: it connects to the hub over
// NodeLink (see package nodelink/wire for the protocol), reports runtimes
// and telemetry, runs assignments on local runtimes, exposes node-local
// tools (fs.list, fs.read, shell.exec) and proxies hub tools to runtimes.
//
// The daemon is packaged as the node module "node" (DefaultEnabled), so
// nexus-node only needs to load modules with internal/host.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/nodelink/wire"
	"github.com/danielpina1/nexus/backend/kernel"
)

// OutboxLimit bounds buffered, unacknowledged run events.
const OutboxLimit = 10000

// Daemon is one node's link to the hub plus its run manager.
type Daemon struct {
	cfg  Config
	h    kernel.Host
	log  *slog.Logger
	http *http.Client
	Runs *RunManager

	sampler sampler

	// link state, guarded by mu
	mu         sync.Mutex
	stream     *connect.BidiStreamForClient[nexusv1.ConnectRequest, nexusv1.ConnectResponse]
	nodeSeq    int64
	outbox     []*nexusv1.RunEvent
	lastHubSeq int64 // last hub message processed
	recvSeq    int64
	connected  chan struct{} // closed while connected

	pmu      sync.Mutex
	pending  map[string]chan *nexusv1.ToolResponse
	hubTools []kernel.ToolSpec
}

// Options customise a Daemon (tests).
type Options struct {
	// HTTPClient overrides the transport (must speak HTTP/2).
	HTTPClient *http.Client
}

// New creates a daemon on host h. Runtimes must be registered in
// h.Runtimes() before Run; call RegisterTools to add node-local tools.
func New(h kernel.Host, cfg Config, o Options) (*Daemon, error) {
	cfg.Defaults(h.DataDir())
	if err := cfg.ResolveFiles(h.DataDir()); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.WorkspacesDir, 0o700); err != nil {
		return nil, err
	}
	d := &Daemon{
		cfg: cfg, h: h, log: h.Logger(),
		nodeSeq:   time.Now().UnixMicro(),
		connected: make(chan struct{}),
		pending:   map[string]chan *nexusv1.ToolResponse{},
	}
	d.http = o.HTTPClient
	if d.http == nil {
		c, err := httpClient(cfg)
		if err != nil {
			return nil, err
		}
		d.http = c
	}
	d.Runs = NewRunManager(RunManagerConfig{
		NodeID: cfg.NodeID, WorkspacesDir: cfg.WorkspacesDir,
		Runtimes: h.Runtimes(), Tools: h.Tools(), Hub: d,
		Verifier: d.verifier, Emit: d.emit, Log: d.log,
		Providers: d.runToolProviders, MaxConcurrent: cfg.MaxConcurrent,
	})
	return d, nil
}

// Config returns the effective configuration.
func (d *Daemon) Config() Config { return d.cfg }

// RegisterTools adds fs.list, fs.read and shell.exec to the host registry.
func (d *Daemon) RegisterTools() error {
	for _, t := range LocalTools(d.rootFor) {
		if err := d.h.Tools().Register(t); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) rootFor(c kernel.ToolCall) (string, error) {
	if c.RunID == "" {
		return d.cfg.WorkspacesDir, nil
	}
	if w, ok := d.Runs.Workdir(c.RunID); ok {
		return w, nil
	}
	return "", fmt.Errorf("unknown run %q", c.RunID)
}

// runToolProviders returns the providers registered on the host (browser…).
func (d *Daemon) runToolProviders() []kernel.RunToolProvider {
	v, ok := d.h.Lookup(kernel.SvcRunToolProviders)
	if !ok {
		return nil
	}
	if r, ok := v.(*kernel.RunToolProviderRegistry); ok {
		return r.Providers()
	}
	return nil
}

func (d *Daemon) verifier() ApprovalVerifier {
	if v, ok := d.h.Lookup(SvcApprovalVerifier); ok {
		if av, ok := v.(ApprovalVerifier); ok {
			return av
		}
	}
	if d.cfg.StrictApprovals {
		return rejectVerifier{}
	}
	return DevApprovalVerifier{Log: d.log}
}

func httpClient(cfg Config) (*http.Client, error) {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	if strings.HasPrefix(cfg.HubURL, "https://") {
		tc, err := ClientTLSConfig(cfg)
		if err != nil {
			return nil, err
		}
		tr.TLSClientConfig = tc
	} else {
		// Plain-text hub (dev / private network): HTTP/2 without TLS (h2c).
		p := new(http.Protocols)
		p.SetUnencryptedHTTP2(true)
		tr.Protocols = p
	}
	return &http.Client{Transport: tr}, nil
}

// Run connects to the hub and reconnects with exponential backoff + jitter
// until ctx is done. Runs are cancelled when Run returns.
func (d *Daemon) Run(ctx context.Context) error {
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		d.Runs.Close(cctx)
	}()
	attempt := 0
	for {
		welcomed, err := d.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if welcomed {
			attempt = 0
		}
		delay := Backoff(attempt, d.cfg.BackoffMin, d.cfg.BackoffMax, rand.Float64)
		attempt++
		d.log.Warn("hub link down; reconnecting", "err", err, "in", delay.Round(time.Millisecond))
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

// Backoff returns the delay before reconnect attempt n (0-based): an
// exponential base*2^n capped at max, with "equal jitter" in [d/2, d].
func Backoff(n int, base, max time.Duration, rnd func() float64) time.Duration {
	d := base
	for i := 0; i < n && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	half := d / 2
	return half + time.Duration(rnd()*float64(d-half))
}

func (d *Daemon) runtimeInfos(ctx context.Context) []*nexusv1.RuntimeInfo {
	var out []*nexusv1.RuntimeInfo
	for _, name := range d.h.Runtimes().Names() {
		rt, _ := d.h.Runtimes().Get(name)
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		v, ok := rt.Available(pctx)
		cancel()
		out = append(out, &nexusv1.RuntimeInfo{Name: name, Version: v, Available: ok})
	}
	return out
}

// session runs one connection. welcomed reports whether the hub accepted it.
func (d *Daemon) session(ctx context.Context) (welcomed bool, err error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client := nexusv1connect.NewNodeLinkServiceClient(d.http, strings.TrimRight(d.cfg.HubURL, "/"))
	stream := client.Connect(sctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+d.cfg.Token)
	stream.RequestHeader().Set(wire.HeaderNodeID, d.cfg.NodeID)
	// The HTTP/2 transport only notices cancellation once the request body
	// is closed, so close it explicitly when the session ends.
	var closeOnce sync.Once
	closeReq := func() { closeOnce.Do(func() { _ = stream.CloseRequest() }) }
	stopClose := context.AfterFunc(sctx, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		closeReq()
	})
	defer func() {
		stopClose()
		d.mu.Lock()
		closeReq() // waits for a concurrent AfterFunc call (sync.Once)
		d.mu.Unlock()
		_ = stream.CloseResponse()
	}()

	d.mu.Lock()
	resume := d.lastHubSeq
	d.mu.Unlock()
	hello := &nexusv1.NodeHello{
		NodeId: d.cfg.NodeID, Version: d.cfg.Version, Tags: d.cfg.helloTags(),
		Runtimes: d.runtimeInfos(sctx), ResumeSeq: resume,
	}
	if err := stream.Send(&nexusv1.ConnectRequest{Msg: &nexusv1.ConnectRequest_Hello{Hello: hello}}); err != nil {
		return false, fmt.Errorf("send hello: %w", err)
	}
	first, err := stream.Receive()
	if err != nil {
		return false, fmt.Errorf("await welcome: %w", err)
	}
	welcome := first.GetWelcome()
	if welcome == nil {
		return false, errors.New("hub did not send welcome")
	}

	// Attach: drop events the hub already has, re-send the rest.
	d.mu.Lock()
	keep := d.outbox[:0]
	for _, re := range d.outbox {
		if re.NodeSeq > welcome.ResumeSeq {
			keep = append(keep, re)
		}
	}
	d.outbox = keep
	d.recvSeq = resume
	for _, re := range d.outbox {
		if err := stream.Send(&nexusv1.ConnectRequest{Msg: &nexusv1.ConnectRequest_RunEvent{RunEvent: re}}); err != nil {
			d.mu.Unlock()
			return true, err
		}
	}
	d.stream = stream
	close(d.connected)
	d.mu.Unlock()
	d.log.Info("connected to hub", "hub", d.cfg.HubURL, "node", d.cfg.NodeID, "resent", len(keep))

	defer func() {
		d.mu.Lock()
		d.stream = nil
		d.connected = make(chan struct{})
		d.mu.Unlock()
		d.failPending(errors.New("hub link closed"))
	}()

	go d.statusLoop(sctx)
	go d.fetchHubTools(sctx)

	for {
		msg, err := stream.Receive()
		if err != nil {
			return true, err
		}
		d.handle(sctx, msg)
	}
}

// send writes a message on the current stream (caller holds d.mu).
func (d *Daemon) sendLocked(m *nexusv1.ConnectRequest) error {
	if d.stream == nil {
		return errors.New("not connected")
	}
	return d.stream.Send(m)
}

func (d *Daemon) send(m *nexusv1.ConnectRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sendLocked(m)
}

// emit sequences a run event, buffers it until acknowledged and sends it.
func (d *Daemon) emit(ev *nexusv1.Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nodeSeq++
	re := &nexusv1.RunEvent{NodeSeq: d.nodeSeq, Event: ev}
	if len(d.outbox) >= OutboxLimit {
		d.log.Error("run event outbox full; dropping oldest", "seq", d.outbox[0].NodeSeq)
		d.outbox = d.outbox[1:]
	}
	d.outbox = append(d.outbox, re)
	if d.stream != nil {
		if err := d.stream.Send(&nexusv1.ConnectRequest{Msg: &nexusv1.ConnectRequest_RunEvent{RunEvent: re}}); err != nil {
			d.log.Debug("run event buffered", "seq", re.NodeSeq, "err", err)
		}
	}
}

func (d *Daemon) handle(ctx context.Context, msg *nexusv1.ConnectResponse) {
	d.mu.Lock()
	d.recvSeq++
	seq := d.recvSeq
	d.mu.Unlock()

	switch m := msg.Msg.(type) {
	case *nexusv1.ConnectResponse_Assign:
		d.Runs.Assign(m.Assign)
	case *nexusv1.ConnectResponse_Input:
		if wire.IsControlInput(m.Input) {
			go d.controlCall(ctx, m.Input)
		} else if err := d.withTimeout(func(c context.Context) error { return d.Runs.Input(c, m.Input) }); err != nil {
			d.log.Warn("run input not delivered", "run", m.Input.RunId, "err", err)
		}
	case *nexusv1.ConnectResponse_Resolution:
		if err := d.withTimeout(func(c context.Context) error { return d.Runs.Resolve(c, m.Resolution) }); err != nil {
			d.log.Warn("approval resolution not delivered", "run", m.Resolution.RunId, "err", err)
		}
	case *nexusv1.ConnectResponse_Cancel:
		if err := d.Runs.Cancel(ctx, m.Cancel); err != nil {
			d.log.Info("cancel", "run", m.Cancel.RunId, "err", err)
		}
	case *nexusv1.ConnectResponse_ToolResponse:
		d.pmu.Lock()
		ch := d.pending[m.ToolResponse.RequestId]
		delete(d.pending, m.ToolResponse.RequestId)
		d.pmu.Unlock()
		if ch != nil {
			ch <- m.ToolResponse
		}
	case *nexusv1.ConnectResponse_Ack:
		d.mu.Lock()
		keep := d.outbox[:0]
		for _, re := range d.outbox {
			if re.NodeSeq > m.Ack.Seq {
				keep = append(keep, re)
			}
		}
		d.outbox = keep
		d.mu.Unlock()
	default:
		d.log.Debug("hub message ignored", "type", fmt.Sprintf("%T", m))
	}

	d.mu.Lock()
	d.lastHubSeq = seq
	if wire.Reliable(msg) {
		_ = d.sendLocked(&nexusv1.ConnectRequest{Msg: &nexusv1.ConnectRequest_Ack{Ack: &nexusv1.Ack{Seq: seq}}})
	}
	d.mu.Unlock()
}

func (d *Daemon) withTimeout(f func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return f(ctx)
}

// controlCall runs a read-only node tool for the hub (NodeService.ListFiles).
func (d *Daemon) controlCall(ctx context.Context, in *nexusv1.RunInput) {
	res := &nexusv1.ToolResult{CallId: in.QuestionId}
	start := time.Now()
	if !wire.ControlTools[in.Text] {
		res.Error = fmt.Sprintf("control tool %q not allowed", in.Text)
	} else if t, ok := d.h.Tools().Get(in.Text); !ok {
		res.Error = fmt.Sprintf("tool %q not available", in.Text)
	} else {
		out, err := t.Invoke(ctx, kernel.ToolCall{ID: in.QuestionId, NodeID: d.cfg.NodeID, Input: json.RawMessage(in.Choice)})
		if err == nil {
			res.Output, err = wire.JSONToStruct(out.Output)
		}
		if err != nil {
			res.Error = err.Error()
		}
	}
	res.DurationMs = time.Since(start).Milliseconds()
	ev := &nexusv1.Event{Actor: "node:" + d.cfg.NodeID, At: timestamppb.Now(), Body: &nexusv1.Event_ToolResult{ToolResult: res}}
	if err := d.send(&nexusv1.ConnectRequest{Msg: &nexusv1.ConnectRequest_RunEvent{RunEvent: &nexusv1.RunEvent{Event: ev}}}); err != nil {
		d.log.Debug("control reply not sent", "err", err)
	}
}

func (d *Daemon) status() *nexusv1.NodeStatus {
	return &nexusv1.NodeStatus{
		NodeId: d.cfg.NodeID, Online: true,
		CpuPercent:     d.sampler.cpuPercent(),
		MemoryPercent:  memoryPercent(),
		DiskPercent:    diskPercent(d.cfg.WorkspacesDir),
		BatteryPercent: batteryPercent(),
		ActiveRuns:     int32(d.Runs.Active()),
		LastSeen:       timestamppb.Now(),
	}
}

func (d *Daemon) statusLoop(ctx context.Context) {
	t := time.NewTicker(d.cfg.StatusInterval)
	defer t.Stop()
	for {
		if err := d.send(&nexusv1.ConnectRequest{Msg: &nexusv1.ConnectRequest_Status{Status: d.status()}}); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Daemon) fetchHubTools(ctx context.Context) {
	out, err := d.CallHubTool(ctx, "", wire.ToolListHubTools, nil)
	if err != nil {
		d.log.Warn("could not list hub tools", "err", err)
		return
	}
	specs, err := wire.DecodeToolSpecs(out)
	if err != nil {
		d.log.Warn("bad hub tool list", "err", err)
		return
	}
	d.pmu.Lock()
	d.hubTools = specs
	d.pmu.Unlock()
}

// Connected returns a channel closed while the link is up (snapshot).
func (d *Daemon) Connected() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.connected
}

var _ kernel.HubToolClient = (*Daemon)(nil)

// CallHubTool implements kernel.HubToolClient. It waits for the link to be
// up (bounded by ctx); in-flight calls fail if the link drops.
func (d *Daemon) CallHubTool(ctx context.Context, runID, tool string, input []byte) ([]byte, error) {
	select {
	case <-d.Connected():
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	in, err := wire.JSONToStruct(input)
	if err != nil {
		return nil, fmt.Errorf("tool input: %w", err)
	}
	id := "tr_" + ulid.Make().String()
	ch := make(chan *nexusv1.ToolResponse, 1)
	d.pmu.Lock()
	d.pending[id] = ch
	d.pmu.Unlock()
	defer func() {
		d.pmu.Lock()
		delete(d.pending, id)
		d.pmu.Unlock()
	}()
	req := &nexusv1.ToolRequest{RequestId: id, RunId: runID, Tool: tool, Input: in}
	if err := d.send(&nexusv1.ConnectRequest{Msg: &nexusv1.ConnectRequest_ToolRequest{ToolRequest: req}}); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Error != "" {
			return nil, errors.New(resp.Error)
		}
		return wire.StructToJSON(resp.Output)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// HubTools implements kernel.HubToolClient (specs fetched at connect).
func (d *Daemon) HubTools() []kernel.ToolSpec {
	d.pmu.Lock()
	defer d.pmu.Unlock()
	return append([]kernel.ToolSpec(nil), d.hubTools...)
}

func (d *Daemon) failPending(err error) {
	d.pmu.Lock()
	defer d.pmu.Unlock()
	for id, ch := range d.pending {
		ch <- &nexusv1.ToolResponse{RequestId: id, Error: err.Error()}
		delete(d.pending, id)
	}
}
