package mcp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// MaxMessageBytes bounds one HTTP request body / stdio line. Claude Code's
// permission prompts carry whole file contents (Write), hence the headroom.
const MaxMessageBytes = 32 << 20

// ---- stdio ----

// ServeStdio reads newline-delimited JSON-RPC messages from r and writes
// responses to w (one per line) until r reaches EOF or ctx is done. Requests
// are handled concurrently (a tools/call may block on an approval); writes
// are serialised. It waits for in-flight requests before returning.
func (s *Server) ServeStdio(ctx context.Context, r io.Reader, w io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wmu sync.Mutex
		wg  sync.WaitGroup
	)
	write := func(b []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		_, _ = w.Write(append(b, '\n'))
	}
	lines := make(chan []byte)
	errc := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), MaxMessageBytes)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
		errc <- sc.Err()
		close(lines)
	}()
	var err error
loop:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				err = <-errc
				break loop
			}
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if resp := s.Handle(ctx, line); resp != nil {
					write(resp)
				}
			}()
		case <-ctx.Done():
			err = ctx.Err()
			break loop
		}
	}
	wg.Wait()
	return err
}

// ---- Streamable HTTP ----

// HTTPHandler serves MCP over Streamable HTTP at whatever path it is mounted:
// POST a JSON-RPC message (or batch) → 200 application/json with the
// response, or 202 Accepted for notifications/responses. GET/DELETE → 405
// (no server-initiated stream, no session termination). Requests carrying a
// non-local Origin are refused (DNS-rebinding protection).
func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if o := req.Header.Get("Origin"); o != "" && !localOrigin(o) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, MaxMessageBytes+1))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body) > MaxMessageBytes {
			http.Error(w, "message too large", http.StatusRequestEntityTooLarge)
			return
		}
		if IsNotification(body) {
			s.Handle(req.Context(), body)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		resp := s.Handle(req.Context(), body)
		if isInitialize(body) && req.Header.Get("Mcp-Session-Id") == "" {
			w.Header().Set("Mcp-Session-Id", randomHex(16))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp)
	})
}

func isInitialize(body []byte) bool {
	var m rpcMessage
	return json.Unmarshal(body, &m) == nil && m.Method == "initialize"
}

func localOrigin(o string) bool {
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Endpoint is a loopback HTTP MCP endpoint with an unguessable path:
// http://127.0.0.1:<port>/mcp/<token>. Knowing the URL is the credential
// (the token is 256 random bits; only processes the node hands the URL to —
// the run's CLI via a 0600 config file in the run workspace — can call it).
type Endpoint struct {
	srv   *http.Server
	ln    net.Listener
	url   string
	token string
	done  chan struct{}
}

// Listen serves s on a fresh 127.0.0.1 port.
func Listen(s *Server) (*Endpoint, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	e := &Endpoint{ln: ln, token: randomHex(32), done: make(chan struct{})}
	path := "/mcp/" + e.token
	e.url = "http://" + ln.Addr().String() + path
	inner := s.HTTPHandler()
	e.srv = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.URL.Path), []byte(path)) != 1 {
				http.NotFound(w, r)
				return
			}
			inner.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: tools/call may wait for a human approval.
	}
	go func() {
		defer close(e.done)
		if err := e.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log().Warn("mcp: http endpoint stopped", "err", err)
		}
	}()
	return e, nil
}

// URL is the endpoint URL including the secret path token.
func (e *Endpoint) URL() string { return e.url }

// Close stops the endpoint, giving in-flight requests a moment to finish.
func (e *Endpoint) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := e.srv.Shutdown(ctx)
	if err != nil {
		err = e.srv.Close()
	}
	<-e.done
	return err
}
