// Package anthrextest is an in-process fake Anthrex daemon speaking the
// real wire protocol (length-prefixed MessagePack over a Unix socket) for
// tests of the client and the Nexus runtime. Its default answers follow
// crates/daemon/src/server.rs and server/run_api.rs.
package anthrextest

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/danielpina1/nexus/backend/internal/anthrex"
)

// Request is one request the daemon received.
type Request struct {
	// Kind is the ClientMsg variant ("RunTagged", "Input", …); for
	// RunTagged it is the inner RunRequest variant prefixed "Run.".
	Kind string
	ID   uint64 // RunTagged id
	Body map[string]any
	Raw  any
}

// Handler answers a run request. Return nil to use the default answer.
// The reply is a RunReply value (e.g. map{"Done": {...}}); the daemon
// stamps request_id itself.
type Handler func(d *Daemon, kind string, body map[string]any) any

// Daemon is the fake.
type Daemon struct {
	Path    string
	Version uint32 // protocol version it accepts (default 21)
	Handler Handler

	t        testing.TB
	ln       net.Listener
	mu       sync.Mutex
	revision uint64
	runs     []any
	windows  []any
	requests []Request
	subs     map[*conn]bool
	conns    map[*conn]bool
	wg       sync.WaitGroup
	notify   chan struct{}
}

type conn struct {
	c  net.Conn
	mu sync.Mutex
}

func (c *conn) write(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return anthrex.WriteFrame(c.c, v)
}

// Start listens on a fresh socket; it stops at test cleanup.
func Start(t testing.TB) *Daemon {
	t.Helper()
	dir, err := os.MkdirTemp("", "axd")
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		Path: filepath.Join(dir, "daemon.sock"), Version: anthrex.ProtoVersion, t: t,
		subs: map[*conn]bool{}, conns: map[*conn]bool{}, notify: make(chan struct{}, 1),
		windows: []any{},
	}
	ln, err := net.Listen("unix", d.Path)
	if err != nil {
		t.Fatal(err)
	}
	d.ln = ln
	d.wg.Add(1)
	go d.accept()
	t.Cleanup(func() {
		d.Close()
		os.RemoveAll(dir)
	})
	return d
}

// Close stops the daemon and drops every connection.
func (d *Daemon) Close() {
	d.ln.Close()
	d.mu.Lock()
	for c := range d.conns {
		c.c.Close()
	}
	d.mu.Unlock()
	d.wg.Wait()
}

// DropConnections closes every client connection (to test reconnects).
func (d *Daemon) DropConnections() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for c := range d.conns {
		c.c.Close()
	}
}

// Requests returns what was received so far.
func (d *Daemon) Requests() []Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Request(nil), d.requests...)
}

// Changed is signalled (non-blocking) whenever a request arrives.
func (d *Daemon) Changed() <-chan struct{} { return d.notify }

// Find returns the received requests of kind.
func (d *Daemon) Find(kind string) []Request {
	var out []Request
	for _, r := range d.Requests() {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// SetWindows replaces the window list (generic WindowInfo maps) and pushes
// WindowsChanged to every connection.
func (d *Daemon) SetWindows(w []any) {
	d.mu.Lock()
	d.windows = w
	conns := d.connList()
	d.mu.Unlock()
	for _, c := range conns {
		_ = c.write(map[string]any{"WindowsChanged": map[string]any{"windows": w}})
	}
}

// SetRuns replaces the runs (generic RunInfo maps), bumps the revision and
// pushes the snapshot to subscribers.
func (d *Daemon) SetRuns(runs []any) {
	d.mu.Lock()
	d.runs = runs
	d.revision++
	snap := d.snapshotLocked()
	var subs []*conn
	for c := range d.subs {
		subs = append(subs, c)
	}
	d.mu.Unlock()
	for _, c := range subs {
		_ = c.write(map[string]any{"Run": map[string]any{"Snapshot": snap}})
	}
}

// Runs returns the current runs.
func (d *Daemon) Runs() []any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runs
}

func (d *Daemon) connList() []*conn {
	var out []*conn
	for c := range d.conns {
		out = append(out, c)
	}
	return out
}

func (d *Daemon) snapshotLocked() map[string]any {
	runs := d.runs
	if runs == nil {
		runs = []any{}
	}
	return map[string]any{"revision": d.revision, "runs": runs, "now": uint64(1_700_000_000), "proposals": []any{}}
}

func (d *Daemon) accept() {
	defer d.wg.Done()
	for {
		nc, err := d.ln.Accept()
		if err != nil {
			return
		}
		c := &conn{c: nc}
		d.mu.Lock()
		d.conns[c] = true
		d.mu.Unlock()
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.serve(c)
			d.mu.Lock()
			delete(d.conns, c)
			delete(d.subs, c)
			d.mu.Unlock()
			nc.Close()
		}()
	}
}

