package nodelink

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/nodelink/wire"
	"github.com/danielpina1/nexus/backend/kernel"
)

var errOffline = errors.New("nodelink: node is not connected")

// Hub tracks nodes and their link sessions.
type Hub struct {
	h   kernel.Host
	log *slog.Logger
	cfg Config

	ctx    context.Context
	cancel context.CancelFunc
	store  *nodeStore // optional persistence

	mu    sync.Mutex
	nodes map[string]*nodeState
	runs  map[string]*runInfo
	calls map[string]chan *nexusv1.ToolResult
}

type runInfo struct {
	nodeID string
	taskID string
	token  string
	agent  *nexusv1.Agent
}

// nodeState persists across sessions of one node.
type nodeState struct {
	id string
	// guarded by Hub.mu
	node   *nexusv1.Node
	active int
	online bool

	// guarded by sendMu
	sendMu  sync.Mutex
	sess    *session
	nextSeq int64
	outbox  []*outMsg

	lastNodeSeq atomic.Int64
}

type outMsg struct {
	seq int64 // 0 = queued while offline, not yet numbered
	msg *nexusv1.ConnectResponse
}

type session struct {
	stream   *connect.BidiStream[nexusv1.ConnectRequest, nexusv1.ConnectResponse]
	cancel   context.CancelFunc
	lastSeen atomic.Int64 // unix nanos
}

func (s *session) touch() { s.lastSeen.Store(time.Now().UnixNano()) }

// NewHub creates a hub; Start must be called before serving.
func NewHub(h kernel.Host, cfg Config) *Hub {
	cfg.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		h: h, log: h.Logger(), cfg: cfg, ctx: ctx, cancel: cancel,
		nodes: map[string]*nodeState{},
		runs:  map[string]*runInfo{},
		calls: map[string]chan *nexusv1.ToolResult{},
	}
}

// Start loads persisted nodes when a store is available.
func (hb *Hub) Start(ctx context.Context) error {
	if st, err := kernel.Resolve[kernel.Store](hb.h, kernel.SvcStore); err == nil {
		s, err := newNodeStore(st)
		if err != nil {
			return err
		}
		hb.store = s
		nodes, err := s.load()
		if err != nil {
			hb.log.Warn("load nodes", "err", err)
		}
		hb.mu.Lock()
		for _, n := range nodes {
			if _, ok := hb.nodes[n.Id]; ok {
				continue
			}
			if n.Status == nil {
				n.Status = &nexusv1.NodeStatus{NodeId: n.Id}
			}
			n.Status.Online = false
			n.Status.ActiveRuns = 0
			hb.nodes[n.Id] = &nodeState{id: n.Id, node: n}
		}
		hb.mu.Unlock()
	}
	if len(hb.cfg.Tokens) == 0 {
		if _, ok := hb.nodeAuthenticator(); !ok {
			hb.log.Warn("nodelink: no node tokens configured and no security service; all nodes will be rejected")
		}
	}
	return nil
}

// Stop closes all sessions.
func (hb *Hub) Stop(context.Context) error {
	hb.cancel()
	return nil
}

func (hb *Hub) nodeAuthenticator() (NodeAuthenticator, bool) {
	v, ok := hb.h.Lookup(kernel.SvcSecurity)
	if !ok {
		return nil, false
	}
	a, ok := v.(NodeAuthenticator)
	return a, ok
}

func (hb *Hub) authenticate(ctx context.Context, nodeID, token string) error {
	if nodeID == "" || token == "" {
		return errors.New("missing node id or token")
	}
	if a, ok := hb.nodeAuthenticator(); ok {
		return a.AuthenticateNode(ctx, nodeID, token)
	}
	want, ok := hb.cfg.Tokens[nodeID]
	if !ok || want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
		return errors.New("invalid node credentials")
	}
	return nil
}

func bearer(v string) string {
	const p = "Bearer "
	if len(v) > len(p) && strings.EqualFold(v[:len(p)], p) {
		return strings.TrimSpace(v[len(p):])
	}
	return ""
}

