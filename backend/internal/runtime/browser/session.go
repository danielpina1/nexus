package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// sessionConfig configures one browser session (one Chrome process).
type sessionConfig struct {
	ChromePath    string
	Headless      bool
	NoSandbox     bool
	ProfileDir    string // user-data-dir; persistent logins
	Width, Height int
	Domains       DomainPolicy
	LaunchTimeout time.Duration
	NavTimeout    time.Duration
	ActionTimeout time.Duration
	Log           *slog.Logger
	// Lock serialises use of ProfileDir across runs (Chrome refuses two
	// processes on one profile). Returns a release func.
	Lock func(ctx context.Context) (release func(), err error)
}

// session is a lazily-started Chrome with one tab.
type session struct {
	cfg sessionConfig

	mu sync.Mutex // serialises actions (and the launch)

	lc          sync.Mutex // lifecycle fields below
	started     bool
	closed      bool
	allocCancel context.CancelFunc
	tabCtx      context.Context
	tabCancel   context.CancelFunc
	release     func()
	kill        chan struct{} // closed when shutdown starts
	done        chan struct{} // closed when Chrome is gone and the profile released

	evMu    sync.Mutex
	dialogs []string
	blocked []string
}

func newSession(cfg sessionConfig) *session {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &session{cfg: cfg, kill: make(chan struct{}), done: make(chan struct{})}
}

var errClosed = errors.New("browser session closed")

// tab returns the tab context, starting Chrome on first use. Caller holds s.mu.
func (s *session) tab(ctx context.Context) (context.Context, error) {
	s.lc.Lock()
	closed, started, tabCtx := s.closed, s.started, s.tabCtx
	s.lc.Unlock()
	if closed {
		return nil, errClosed
	}
	if started {
		return tabCtx, nil
	}
	if s.cfg.ChromePath == "" {
		return nil, errors.New("no Chrome/Chromium found: install Chrome or set runtime.browser.chrome_path (or CHROME_PATH)")
	}
	var release func()
	if s.cfg.Lock != nil {
		lctx, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-s.kill:
				cancel()
			case <-lctx.Done():
			}
		}()
		rel, err := s.cfg.Lock(lctx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("browser profile %s is in use by another run: %w", s.cfg.ProfileDir, err)
		}
		release = rel
	}
	if err := os.MkdirAll(s.cfg.ProfileDir, 0o700); err != nil {
		if release != nil {
			release()
		}
		return nil, fmt.Errorf("browser profile: %w", err)
	}
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(s.cfg.ChromePath),
		chromedp.UserDataDir(s.cfg.ProfileDir),
		chromedp.WindowSize(s.cfg.Width, s.cfg.Height),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-breakpad", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-features", "Translate"),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("use-mock-keychain", true),
		chromedp.Flag("lang", "pt-PT,pt,en"),
		chromedp.WSURLReadTimeout(s.cfg.LaunchTimeout),
	}
	if s.cfg.Headless {
		opts = append(opts, chromedp.Headless)
	}
	if s.cfg.NoSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	tabCtx, tabCancel := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(f string, a ...any) { s.cfg.Log.Debug(fmt.Sprintf(f, a...)) }),
		chromedp.WithErrorf(func(f string, a ...any) { s.cfg.Log.Debug("cdp: " + fmt.Sprintf(f, a...)) }),
	)
	// The first Run allocates the browser; it must not carry a timeout (that
	// would bind the browser's lifetime to it), so time it out from outside.
	errc := make(chan error, 1)
	go func() { errc <- chromedp.Run(tabCtx, s.setupActions(tabCtx)...) }()
	var err error
	select {
	case err = <-errc:
	case <-time.After(s.cfg.LaunchTimeout):
		err = fmt.Errorf("chrome did not start within %s", s.cfg.LaunchTimeout)
	case <-ctx.Done():
		err = ctx.Err()
	case <-s.kill:
		err = errClosed
	}
	s.lc.Lock()
	if err == nil && s.closed {
		err = errClosed
	}
	if err == nil {
		s.started, s.tabCtx, s.tabCancel, s.allocCancel, s.release = true, tabCtx, tabCancel, allocCancel, release
	}
	s.lc.Unlock()
	if err != nil {
		tabCancel()
		allocCancel()
		if release != nil {
			release()
		}
		return nil, fmt.Errorf("start chrome (%s): %w", s.cfg.ChromePath, err)
	}
	s.cfg.Log.Info("browser started", "chrome", s.cfg.ChromePath, "profile", s.cfg.ProfileDir, "headless", s.cfg.Headless)
	return tabCtx, nil
}