func (d *Daemon) record(r Request) {
	d.mu.Lock()
	d.requests = append(d.requests, r)
	d.mu.Unlock()
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

func (d *Daemon) serve(c *conn) {
	v, err := anthrex.ReadFrame(c.c)
	if err != nil {
		return
	}
	kind, body, err := anthrex.SplitVariant(v)
	f, _ := body.(map[string]any)
	if err != nil || kind != "Hello" {
		return
	}
	d.record(Request{Kind: "Hello", Body: f, Raw: v})
	if ver, _ := f["proto_version"].(int64); uint32(ver) != d.Version {
		_ = c.write(map[string]any{"Error": map[string]any{"request": "hello",
			"message": fmt.Sprintf("protocol version mismatch (client %d, daemon %d); run `anthrex daemon stop` and try again", ver, d.Version)}})
		return
	}
	d.mu.Lock()
	windows := d.windows
	d.mu.Unlock()
	if err := c.write(map[string]any{"Welcome": map[string]any{"daemon_version": "0.9.10-fake", "windows": windows}}); err != nil {
		return
	}
	for {
		v, err := anthrex.ReadFrame(c.c)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return
			}
			return
		}
		if err := d.handle(c, v); err != nil {
			return
		}
	}
}

func (d *Daemon) handle(c *conn, v any) error {
	kind, body, err := anthrex.SplitVariant(v)
	if err != nil {
		return err
	}
	f, _ := body.(map[string]any)
	switch kind {
	case "RunTagged":
		id, _ := f["id"].(int64)
		rk, rb, err := anthrex.SplitVariant(f["request"])
		if err != nil {
			return err
		}
		rf, _ := rb.(map[string]any)
		d.record(Request{Kind: "Run." + rk, ID: uint64(id), Body: rf, Raw: f["request"]})
		return d.runRequest(c, uint64(id), rk, rf)
	case "ListWindows":
		d.record(Request{Kind: kind, Raw: v})
		d.mu.Lock()
		w := d.windows
		d.mu.Unlock()
		return c.write(map[string]any{"WindowsChanged": map[string]any{"windows": w}})
	case "CreateWindow":
		d.record(Request{Kind: kind, Body: f, Raw: v})
		return c.write(map[string]any{"Created": map[string]any{"window_id": uint32(7)}})
	case "Subscribe":
		d.record(Request{Kind: kind, Body: f, Raw: v})
		wid := f["window_id"]
		if w, _ := wid.(int64); w == 404 {
			return c.write(map[string]any{"Error": map[string]any{"request": "subscribe", "message": "no window 404"}})
		}
		if err := c.write(map[string]any{"Snapshot": map[string]any{"window_id": wid, "cols": f["cols"], "rows": f["rows"], "bytes": []byte("\x1b[H\x1b[Jhello")}}); err != nil {
			return err
		}
		return c.write(map[string]any{"Output": map[string]any{"window_id": wid, "bytes": []byte(" world")}})
	case "Input":
		d.record(Request{Kind: kind, Body: f, Raw: v})
		if w, _ := f["window_id"].(int64); w == 404 {
			return c.write(map[string]any{"Error": map[string]any{"request": "input", "message": "no window 404"}})
		}
		return nil
	case "Kill", "Restart":
		d.record(Request{Kind: kind, Body: f, Raw: v})
		return c.write(map[string]any{"Ack": map[string]any{"request": map[string]string{"Kill": "kill", "Restart": "restart"}[kind]}})
	case "SubscribeConversation":
		d.record(Request{Kind: kind, Body: f, Raw: v})
		return c.write(map[string]any{"ConversationSnapshot": map[string]any{
			"window_id": f["window_id"], "agent_id": f["agent_id"],
			"conversation": map[string]any{
				"window_id": f["window_id"], "agent_id": f["agent_id"], "session_id": "s1", "runtime": "claude",
				"rev": uint64(3), "degraded": nil, "dropped_turns": uint32(0), "dropped_by": nil,
				"turns": []any{map[string]any{"id": uint64(1), "role": "user", "at_unix_secs": uint64(1_700_000_000), "state": "complete",
					"blocks": []any{map[string]any{"text": map[string]any{"text": "hello"}}}}},
			},
		}})
	default:
		d.record(Request{Kind: kind, Body: f, Raw: v})
	}
	return nil
}