// Connect implements nexusv1connect.NodeLinkServiceHandler.
func (hb *Hub) Connect(ctx context.Context, stream *connect.BidiStream[nexusv1.ConnectRequest, nexusv1.ConnectResponse]) error {
	nodeID := stream.RequestHeader().Get(wire.HeaderNodeID)
	token := bearer(stream.RequestHeader().Get("Authorization"))
	if err := hb.authenticate(ctx, nodeID, token); err != nil {
		hb.log.Warn("node authentication failed", "node", nodeID, "err", err)
		return connect.NewError(connect.CodeUnauthenticated, errors.New("node authentication failed"))
	}
	first, err := stream.Receive()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("first message must be NodeHello"))
	}
	if hello.NodeId != "" && hello.NodeId != nodeID {
		return connect.NewError(connect.CodePermissionDenied, errors.New("hello node_id does not match credentials"))
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(hb.ctx, cancel)
	defer stop()

	sess := &session{stream: stream, cancel: cancel}
	sess.touch()
	ns := hb.register(nodeID, hello)
	if err := hb.attach(ns, sess, hello.ResumeSeq); err != nil {
		hb.detach(ns, sess)
		return err
	}
	defer hb.detach(ns, sess)
	hb.log.Info("node connected", "node", nodeID, "resume_seq", hello.ResumeSeq, "runtimes", len(hello.Runtimes))

	errc := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Receive()
			if err != nil {
				errc <- err
				return
			}
			sess.touch()
			hb.handle(sctx, ns, sess, msg)
		}
	}()

	tick := time.NewTicker(hb.cfg.HeartbeatTimeout / 3)
	defer tick.Stop()
	for {
		select {
		case <-sctx.Done():
			if ctx.Err() != nil {
				return nil // client went away
			}
			return connect.NewError(connect.CodeAborted, errors.New("session replaced or hub stopping"))
		case err := <-errc:
			if isEOF(err) {
				return nil
			}
			hb.log.Info("node stream ended", "node", nodeID, "err", err)
			return nil
		case <-tick.C:
			if time.Since(time.Unix(0, sess.lastSeen.Load())) > hb.cfg.HeartbeatTimeout {
				hb.log.Warn("node heartbeat timeout", "node", nodeID)
				return connect.NewError(connect.CodeDeadlineExceeded, errors.New("heartbeat timeout"))
			}
		}
	}
}

func isEOF(err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "EOF"))
}

// register records the node's hello and marks it online.
func (hb *Hub) register(nodeID string, hello *nexusv1.NodeHello) *nodeState {
	hb.mu.Lock()
	ns, ok := hb.nodes[nodeID]
	if !ok {
		ns = &nodeState{id: nodeID}
		hb.nodes[nodeID] = ns
	}
	n := ns.node
	if n == nil {
		n = &nexusv1.Node{Id: nodeID, Name: nodeID, PairedAt: timestamppb.Now()}
	} else {
		n = proto.Clone(n).(*nexusv1.Node)
	}
	n.Tags = append([]string(nil), hello.Tags...)
	n.Runtimes = hello.Runtimes
	for _, t := range hello.Tags {
		if v, ok := strings.CutPrefix(t, "os:"); ok {
			n.Os = v
		}
		if v, ok := strings.CutPrefix(t, "arch:"); ok {
			n.Arch = v
		}
	}
	st := &nexusv1.NodeStatus{NodeId: nodeID, Online: true, LastSeen: timestamppb.Now(), ActiveRuns: int32(ns.active)}
	if n.Status != nil {
		st.CpuPercent, st.MemoryPercent, st.DiskPercent, st.BatteryPercent =
			n.Status.CpuPercent, n.Status.MemoryPercent, n.Status.DiskPercent, n.Status.BatteryPercent
	}
	n.Status = st
	ns.node = n
	ns.online = true
	snap := proto.Clone(n).(*nexusv1.Node)
	hb.mu.Unlock()
	hb.persist(snap)
	hb.publishStatus(snap.Status)
	return ns
}

