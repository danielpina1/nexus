package anthrex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Errors.
var (
	// ErrProtoVersion: the daemon speaks another protocol version. Callers
	// fall back to the CLI client (which always matches its own daemon).
	ErrProtoVersion = errors.New("anthrex: protocol version mismatch")
	// ErrUnavailable: no daemon socket (or binary) to talk to.
	ErrUnavailable = errors.New("anthrex: daemon not reachable")
	// ErrUnsupported: the operation is not available on this client (CLI
	// mode has no PTY stream, AnswerAsk, …).
	ErrUnsupported = errors.New("anthrex: operation not supported by this client")
	// ErrClosed: the connection ended.
	ErrClosed = errors.New("anthrex: connection closed")
)

// HandshakeTimeout is proto::HANDSHAKE_TIMEOUT.
const HandshakeTimeout = 5 * time.Second

// Default reply budgets, mirroring crates/cli/src/run_cmd.rs.
const (
	DefaultRequestTimeout = 180 * time.Second
	// GoalRequestTimeout: run start --goal (RUN_START_TIMEOUT + triage).
	GoalRequestTimeout = 1220 * time.Second
	// FinishRequestTimeout: accept/discard run the merge under the daemon's
	// own (unbounded-by-us) merge timeout.
	FinishRequestTimeout = 15 * time.Minute
	// WindowRequestTimeout: new --worktree (WORKTREE_REQUEST_TIMEOUT).
	WindowRequestTimeout = 62 * time.Second
)

// Options configure the socket client.
type Options struct {
	// Socket overrides SocketPath's discovery.
	Socket string
	// Client is the ClientKind sent in Hello (default "cli").
	Client string
	Logger *slog.Logger
}

func (o Options) socket() string { return SocketPath(o.Socket) }

func (o Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Client is one handshaken connection to the Anthrex daemon. It is safe for
// concurrent use. A Client does not reconnect; see Supervisor.
type Client struct {
	conn          net.Conn
	log           *slog.Logger
	daemonVersion string

	wmu    sync.Mutex
	nextID atomic.Uint64

	mu          sync.Mutex
	windows     []WindowInfo
	pending     map[uint64]chan *RunReply
	listWaiters []chan *RunsSnapshot
	winWaiters  []chan []WindowInfo
	labels      map[string][]chan *DaemonMsg
	runSubs     map[int]chan *RunsSnapshot
	winSubs     map[int]chan []WindowInfo
	subSeq      int
	raw         chan *DaemonMsg // stream connections only

	done chan struct{}
	err  error
}

// Dial connects and handshakes. A daemon of another protocol version yields
// an error wrapping ErrProtoVersion; no socket yields ErrUnavailable.
func Dial(ctx context.Context, o Options) (*Client, error) {
	return dial(ctx, o, false)
}

func dial(ctx context.Context, o Options, raw bool) (*Client, error) {
	path := o.socket()
	var d net.Dialer
	dctx, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancel()
	conn, err := d.DialContext(dctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("%w at %s: %v", ErrUnavailable, path, err)
	}
	kind := o.Client
	if kind == "" {
		kind = ClientCLI
	}
	_ = conn.SetDeadline(time.Now().Add(HandshakeTimeout))
	if err := WriteFrame(conn, MsgHello(ProtoVersion, kind)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("anthrex: hello: %w", err)
	}
	v, err := ReadFrame(conn)
	if err != nil {
		conn.Close()
		if errors.Is(err, io.EOF) {
			// A daemon that drops the connection after Hello is either
			// shutting down or one that could not decode our Hello.
			return nil, fmt.Errorf("%w: daemon closed the connection during the handshake", ErrProtoVersion)
		}
		return nil, fmt.Errorf("anthrex: handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	m, err := DecodeDaemonMsg(v)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: undecodable welcome: %v", ErrProtoVersion, err)
	}
	switch m.Kind {
	case "Welcome":
	case "Error":
		conn.Close()
		if strings.Contains(m.Message, "protocol version") {
			return nil, fmt.Errorf("%w: %s", ErrProtoVersion, m.Message)
		}
		return nil, fmt.Errorf("anthrex: handshake refused: %s", m.Message)
	default:
		conn.Close()
		return nil, fmt.Errorf("anthrex: unexpected handshake reply %s", m.Kind)
	}
	c := &Client{
		conn: conn, log: o.logger(), daemonVersion: m.DaemonVersion, windows: m.Windows,
		pending: map[uint64]chan *RunReply{}, labels: map[string][]chan *DaemonMsg{},
		runSubs: map[int]chan *RunsSnapshot{}, winSubs: map[int]chan []WindowInfo{},
		done: make(chan struct{}),
	}
	if raw {
		c.raw = make(chan *DaemonMsg, 256)
	}
	go c.readLoop()
	return c, nil
}

// DaemonVersion is the daemon's crate version from Welcome.
func (c *Client) DaemonVersion() string { return c.daemonVersion }

// Done is closed when the connection ends; Err then says why.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is the reason the connection ended (nil while open).
func (c *Client) Err() error {
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.err
	default:
		return nil
	}
}

