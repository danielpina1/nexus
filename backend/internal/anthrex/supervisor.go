package anthrex

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Supervisor is the socket implementation of API. It keeps one control
// connection, redialing (with backoff) when it drops; WatchRuns survives
// reconnects by re-subscribing. PTY and conversation streams use their own
// connections (the daemon allows one PTY view per connection).
type Supervisor struct {
	opts Options

	mu      sync.Mutex
	client  *Client
	closed  bool
	lastErr error
	nextTry time.Time
	backoff time.Duration
}

// Backoff bounds between redials.
const (
	minBackoff = 500 * time.Millisecond
	maxBackoff = 30 * time.Second
)

// NewSupervisor returns a supervisor; it dials lazily.
func NewSupervisor(o Options) *Supervisor { return &Supervisor{opts: o} }

// Mode implements API.
func (s *Supervisor) Mode() string { return "socket" }

// Version implements API (the daemon version, "" before a connection).
func (s *Supervisor) Version() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client.DaemonVersion()
	}
	return ""
}

// Socket is the socket path in use.
func (s *Supervisor) Socket() string { return s.opts.socket() }

// Ping connects (or reuses the connection) and reports the handshake error.
func (s *Supervisor) Ping(ctx context.Context) error {
	_, err := s.conn(ctx, true)
	return err
}

// conn returns a live client, dialing when needed. force ignores backoff.
func (s *Supervisor) conn(ctx context.Context, force bool) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if s.client != nil && s.client.Err() == nil {
		return s.client, nil
	}
	if !force && time.Now().Before(s.nextTry) && s.lastErr != nil {
		return nil, s.lastErr
	}
	c, err := Dial(ctx, s.opts)
	if err != nil {
		s.lastErr = err
		if s.backoff == 0 {
			s.backoff = minBackoff
		} else if s.backoff *= 2; s.backoff > maxBackoff {
			s.backoff = maxBackoff
		}
		s.nextTry = time.Now().Add(s.backoff)
		return nil, err
	}
	s.client, s.lastErr, s.backoff = c, nil, 0
	return c, nil
}

// Close implements API.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.client != nil {
		s.client.Close()
	}
	return nil
}

// run calls f on a live client, retrying once on a dropped connection for
// requests that are safe to resend (idempotent reads).
func (s *Supervisor) run(ctx context.Context, idempotent bool, f func(*Client) error) error {
	c, err := s.conn(ctx, true)
	if err != nil {
		return err
	}
	err = f(c)
	if err != nil && idempotent && errors.Is(err, ErrClosed) {
		if c, err2 := s.conn(ctx, true); err2 == nil {
			return f(c)
		}
	}
	return err
}