// attach makes sess the node's current session, sends HubWelcome and
// re-sends unacknowledged messages.
func (hb *Hub) attach(ns *nodeState, sess *session, resumeSeq int64) error {
	ns.sendMu.Lock()
	defer ns.sendMu.Unlock()
	if old := ns.sess; old != nil {
		old.cancel()
	}
	ns.sess = sess
	keep := ns.outbox[:0]
	for _, m := range ns.outbox {
		if m.seq == 0 || m.seq > resumeSeq {
			keep = append(keep, m)
		}
	}
	ns.outbox = keep
	ns.nextSeq = resumeSeq
	welcome := &nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Welcome{
		Welcome: &nexusv1.HubWelcome{ResumeSeq: ns.lastNodeSeq.Load()},
	}}
	if err := sess.stream.Send(welcome); err != nil {
		return err
	}
	for _, m := range ns.outbox {
		ns.nextSeq++
		m.seq = ns.nextSeq
		if err := sess.stream.Send(m.msg); err != nil {
			return err
		}
	}
	return nil
}

// detach clears the session and marks the node offline (unless replaced).
func (hb *Hub) detach(ns *nodeState, sess *session) {
	ns.sendMu.Lock()
	current := ns.sess == sess
	if current {
		// Outbox entries keep their seq so the next NodeHello.resume_seq can
		// tell which ones the node already processed; attach renumbers.
		ns.sess = nil
	}
	ns.sendMu.Unlock()
	if !current {
		return
	}
	hb.mu.Lock()
	ns.online = false
	n := proto.Clone(ns.node).(*nexusv1.Node)
	n.Status.Online = false
	n.Status.LastSeen = timestamppb.Now()
	ns.node = n
	snap := proto.Clone(n).(*nexusv1.Node)
	hb.mu.Unlock()
	hb.persist(snap)
	hb.publishStatus(snap.Status)
	hb.log.Info("node disconnected", "node", ns.id)
}

// send numbers and writes a message to the node. Reliable messages are kept
// in the outbox until acknowledged (and queued while offline).
func (hb *Hub) send(ns *nodeState, msg *nexusv1.ConnectResponse) error {
	reliable := wire.Reliable(msg)
	ns.sendMu.Lock()
	defer ns.sendMu.Unlock()
	if reliable && len(ns.outbox) >= hb.cfg.OutboxLimit {
		return fmt.Errorf("nodelink: outbox full for node %s", ns.id)
	}
	if ns.sess == nil {
		if !reliable {
			return errOffline
		}
		ns.outbox = append(ns.outbox, &outMsg{msg: msg})
		return nil
	}
	ns.nextSeq++
	if reliable {
		ns.outbox = append(ns.outbox, &outMsg{seq: ns.nextSeq, msg: msg})
	}
	if err := ns.sess.stream.Send(msg); err != nil {
		ns.sess.cancel()
		if reliable {
			return nil // re-sent on reconnect
		}
		return err
	}
	return nil
}

func (hb *Hub) ack(ns *nodeState, sess *session, seq int64) {
	ns.sendMu.Lock()
	defer ns.sendMu.Unlock()
	if ns.sess != sess {
		return // stale session: its numbering no longer applies
	}
	keep := ns.outbox[:0]
	for _, m := range ns.outbox {
		if m.seq == 0 || m.seq > seq {
			keep = append(keep, m)
		}
	}
	ns.outbox = keep
}

func (hb *Hub) handle(ctx context.Context, ns *nodeState, sess *session, msg *nexusv1.ConnectRequest) {
	switch m := msg.Msg.(type) {
	case *nexusv1.ConnectRequest_Status:
		hb.updateStatus(ns, m.Status)
	case *nexusv1.ConnectRequest_RunEvent:
		hb.handleRunEvent(ctx, ns, m.RunEvent)
	case *nexusv1.ConnectRequest_ToolRequest:
		go hb.handleToolRequest(ctx, ns, m.ToolRequest)
	case *nexusv1.ConnectRequest_Ack:
		hb.ack(ns, sess, m.Ack.Seq)
	case *nexusv1.ConnectRequest_Hello:
		hb.log.Debug("duplicate hello ignored", "node", ns.id)
	default:
		hb.log.Debug("node message ignored", "node", ns.id, "type", fmt.Sprintf("%T", m))
	}
}