// setupActions installs dialog handling and, when the agent has an
// allow-list, blocks main-frame navigations to other domains.
func (s *session) setupActions(tabCtx context.Context) []chromedp.Action {
	chromedp.ListenTarget(tabCtx, func(ev any) {
		switch e := ev.(type) {
		case *page.EventJavascriptDialogOpening:
			accept := e.Type == page.DialogTypeAlert || e.Type == page.DialogTypeBeforeunload
			s.note(&s.dialogs, fmt.Sprintf("%s dialog %q was %s", e.Type, oneLine(e.Message, 200), map[bool]string{true: "accepted", false: "dismissed"}[accept]))
			go func() {
				c := chromedp.FromContext(tabCtx)
				if c == nil || c.Target == nil {
					return
				}
				_ = page.HandleJavaScriptDialog(accept).Do(cdp.WithExecutor(tabCtx, c.Target))
			}()
		case *fetch.EventRequestPaused:
			go s.onRequest(tabCtx, e)
		}
	})
	acts := []chromedp.Action{
		chromedp.EmulateViewport(int64(s.cfg.Width), int64(s.cfg.Height)),
	}
	if s.cfg.Domains.Restricted() {
		acts = append(acts, fetch.Enable().WithPatterns([]*fetch.RequestPattern{{
			URLPattern: "*", ResourceType: network.ResourceTypeDocument, RequestStage: fetch.RequestStageRequest,
		}}))
	}
	return acts
}

func (s *session) onRequest(tabCtx context.Context, e *fetch.EventRequestPaused) {
	c := chromedp.FromContext(tabCtx)
	if c == nil || c.Target == nil {
		return
	}
	ectx := cdp.WithExecutor(tabCtx, c.Target)
	main := string(e.FrameID) == string(c.Target.TargetID)
	if main && e.Request != nil {
		u, err := url.Parse(e.Request.URL)
		allowed := err == nil && (u.Scheme == "about" || u.Scheme == "data" || u.Scheme == "blob" || s.cfg.Domains.AllowHost(u.Host))
		if !allowed {
			s.note(&s.blocked, e.Request.URL)
			s.cfg.Log.Warn("browser: blocked navigation outside allowed domains", "url", e.Request.URL)
			_ = fetch.FailRequest(e.RequestID, network.ErrorReasonBlockedByClient).Do(ectx)
			return
		}
	}
	_ = fetch.ContinueRequest(e.RequestID).Do(ectx)
}

func (s *session) note(list *[]string, msg string) {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	if len(*list) < 20 {
		*list = append(*list, msg)
	}
}

// drainNotices returns and clears dialog/blocked-navigation notices.
func (s *session) drainNotices() (dialogs, blocked []string) {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	dialogs, blocked = s.dialogs, s.blocked
	s.dialogs, s.blocked = nil, nil
	return
}

// do runs actions on the tab with a timeout, aborting when ctx is done.
func (s *session) do(ctx context.Context, timeout time.Duration, actions ...chromedp.Action) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doLocked(ctx, timeout, actions...)
}

func (s *session) doLocked(ctx context.Context, timeout time.Duration, actions ...chromedp.Action) error {
	tab, err := s.tab(ctx)
	if err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = s.cfg.ActionTimeout
	}
	actx, cancel := context.WithTimeout(tab, timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	err = chromedp.Run(actx, actions...)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && errors.Is(actx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("browser action timed out after %s: %w", timeout, err)
	}
	return err
}

// eval evaluates a JS expression returning a string.
func (s *session) evalLocked(ctx context.Context, expr string) (string, error) {
	var out string
	err := s.doLocked(ctx, 0, chromedp.Evaluate(expr, &out))
	return out, err
}

// settleLocked waits for the document to finish loading after an action
// that may navigate (best effort; bounded by NavTimeout).
func (s *session) settleLocked(ctx context.Context) {
	deadline := time.Now().Add(s.cfg.NavTimeout)
	time.Sleep(150 * time.Millisecond)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		var state string
		err := s.doLocked(ctx, 2*time.Second, chromedp.Evaluate(`document.readyState`, &state))
		if err == nil && state == "complete" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// location returns the current URL and title.
func (s *session) locationLocked(ctx context.Context) (u, title string) {
	_ = s.doLocked(ctx, 3*time.Second, chromedp.Location(&u), chromedp.Title(&title))
	return u, title
}

// Close shuts Chrome down gracefully (so the profile's cookies are flushed),
// falling back to killing it, and releases the profile. Safe to call
// repeatedly and concurrently with an in-flight action (which then fails).
func (s *session) Close() { s.shutdown(true) }

// Kill terminates Chrome immediately (run cancelled).
func (s *session) Kill() { s.shutdown(false) }

func (s *session) shutdown(graceful bool) {
	s.lc.Lock()
	if s.closed {
		s.lc.Unlock()
		return
	}
	s.closed = true
	close(s.kill)
	started, tabCtx, tabCancel, allocCancel, release := s.started, s.tabCtx, s.tabCancel, s.allocCancel, s.release
	s.release = nil
	s.lc.Unlock()
	if started {
		if graceful {
			done := make(chan struct{})
			go func() {
				_ = chromedp.Cancel(tabCtx)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
		tabCancel()
		allocCancel() // kills the process if still alive and waits for it
		s.cfg.Log.Info("browser stopped", "profile", s.cfg.ProfileDir, "graceful", graceful)
	}
	if release != nil {
		release()
	}
	close(s.done)
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