// Close ends the connection.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	return nil
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return
	default:
	}
	c.err = err
	close(c.done)
	for _, ch := range c.runSubs {
		close(ch)
	}
	for _, ch := range c.winSubs {
		close(ch)
	}
	c.runSubs, c.winSubs = map[int]chan *RunsSnapshot{}, map[int]chan []WindowInfo{}
	if c.raw != nil {
		close(c.raw)
	}
	c.mu.Unlock()
	_ = c.conn.Close()
}

func (c *Client) send(v any) error {
	select {
	case <-c.done:
		return c.closedErr()
	default:
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err := WriteFrame(c.conn, v); err != nil {
		c.fail(fmt.Errorf("anthrex: write: %w", err))
		return err
	}
	return nil
}

func (c *Client) closedErr() error {
	if err := c.Err(); err != nil {
		if errors.Is(err, ErrClosed) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrClosed, err)
	}
	return ErrClosed
}

func (c *Client) readLoop() {
	for {
		v, err := ReadFrame(c.conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = ErrClosed
			}
			c.fail(err)
			return
		}
		m, err := DecodeDaemonMsg(v)
		if err != nil {
			c.log.Warn("anthrex: undecodable daemon message", "err", err)
			continue
		}
		c.dispatch(m)
	}
}

// offerLatest sends v without blocking, replacing an unread older value.
func offerLatest[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}

func (c *Client) dispatch(m *DaemonMsg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done: // channels may be closed already
		return
	default:
	}
	switch m.Kind {
	case "Run":
		r := m.Run
		if r.Kind == "Snapshot" {
			for _, w := range c.listWaiters {
				w <- r.Snapshot
			}
			c.listWaiters = nil
			for _, ch := range c.runSubs {
				offerLatest(ch, r.Snapshot)
			}
			return
		}
		if r.RequestID != nil {
			if ch, ok := c.pending[*r.RequestID]; ok {
				delete(c.pending, *r.RequestID)
				ch <- r
				return
			}
		}
		c.log.Debug("anthrex: unmatched run reply", "kind", r.Kind)
	case "Welcome", "WindowsChanged":
		c.windows = m.Windows
		for _, w := range c.winWaiters {
			w <- m.Windows
		}
		c.winWaiters = nil
		for _, ch := range c.winSubs {
			offerLatest(ch, m.Windows)
		}
	case "Created":
		c.deliverLabel("create", m)
	case "Ack", "Error":
		if m.Kind == "Error" && m.Request == "subscribe" && c.raw != nil {
			c.rawSend(m)
			return
		}
		if !c.deliverLabel(m.Request, m) {
			c.log.Debug("anthrex: unmatched reply", "kind", m.Kind, "request", m.Request, "message", m.Message)
		}
	case "RemoveDirty":
		c.deliverLabel("remove", m)
	case "Bye":
		go c.fail(fmt.Errorf("%w: daemon said bye: %s", ErrClosed, m.Reason))
	case "Snapshot", "Output", "ConversationSnapshot", "ConversationDelta", "ConversationGone":
		if c.raw != nil {
			c.rawSend(m)
		}
	}
}