func (hb *Hub) updateStatus(ns *nodeState, st *nexusv1.NodeStatus) {
	st = proto.Clone(st).(*nexusv1.NodeStatus)
	st.NodeId = ns.id
	st.Online = true
	st.LastSeen = timestamppb.Now()
	hb.mu.Lock()
	n := proto.Clone(ns.node).(*nexusv1.Node)
	n.Status = st
	ns.node = n
	hb.mu.Unlock()
	hb.publishStatus(proto.Clone(st).(*nexusv1.NodeStatus))
}

func (hb *Hub) publishStatus(st *nexusv1.NodeStatus) {
	bus, err := kernel.Resolve[kernel.EventBus](hb.h, kernel.SvcEventBus)
	if err != nil {
		return
	}
	ev := &nexusv1.Event{Actor: "node:" + st.NodeId, Body: &nexusv1.Event_NodeStatus{NodeStatus: st}}
	if _, err := bus.Publish(hb.ctx, ev); err != nil {
		hb.log.Warn("publish node status", "err", err)
	}
}

func (hb *Hub) handleRunEvent(ctx context.Context, ns *nodeState, re *nexusv1.RunEvent) {
	ev := re.GetEvent()
	if re.NodeSeq == 0 {
		// Unsequenced control reply.
		if tr := ev.GetToolResult(); tr != nil {
			hb.mu.Lock()
			ch := hb.calls[tr.CallId]
			delete(hb.calls, tr.CallId)
			hb.mu.Unlock()
			if ch != nil {
				ch <- tr
			}
		}
		return
	}
	if re.NodeSeq <= ns.lastNodeSeq.Load() {
		hb.sendAck(ns, ns.lastNodeSeq.Load()) // duplicate after resume
		return
	}
	if ev != nil && hb.acceptRunEvent(ns, ev) {
		hb.deliver(ctx, ns.id, re)
	}
	ns.lastNodeSeq.Store(re.NodeSeq)
	hb.sendAck(ns, re.NodeSeq)
}

func (hb *Hub) sendAck(ns *nodeState, seq int64) {
	_ = hb.send(ns, &nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Ack{Ack: &nexusv1.Ack{Seq: seq}}})
}

// acceptRunEvent validates ownership and updates run accounting.
func (hb *Hub) acceptRunEvent(ns *nodeState, ev *nexusv1.Event) bool {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	ri, known := hb.runs[ev.RunId]
	if known && ri.nodeID != ns.id {
		hb.log.Warn("run event from wrong node dropped", "node", ns.id, "run", ev.RunId, "owner", ri.nodeID)
		return false
	}
	if !known {
		hb.log.Debug("run event for unknown run", "node", ns.id, "run", ev.RunId)
	}
	if a := ev.GetApproval(); a != nil && a.NodeId == "" {
		a.NodeId = ns.id
	}
	if tu := ev.GetTaskUpdated(); tu != nil && tu.Task != nil {
		if tu.Task.NodeId == "" {
			tu.Task.NodeId = ns.id
		}
		if known && wire.TerminalState(tu.Task.State) {
			delete(hb.runs, ev.RunId)
			if ns.active > 0 {
				ns.active--
			}
		}
	}
	return true
}

func (hb *Hub) deliver(ctx context.Context, nodeID string, re *nexusv1.RunEvent) {
	if te, err := kernel.Resolve[kernel.TaskEngine](hb.h, kernel.SvcTasks); err == nil {
		if err := te.HandleRunEvent(ctx, nodeID, re); err != nil {
			hb.log.Warn("task engine rejected run event", "node", nodeID, "run", re.Event.RunId, "err", err)
		}
		return
	}
	bus, err := kernel.Resolve[kernel.EventBus](hb.h, kernel.SvcEventBus)
	if err != nil {
		hb.log.Warn("no task engine or event bus; run event dropped", "node", nodeID)
		return
	}
	ev := proto.Clone(re.Event).(*nexusv1.Event)
	if ev.Actor == "" {
		ev.Actor = "node:" + nodeID
	}
	if _, err := bus.Publish(ctx, ev); err != nil {
		hb.log.Warn("publish run event", "err", err)
	}
}