func (s *Supervisor) request(ctx context.Context, req any, timeout time.Duration, idempotent bool) (*RunReply, error) {
	var r *RunReply
	err := s.run(ctx, idempotent, func(c *Client) error {
		var err error
		r, err = c.RunRequest(ctx, req, timeout)
		return err
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// done sends req and expects Done (Refused becomes an error).
func (s *Supervisor) done(ctx context.Context, req any) (string, error) {
	r, err := s.request(ctx, req, DefaultRequestTimeout, false)
	if err != nil {
		return "", err
	}
	if err := r.Err(); err != nil {
		return "", err
	}
	if r.Kind != "Done" {
		return "", fmt.Errorf("anthrex: unexpected reply %s", r.Kind)
	}
	return r.Message, nil
}

// ListRuns implements API.
func (s *Supervisor) ListRuns(ctx context.Context) (*RunsSnapshot, error) {
	var snap *RunsSnapshot
	err := s.run(ctx, true, func(c *Client) error {
		var err error
		snap, err = c.ListRuns(ctx)
		return err
	})
	return snap, err
}

// WatchRuns implements API: snapshots keep coming across reconnects.
func (s *Supervisor) WatchRuns(ctx context.Context) (<-chan *RunsSnapshot, error) {
	c, err := s.conn(ctx, true)
	if err != nil {
		return nil, err
	}
	in, err := c.WatchRuns(ctx)
	if err != nil {
		return nil, err
	}
	out := make(chan *RunsSnapshot, 1)
	go func() {
		defer close(out)
		for {
			for snap := range in {
				offerLatest(out, snap)
			}
			if ctx.Err() != nil {
				return
			}
			// Connection dropped: redial with backoff and re-subscribe.
			delay := minBackoff
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
				c, err := s.conn(ctx, true)
				if err == nil {
					if in, err = c.WatchRuns(ctx); err == nil {
						break
					}
				}
				if errors.Is(err, ErrClosed) && s.isClosed() {
					return
				}
				if delay *= 2; delay > maxBackoff {
					delay = maxBackoff
				}
			}
		}
	}()
	return out, nil
}

func (s *Supervisor) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// StartGoal implements API.
func (s *Supervisor) StartGoal(ctx context.Context, g StartGoalRequest) (*StartResult, error) {
	req, err := ReqStartGoal(g)
	if err != nil {
		return nil, err
	}
	timeout := GoalRequestTimeout
	r, err := s.request(ctx, req, timeout, false)
	if err != nil {
		return nil, err
	}
	switch r.Kind {
	case "Started":
		return &StartResult{RunID: r.RunID, State: r.State}, nil
	case "Triaged":
		if r.RunID == "" {
			return nil, fmt.Errorf("anthrex: %s", r.Message)
		}
		return &StartResult{RunID: r.RunID, Message: r.Message}, nil
	case "Queued":
		return &StartResult{Queued: true, GoalID: r.GoalID, Message: r.Message}, nil
	case "Refused":
		return nil, r.Err()
	case "Done":
		return &StartResult{Message: r.Message}, nil
	}
	return nil, fmt.Errorf("anthrex: unexpected reply %s to StartGoal", r.Kind)
}

// Approve implements API.
func (s *Supervisor) Approve(ctx context.Context, runID string) (string, error) {
	return s.done(ctx, ReqApprove(runID))
}

// Reject implements API.
func (s *Supervisor) Reject(ctx context.Context, runID string) (string, error) {
	return s.done(ctx, ReqReject(runID))
}

// ApproveHold implements API.
func (s *Supervisor) ApproveHold(ctx context.Context, runID, hold string) (string, error) {
	return s.done(ctx, ReqApproveHold(runID, hold))
}

// RejectHold implements API.
func (s *Supervisor) RejectHold(ctx context.Context, runID, hold string) (string, error) {
	return s.done(ctx, ReqRejectHold(runID, hold))
}

// DocGate implements API.
func (s *Supervisor) DocGate(ctx context.Context, runID, gate string, a DocGateAction) (string, error) {
	req, err := ReqDocGate(runID, gate, a)
	if err != nil {
		return "", err
	}
	return s.done(ctx, req)
}

// AnswerAsk implements API.
func (s *Supervisor) AnswerAsk(ctx context.Context, runID string, ask uint64, choice *uint32) (string, error) {
	return s.done(ctx, ReqAnswerAsk(runID, ask, choice))
}

// Message implements API.
func (s *Supervisor) Message(ctx context.Context, runID, to, text, kind string) (string, error) {
	e, err := EditMessage(to, text, kind)
	if err != nil {
		return "", err
	}
	return s.done(ctx, ReqEdit(runID, []any{e}, false))
}

// Cancel implements API.
func (s *Supervisor) Cancel(ctx context.Context, runID string) (string, error) {
	return s.done(ctx, ReqCancel(runID))
}

// Resume implements API.
func (s *Supervisor) Resume(ctx context.Context, runID string, rebaseline bool) (string, error) {
	return s.done(ctx, ReqResume(runID, rebaseline))
}

// Finish implements API.
func (s *Supervisor) Finish(ctx context.Context, runID, action, confirm string) (*FinishResult, error) {
	if action != FinishAccept && action != FinishDiscard {
		return nil, fmt.Errorf("anthrex: finish action %q", action)
	}
	r, err := s.request(ctx, ReqFinish(runID, action, confirm), FinishRequestTimeout, false)
	if err != nil {
		return nil, err
	}
	switch r.Kind {
	case "Done":
		return &FinishResult{Done: true, Message: r.Message}, nil
	case "ConfirmNeeded":
		return &FinishResult{ConfirmNeeded: true, Prompt: r.Prompt, BaseMoved: r.BaseMoved}, nil
	case "Refused":
		return nil, r.Err()
	}
	return nil, fmt.Errorf("anthrex: unexpected reply %s to Finish", r.Kind)
}

func (s *Supervisor) payload(ctx context.Context, req any, want string) (any, error) {
	r, err := s.request(ctx, req, DefaultRequestTimeout, true)
	if err != nil {
		return nil, err
	}
	if err := r.Err(); err != nil {
		return nil, err
	}
	if r.Kind != want {
		if r.Kind == "Done" {
			return r.Message, nil
		}
		return nil, fmt.Errorf("anthrex: unexpected reply %s (want %s)", r.Kind, want)
	}
	return r.Payload, nil
}

// ShowDoc implements API.
func (s *Supervisor) ShowDoc(ctx context.Context, runID, kind string, version *uint32) (any, error) {
	return s.payload(ctx, ReqShowDoc(runID, kind, version), "Doc")
}

// TaskDetail implements API.
func (s *Supervisor) TaskDetail(ctx context.Context, runID, taskID string) (any, error) {
	return s.payload(ctx, ReqTaskDetail(runID, taskID), "TaskDetail")
}

// Stats implements API.
func (s *Supervisor) Stats(ctx context.Context, dir string) (any, error) {
	return s.payload(ctx, ReqStats(dir), "Stats")
}

// ProfileStatus implements API.
func (s *Supervisor) ProfileStatus(ctx context.Context, dir string) (any, error) {
	p, err := s.payload(ctx, ReqProfileStatus(dir), "Profile")
	if err != nil {
		return nil, err
	}
	// ProfileReply::Status(ProfileStatus) | Refused{message} | Done{message}
	if kind, body, err := SplitVariant(p); err == nil {
		if kind == "Refused" {
			m, _ := body.(map[string]any)
			return nil, &RefusedError{Request: "profile", Message: str(m, "message")}
		}
		return body, nil
	}
	return p, nil
}

// ListWindows implements API.
func (s *Supervisor) ListWindows(ctx context.Context) ([]WindowInfo, error) {
	var w []WindowInfo
	err := s.run(ctx, true, func(c *Client) error {
		var err error
		w, err = c.ListWindows(ctx)
		return err
	})
	return w, err
}

// CreateWindow implements API.
func (s *Supervisor) CreateWindow(ctx context.Context, spec WindowSpec) (uint32, error) {
	var id uint32
	err := s.run(ctx, false, func(c *Client) error {
		var err error
		id, err = c.CreateWindow(ctx, spec, 0, 0)
		return err
	})
	return id, err
}

// SendInput implements API.
func (s *Supervisor) SendInput(ctx context.Context, windowID uint32, b []byte) error {
	return s.run(ctx, false, func(c *Client) error { return c.SendInput(ctx, windowID, b) })
}

// Conversation implements API: subscribe, take the first snapshot, leave.
func (s *Supervisor) Conversation(ctx context.Context, windowID uint32, agentID string) (*Conversation, error) {
	ctx, cancel := withDefault(ctx, 30*time.Second)
	defer cancel()
	ch, err := s.SubscribeConversation(ctx, windowID, agentID, nil)
	if err != nil {
		return nil, err
	}
	for ev := range ch {
		switch {
		case ev.Snapshot != nil:
			return ev.Snapshot, nil
		case ev.Gone != "":
			return nil, fmt.Errorf("anthrex: conversation gone: %s", ev.Gone)
		case ev.Err != nil:
			return nil, ev.Err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, ErrClosed
}

// PTYEvent is one item of a window's PTY stream.
type PTYEvent struct {
	// Snapshot: Bytes redraw the whole screen (first event, and after the
	// daemon resynchronises a lagging view); else Bytes is live output.
	Snapshot   bool
	Cols, Rows uint16
	Bytes      []byte
	Err        error // terminal: the stream ended with this error
}

// StreamWindow subscribes to a window's PTY on a dedicated connection. The
// channel closes when ctx ends or the connection drops (after an Err event
// when there is one). Input for the window goes through SendInput.
func (s *Supervisor) StreamWindow(ctx context.Context, windowID uint32, cols, rows uint16) (<-chan PTYEvent, error) {
	if cols == 0 || rows == 0 {
		cols, rows = 120, 40
	}
	c, err := dial(ctx, s.opts, true)
	if err != nil {
		return nil, err
	}
	if err := c.send(MsgSubscribe(windowID, cols, rows)); err != nil {
		c.Close()
		return nil, err
	}
	out := make(chan PTYEvent, 64)
	go func() {
		defer close(out)
		defer c.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-c.raw:
				if !ok {
					if err := c.Err(); err != nil && !errors.Is(err, ErrClosed) {
						sendEv(ctx, out, PTYEvent{Err: err})
					}
					return
				}
				var ev PTYEvent
				switch m.Kind {
				case "Snapshot":
					ev = PTYEvent{Snapshot: true, Cols: m.Cols, Rows: m.Rows, Bytes: m.Bytes}
				case "Output":
					ev = PTYEvent{Bytes: m.Bytes}
				case "Error":
					sendEv(ctx, out, PTYEvent{Err: fmt.Errorf("anthrex: subscribe: %s", m.Message)})
					return
				default:
					continue
				}
				if m.WindowID != windowID && m.Kind != "Error" {
					continue
				}
				if !sendEv(ctx, out, ev) {
					return
				}
			}
		}
	}()
	return out, nil
}

func sendEv[T any](ctx context.Context, ch chan T, v T) bool {
	select {
	case ch <- v:
		return true
	case <-ctx.Done():
		return false
	}
}

// ConversationEvent is one item of a conversation subscription.
type ConversationEvent struct {
	Snapshot *Conversation
	// Delta: from→to revision and the TurnPatch values ({"upsert": turn} or
	// {"drop": {"id": n}}), generic.
	FromRev, ToRev uint64
	Patches        []any
	Gone           string // the subscription ended (reason)
	Err            error
}

// SubscribeConversation follows a window's conversation (agentID "" = the
// main agent) on a dedicated connection. fromRev nil asks for a snapshot.
func (s *Supervisor) SubscribeConversation(ctx context.Context, windowID uint32, agentID string, fromRev *uint64) (<-chan ConversationEvent, error) {
	c, err := dial(ctx, s.opts, true)
	if err != nil {
		return nil, err
	}
	if err := c.send(MsgSubscribeConversation(windowID, agentID, fromRev)); err != nil {
		c.Close()
		return nil, err
	}
	out := make(chan ConversationEvent, 16)
	go func() {
		defer close(out)
		defer c.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-c.raw:
				if !ok {
					return
				}
				if m.WindowID != windowID {
					continue
				}
				var ev ConversationEvent
				switch m.Kind {
				case "ConversationSnapshot":
					ev.Snapshot = m.Conversation
				case "ConversationDelta":
					ev.FromRev, ev.ToRev, ev.Patches = m.FromRev, m.ToRev, m.TurnPatch
				case "ConversationGone":
					sendEv(ctx, out, ConversationEvent{Gone: nonEmpty(m.Reason, "gone")})
					return
				default:
					continue
				}
				if !sendEv(ctx, out, ev) {
					return
				}
			}
		}
	}()
	return out, nil
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
