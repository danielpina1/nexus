package hubserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/danielpina1/nexus/backend/kernel"
)

// Options configure a Server.
type Options struct {
	Config Config
	// Mux is the mux passed to host.New; modules mount handlers on it.
	Mux    *http.ServeMux
	Logger *slog.Logger
	// Auth wraps module handlers when set (usually resolved from
	// kernel.SvcAuth after host Init; see AuthFrom).
	Auth kernel.Authenticator
	// TLS supplies the listener config for server.tls.auto (the security
	// module; see TLSFrom).
	TLS TLSSource
	// Version is reported by /healthz.
	Version string
	// ShutdownGrace is how long Shutdown waits for in-flight requests before
	// cancelling long-lived streams. Default 2s.
	ShutdownGrace time.Duration
}

// Server is the hub HTTP server (HTTP/1.1 + h2c, or TLS with h2).
type Server struct {
	opts    Options
	handler http.Handler
	srv     *http.Server

	baseCtx    context.Context
	cancelBase context.CancelFunc

	mu   sync.Mutex
	ln   net.Listener
	errc chan error
}

// AuthFrom returns the kernel.SvcAuth authenticator if a module provided it.
func AuthFrom(lookup func(string) (any, bool)) kernel.Authenticator {
	v, ok := lookup(kernel.SvcAuth)
	if !ok {
		return nil
	}
	a, _ := v.(kernel.Authenticator)
	return a
}

// TLSSource issues the hub's TLS config from its private CA. extraHosts are
// added to the certificate SANs (the listen IP). Implemented by
// *security.Security.
type TLSSource interface {
	HubTLSConfig(extraHosts []string) *tls.Config
}

// TLSFrom returns the kernel.SvcSecurity service as a TLSSource, if any.
func TLSFrom(lookup func(string) (any, bool)) TLSSource {
	v, ok := lookup(kernel.SvcSecurity)
	if !ok {
		return nil
	}
	t, _ := v.(TLSSource)
	return t
}

// New builds a server. Call Listen, then Serve.
func New(o Options) *Server {
	o.Config.applyDefaults()
	if o.Mux == nil {
		o.Mux = http.NewServeMux()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.ShutdownGrace <= 0 {
		o.ShutdownGrace = 2 * time.Second
	}
	s := &Server{opts: o, errc: make(chan error, 1)}
	s.baseCtx, s.cancelBase = context.WithCancel(context.Background())
	s.handler = s.build()
	s.srv = &http.Server{
		Handler:           h2c.NewHandler(s.handler, &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return s.baseCtx },
		ErrorLog:          slog.NewLogLogger(o.Logger.Handler(), slog.LevelWarn),
	}
	return s
}

// Handler returns the full middleware chain (without h2c), for tests.
func (s *Server) Handler() http.Handler { return s.handler }

// build assembles: recovery → logging → CORS → router.
func (s *Server) build() http.Handler {
	return withRecovery(s.opts.Logger, withLogging(s.opts.Logger, withCORS(s.opts.Config.CORS.AllowedOrigins, s.router())))
}

// router dispatches /healthz, module routes (behind auth) and the dashboard.
func (s *Server) router() http.Handler {
	api := http.Handler(s.opts.Mux)
	if a := s.opts.Auth; a != nil {
		authed := a.Middleware(s.opts.Mux)
		api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if a.Public(r.URL.Path) {
				s.opts.Mux.ServeHTTP(w, r)
				return
			}
			authed.ServeHTTP(w, r)
		})
	}
	web := placeholderHandler()
	if dir := s.opts.Config.WebDir; dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			s.opts.Logger.Warn("web dashboard not found; serving placeholder", "web_dir", dir, "err", err)
		} else {
			web = staticHandler(dir)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			s.health(w)
			return
		}
		if _, pattern := s.opts.Mux.Handler(r); pattern != "" {
			api.ServeHTTP(w, r)
			return
		}
		web.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": s.opts.Version})
}

// Listen binds the configured address (fail fast before modules start).
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.opts.Config.Listen)
	if err != nil {
		return fmt.Errorf("hubserver: listen %s: %w", s.opts.Config.Listen, err)
	}
	tc, err := s.tlsConfig()
	if err != nil {
		ln.Close()
		return err
	}
	if tc != nil {
		s.srv.TLSConfig = tc
		if err := http2.ConfigureServer(s.srv, &http2.Server{}); err != nil {
			ln.Close()
			return fmt.Errorf("hubserver: http2: %w", err)
		}
		ln = tls.NewListener(ln, s.srv.TLSConfig)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	return nil
}

// tlsConfig returns the listener TLS config, or nil for plain h2c.
func (s *Server) tlsConfig() (*tls.Config, error) {
	c := s.opts.Config.TLS
	switch {
	case c.Auto:
		if c.Cert != "" || c.Key != "" {
			return nil, errors.New("hubserver: server.tls.auto and server.tls.cert/key are mutually exclusive")
		}
		if s.opts.TLS == nil {
			return nil, errors.New("hubserver: server.tls.auto requires the security module")
		}
		tc := s.opts.TLS.HubTLSConfig(listenHosts(s.opts.Config.Listen)).Clone()
		tc.MinVersion = tls.VersionTLS13
		tc.NextProtos = []string{"h2", "http/1.1"}
		return tc, nil
	case c.Cert != "" || c.Key != "":
		cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
		if err != nil {
			return nil, fmt.Errorf("hubserver: tls: %w", err)
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"h2", "http/1.1"},
		}, nil
	}
	return nil, nil
}

// listenHosts returns the IP of a listen address for the certificate SANs
// (none for wildcard addresses such as ":7700" or "0.0.0.0:7700").
func listenHosts(listen string) []string {
	h, _, err := net.SplitHostPort(listen)
	if err != nil || h == "" {
		return nil
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsUnspecified() {
		return nil
	}
	return []string{h}
}

// Addr returns the bound address (after Listen).
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Serve accepts connections in the background. The returned channel receives
// the error that stopped the server (nil after a clean Shutdown).
func (s *Server) Serve() <-chan error {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		s.errc <- errors.New("hubserver: Serve called before Listen")
		return s.errc
	}
	scheme := "http"
	if s.srv.TLSConfig != nil {
		scheme = "https"
	}
	s.opts.Logger.Info("hub API listening", "addr", ln.Addr().String(), "scheme", scheme)
	go func() {
		err := s.srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.errc <- err
	}()
	return s.errc
}

// Shutdown stops accepting connections, waits up to ShutdownGrace for
// in-flight requests, then cancels the remaining ones (event streams, h2c
// connections) and waits for ctx.
func (s *Server) Shutdown(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- s.srv.Shutdown(ctx) }()
	select {
	case err := <-done:
		s.cancelBase()
		return err
	case <-time.After(s.opts.ShutdownGrace):
	case <-ctx.Done():
	}
	s.cancelBase() // ends streaming handlers and hijacked h2c connections
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return s.srv.Close()
	}
}