func (hb *Hub) handleToolRequest(ctx context.Context, ns *nodeState, req *nexusv1.ToolRequest) {
	resp := &nexusv1.ToolResponse{RequestId: req.RequestId}
	out, err := hb.invokeTool(ctx, ns, req)
	if err != nil {
		resp.Error = err.Error()
	} else if resp.Output, err = wire.JSONToStruct(out); err != nil {
		resp.Error = "encode output: " + err.Error()
	}
	if err := hb.send(ns, &nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_ToolResponse{ToolResponse: resp}}); err != nil {
		hb.log.Debug("tool response not delivered", "node", ns.id, "err", err)
	}
}

func (hb *Hub) invokeTool(ctx context.Context, ns *nodeState, req *nexusv1.ToolRequest) ([]byte, error) {
	if req.Tool == wire.ToolListHubTools {
		return wire.EncodeToolSpecs(hb.h.Tools().List())
	}
	hb.mu.Lock()
	ri := hb.runs[req.RunId]
	hb.mu.Unlock()
	if ri == nil || ri.nodeID != ns.id {
		return nil, fmt.Errorf("unknown run %q for this node", req.RunId)
	}
	if !contains(ri.agent.GetTools(), req.Tool) {
		return nil, fmt.Errorf("tool %q not allowed for agent %q", req.Tool, ri.agent.GetId())
	}
	tool, ok := hb.h.Tools().Get(req.Tool)
	if !ok {
		return nil, fmt.Errorf("unknown hub tool %q", req.Tool)
	}
	in, err := wire.StructToJSON(req.Input)
	if err != nil {
		return nil, err
	}
	res, err := tool.Invoke(ctx, kernel.ToolCall{
		ID: req.RequestId, TaskID: ri.taskID, RunID: req.RunId,
		AgentID: ri.agent.GetId(), NodeID: ns.id, Input: in,
	})
	if err != nil {
		return nil, err
	}
	return res.Output, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// controlCall invokes a read-only node tool over the link (see wire docs).
func (hb *Hub) controlCall(ctx context.Context, nodeID, tool string, input []byte) ([]byte, error) {
	hb.mu.Lock()
	ns := hb.nodes[nodeID]
	online := ns != nil && ns.online
	hb.mu.Unlock()
	if ns == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown node %q", nodeID))
	}
	if !online {
		return nil, connect.NewError(connect.CodeUnavailable, errOffline)
	}
	id := wire.ControlCallPrefix + ulid.Make().String()
	ch := make(chan *nexusv1.ToolResult, 1)
	hb.mu.Lock()
	hb.calls[id] = ch
	hb.mu.Unlock()
	defer func() {
		hb.mu.Lock()
		delete(hb.calls, id)
		hb.mu.Unlock()
	}()
	msg := &nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Input{Input: &nexusv1.RunInput{
		QuestionId: id, Text: tool, Choice: string(input),
	}}}
	if err := hb.send(ns, msg); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	t := time.NewTimer(hb.cfg.ControlTimeout)
	defer t.Stop()
	select {
	case tr := <-ch:
		if tr.Error != "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(tr.Error))
		}
		return wire.StructToJSON(tr.Output)
	case <-t.C:
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("node did not answer"))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (hb *Hub) persist(n *nexusv1.Node) {
	if hb.store == nil {
		return
	}
	if err := hb.store.upsert(n); err != nil {
		hb.log.Warn("persist node", "node", n.Id, "err", err)
	}
}

// snapshot returns copies of all nodes sorted by id.
func (hb *Hub) snapshot() []*nexusv1.Node {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	out := make([]*nexusv1.Node, 0, len(hb.nodes))
	for _, ns := range hb.nodes {
		if ns.node == nil {
			continue
		}
		n := proto.Clone(ns.node).(*nexusv1.Node)
		if n.Status == nil {
			n.Status = &nexusv1.NodeStatus{NodeId: n.Id}
		}
		n.Status.Online = ns.online
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out
}