func done(request, message string) any {
	return map[string]any{"Done": map[string]any{"request": request, "message": message}}
}

func (d *Daemon) runRequest(c *conn, id uint64, kind string, f map[string]any) error {
	switch kind {
	case "Subscribe":
		d.mu.Lock()
		d.subs[c] = true
		snap := d.snapshotLocked()
		d.mu.Unlock()
		return c.write(map[string]any{"Run": map[string]any{"Snapshot": snap}})
	case "Unsubscribe":
		d.mu.Lock()
		delete(d.subs, c)
		d.mu.Unlock()
		return nil
	case "List":
		d.mu.Lock()
		snap := d.snapshotLocked()
		d.mu.Unlock()
		return c.write(map[string]any{"Run": map[string]any{"Snapshot": snap}})
	}
	var reply any
	if d.Handler != nil {
		reply = d.Handler(d, kind, f)
	}
	if reply == nil {
		reply = defaultReply(kind, f)
	}
	// Stamp request_id like RunReply::tagged.
	rk, rb, err := anthrex.SplitVariant(reply)
	if err != nil {
		return err
	}
	if m, ok := rb.(map[string]any); ok {
		m["request_id"] = id
		reply = map[string]any{rk: m}
	}
	return c.write(map[string]any{"Run": reply})
}

func defaultReply(kind string, f map[string]any) any {
	switch kind {
	case "StartGoal":
		return map[string]any{"Started": map[string]any{"run_id": "run-fake1", "state": "planning"}}
	case "Approve":
		return done("run approve", "approved")
	case "Reject":
		return done("run reject", "rejected")
	case "ApproveHold":
		return done("run approve", "hold approved")
	case "RejectHold":
		return done("run reject", "hold rejected")
	case "Cancel":
		return done("run cancel", "cancelled")
	case "Resume":
		return done("run resume", "resumed")
	case "Edit":
		return done("run edit", "edit accepted")
	case "AnswerAsk":
		return done("run answer-ask", "answered")
	case "DocGate":
		return done("run doc gate", "gate answered")
	case "Finish":
		if f["confirm"] == nil {
			return map[string]any{"ConfirmNeeded": map[string]any{"run_id": f["run_id"], "prompt": "merge run into main?", "base_moved": nil}}
		}
		return done("run finish", "merged into main")
	case "TaskDetail":
		return map[string]any{"TaskDetail": map[string]any{"detail": map[string]any{"task_id": f["task_id"], "brief": "do it"}}}
	case "ShowDoc":
		return map[string]any{"Doc": map[string]any{"doc": map[string]any{"kind": f["kind"], "version": uint32(1), "text": "# Spec"}}}
	case "Stats":
		return map[string]any{"Stats": map[string]any{"stats": map[string]any{"runs": uint32(3)}}}
	case "Profile":
		return map[string]any{"Profile": map[string]any{"reply": map[string]any{"Status": map[string]any{"state": "confirmed"}}}}
	}
	return map[string]any{"Refused": map[string]any{"request": kind, "message": "not implemented by the fake"}}
}