// rawSend delivers to the stream channel; a stalled consumer drops the
// connection rather than blocking the reader forever.
func (c *Client) rawSend(m *DaemonMsg) {
	select {
	case c.raw <- m:
	default:
		go c.fail(fmt.Errorf("%w: stream consumer too slow", ErrClosed))
	}
}

func (c *Client) deliverLabel(label string, m *DaemonMsg) bool {
	q := c.labels[label]
	if len(q) == 0 {
		return false
	}
	q[0] <- m
	c.labels[label] = q[1:]
	return true
}

func (c *Client) waitLabel(label string) chan *DaemonMsg {
	ch := make(chan *DaemonMsg, 1)
	c.mu.Lock()
	c.labels[label] = append(c.labels[label], ch)
	c.mu.Unlock()
	return ch
}

func (c *Client) dropLabel(label string, ch chan *DaemonMsg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.labels[label]
	for i, x := range q {
		if x == ch {
			c.labels[label] = append(q[:i:i], q[i+1:]...)
			return
		}
	}
}

func withDefault(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// RunRequest sends a RunTagged request and waits for its reply. Refused
// replies are returned as a reply (check RunReply.Err).
func (c *Client) RunRequest(ctx context.Context, req any, timeout time.Duration) (*RunReply, error) {
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	ctx, cancel := withDefault(ctx, timeout)
	defer cancel()
	id := c.nextID.Add(1)
	ch := make(chan *RunReply, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.send(MsgRunTagged(id, req)); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-c.done:
		return nil, c.closedErr()
	case <-ctx.Done():
		return nil, fmt.Errorf("anthrex: waiting for reply: %w", ctx.Err())
	}
}

// ListRuns asks for the current runs snapshot.
func (c *Client) ListRuns(ctx context.Context) (*RunsSnapshot, error) {
	ctx, cancel := withDefault(ctx, 30*time.Second)
	defer cancel()
	ch := make(chan *RunsSnapshot, 1)
	c.mu.Lock()
	c.listWaiters = append(c.listWaiters, ch)
	c.mu.Unlock()
	if err := c.send(MsgRunTagged(c.nextID.Add(1), ReqList())); err != nil {
		return nil, err
	}
	select {
	case s := <-ch:
		return s, nil
	case <-c.done:
		return nil, c.closedErr()
	case <-ctx.Done():
		c.mu.Lock()
		for i, w := range c.listWaiters {
			if w == ch {
				c.listWaiters = append(c.listWaiters[:i:i], c.listWaiters[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
		return nil, fmt.Errorf("anthrex: waiting for runs: %w", ctx.Err())
	}
}

// WatchRuns subscribes to runs snapshots: the current one at once, then
// every later one. Slow readers see only the latest. The channel closes when
// ctx ends or the connection drops.
func (c *Client) WatchRuns(ctx context.Context) (<-chan *RunsSnapshot, error) {
	ch := make(chan *RunsSnapshot, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil, c.closedErr()
	default:
	}
	c.subSeq++
	id := c.subSeq
	first := len(c.runSubs) == 0
	c.runSubs[id] = ch
	c.mu.Unlock()
	if first {
		if err := c.send(MsgRunTagged(c.nextID.Add(1), ReqSubscribe())); err != nil {
			return nil, err
		}
	} else if s, err := c.ListRuns(ctx); err == nil {
		offerLatest(ch, s)
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-c.done:
			return
		}
		c.mu.Lock()
		if _, ok := c.runSubs[id]; ok {
			delete(c.runSubs, id)
			close(ch)
		}
		last := len(c.runSubs) == 0
		c.mu.Unlock()
		if last {
			_ = c.send(MsgRunTagged(c.nextID.Add(1), ReqUnsubscribe()))
		}
	}()
	return ch, nil
}

// WatchWindows streams the window list on every change (latest wins).
func (c *Client) WatchWindows(ctx context.Context) (<-chan []WindowInfo, error) {
	ch := make(chan []WindowInfo, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil, c.closedErr()
	default:
	}
	c.subSeq++
	id := c.subSeq
	c.winSubs[id] = ch
	offerLatest(ch, c.windows)
	c.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-c.done:
			return
		}
		c.mu.Lock()
		if _, ok := c.winSubs[id]; ok {
			delete(c.winSubs, id)
			close(ch)
		}
		c.mu.Unlock()
	}()
	return ch, nil
}

// ListWindows asks for the window list.
func (c *Client) ListWindows(ctx context.Context) ([]WindowInfo, error) {
	ctx, cancel := withDefault(ctx, 30*time.Second)
	defer cancel()
	ch := make(chan []WindowInfo, 1)
	c.mu.Lock()
	c.winWaiters = append(c.winWaiters, ch)
	c.mu.Unlock()
	if err := c.send(MsgListWindows()); err != nil {
		return nil, err
	}
	select {
	case w := <-ch:
		return w, nil
	case <-c.done:
		return nil, c.closedErr()
	case <-ctx.Done():
		return nil, fmt.Errorf("anthrex: waiting for windows: %w", ctx.Err())
	}
}

// labelled sends msg and waits for the reply carrying label (Ack, Error,
// Created, RemoveDirty).
func (c *Client) labelled(ctx context.Context, label string, msg any, timeout time.Duration) (*DaemonMsg, error) {
	ctx, cancel := withDefault(ctx, timeout)
	defer cancel()
	ch := c.waitLabel(label)
	if err := c.send(msg); err != nil {
		c.dropLabel(label, ch)
		return nil, err
	}
	select {
	case m := <-ch:
		return m, nil
	case <-c.done:
		return nil, c.closedErr()
	case <-ctx.Done():
		c.dropLabel(label, ch)
		return nil, fmt.Errorf("anthrex: waiting for %s: %w", label, ctx.Err())
	}
}

var createMu sync.Mutex // one CreateWindow in flight per process: replies carry no id

// CreateWindow creates a window and returns its id.
func (c *Client) CreateWindow(ctx context.Context, spec WindowSpec, cols, rows uint16) (uint32, error) {
	if cols == 0 || rows == 0 {
		cols, rows = 120, 40
	}
	createMu.Lock()
	defer createMu.Unlock()
	m, err := c.labelled(ctx, "create", MsgCreateWindow(spec, cols, rows), WindowRequestTimeout)
	if err != nil {
		return 0, err
	}
	if m.Kind == "Created" {
		return m.WindowID, nil
	}
	return 0, fmt.Errorf("anthrex: create window: %s", m.Message)
}

func ackErr(m *DaemonMsg) error {
	switch m.Kind {
	case "Ack":
		return nil
	case "Error", "RemoveDirty":
		return fmt.Errorf("anthrex: %s", m.Message)
	}
	return fmt.Errorf("anthrex: unexpected reply %s", m.Kind)
}

// Kill kills a window's process group.
func (c *Client) Kill(ctx context.Context, windowID uint32) error {
	m, err := c.labelled(ctx, "kill", MsgKill(windowID), 30*time.Second)
	if err != nil {
		return err
	}
	return ackErr(m)
}

// Restart restarts a window.
func (c *Client) Restart(ctx context.Context, windowID uint32) error {
	m, err := c.labelled(ctx, "restart", MsgRestart(windowID), 30*time.Second)
	if err != nil {
		return err
	}
	return ackErr(m)
}

// inputGrace is how long SendInput waits for an Error: Input is answered
// only when it fails.
const inputGrace = 300 * time.Millisecond

// SendInput writes bytes to a window's PTY.
func (c *Client) SendInput(ctx context.Context, windowID uint32, b []byte) error {
	ch := c.waitLabel("input")
	defer c.dropLabel("input", ch)
	if err := c.send(MsgInput(windowID, b)); err != nil {
		return err
	}
	t := time.NewTimer(inputGrace)
	defer t.Stop()
	select {
	case m := <-ch:
		return ackErr(m)
	case <-t.C:
		return nil
	case <-c.done:
		return c.closedErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}
