package triggers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/danielpina1/nexus/backend/kernel"
)

// HookPathPrefix is where webhooks are mounted: POST /hooks/{id}.
const HookPathPrefix = "/hooks/"

// SignatureHeader carries "sha256=<hex HMAC-SHA256(secret, body)>".
const SignatureHeader = "X-Nexus-Signature"

// HookSpec declares a webhook. Firing.Key is ID.
type HookSpec struct {
	ID string `yaml:"id" json:"id"`
	// Secret is the shared secret (prefer SecretEnv).
	Secret string `yaml:"secret" json:"-"`
	// SecretEnv names an environment variable holding the secret.
	SecretEnv string `yaml:"secret_env" json:"secret_env,omitempty"`
}

func (h HookSpec) secret() string {
	if h.Secret != "" {
		return h.Secret
	}
	if h.SecretEnv != "" {
		return os.Getenv(h.SecretEnv)
	}
	return ""
}

type hookEntry struct {
	HookSpec
	owner string
}

// WebhookSource is the "webhook" trigger source and the http.Handler for
// POST /hooks/{id}.
//
// Authentication (one of):
//   - X-Nexus-Signature: sha256=<hex HMAC-SHA256(secret, raw body)>
//   - X-Hub-Signature-256: sha256=<hex> (GitHub style, same computation)
//   - Authorization: Bearer <secret>
//
// The body becomes Firing.Payload: a JSON object as is, any other JSON value
// as {"body": value}, anything else as {"body": "<text>"}. Firing.Meta holds
// the content type and a few well-known event headers.
type WebhookSource struct {
	log     *slog.Logger
	maxBody int64

	mu    sync.RWMutex
	hooks map[string][]hookEntry // id → entries (several owners may declare the same id)
	emit  func(kernel.Firing)
}

var (
	_ kernel.TriggerSource = (*WebhookSource)(nil)
	_ HookRegistrar        = (*WebhookSource)(nil)
	_ http.Handler         = (*WebhookSource)(nil)
)

// NewWebhookSource returns the webhook source (maxBody <= 0 = 1 MiB).
func NewWebhookSource(maxBody int64, log *slog.Logger) *WebhookSource {
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	if log == nil {
		log = slog.Default()
	}
	return &WebhookSource{log: log, maxBody: maxBody, hooks: map[string][]hookEntry{}}
}

func (w *WebhookSource) Name() string { return SourceWebhook }

// SetHooks replaces all hooks of owner.
func (w *WebhookSource) SetHooks(owner string, hooks []HookSpec) error {
	var errs []error
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, es := range w.hooks {
		kept := es[:0:0]
		for _, e := range es {
			if e.owner != owner {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(w.hooks, id)
		} else {
			w.hooks[id] = kept
		}
	}
	for _, h := range hooks {
		if h.ID == "" || strings.ContainsAny(h.ID, "/?#") {
			errs = append(errs, fmt.Errorf("webhook id %q is invalid", h.ID))
			continue
		}
		w.hooks[h.ID] = append(w.hooks[h.ID], hookEntry{HookSpec: h, owner: owner})
	}
	return errors.Join(errs...)
}

// Run makes the source live until ctx ends.
func (w *WebhookSource) Run(ctx context.Context, emit func(kernel.Firing)) error {
	w.mu.Lock()
	w.emit = emit
	w.mu.Unlock()
	<-ctx.Done()
	w.mu.Lock()
	w.emit = nil
	w.mu.Unlock()
	return nil
}

// Sign returns the X-Nexus-Signature value for body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// VerifySignature checks a "sha256=<hex>" signature in constant time.
func VerifySignature(secret string, body []byte, sig string) bool {
	if secret == "" {
		return false
	}
	hexSig, ok := strings.CutPrefix(strings.TrimSpace(sig), "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}

// authorized reports whether the request carries a valid signature or
// bearer secret for one of the secrets.
func authorized(r *http.Request, body []byte, secrets []string) bool {
	sig := r.Header.Get(SignatureHeader)
	if sig == "" {
		sig = r.Header.Get("X-Hub-Signature-256")
	}
	bearer, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if sig != "" && VerifySignature(s, body, sig) {
			return true
		}
		if hasBearer && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(bearer)), []byte(s)) == 1 {
			return true
		}
	}
	return false
}

var metaHeaders = []string{"X-GitHub-Event", "X-GitHub-Delivery", "X-Gitlab-Event", "X-Event-Type", "X-Request-Id", "User-Agent"}

func (w *WebhookSource) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = strings.TrimPrefix(r.URL.Path, HookPathPrefix)
	}
	if r.Method != http.MethodPost {
		writeJSON(rw, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		return
	}
	w.mu.RLock()
	entries := w.hooks[id]
	emit := w.emit
	w.mu.RUnlock()
	if len(entries) == 0 {
		writeJSON(rw, http.StatusNotFound, map[string]any{"error": "unknown hook"})
		return
	}
	var secrets []string
	for _, e := range entries {
		if s := e.secret(); s != "" {
			secrets = append(secrets, s)
		}
	}
	if len(secrets) == 0 {
		w.log.Warn("webhook has no secret configured; rejecting", "hook", id)
		writeJSON(rw, http.StatusForbidden, map[string]any{"error": "hook has no secret configured"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, w.maxBody))
	if err != nil {
		writeJSON(rw, http.StatusRequestEntityTooLarge, map[string]any{"error": "body too large"})
		return
	}
	if !authorized(r, body, secrets) {
		w.log.Warn("webhook signature rejected", "hook", id, "remote", r.RemoteAddr)
		writeJSON(rw, http.StatusUnauthorized, map[string]any{"error": "bad signature"})
		return
	}
	if emit == nil {
		writeJSON(rw, http.StatusServiceUnavailable, map[string]any{"error": "trigger engine not running"})
		return
	}
	meta := map[string]string{"content_type": r.Header.Get("Content-Type"), "received_at": time.Now().UTC().Format(time.RFC3339)}
	for _, h := range metaHeaders {
		if v := r.Header.Get(h); v != "" {
			meta[strings.ToLower(strings.ReplaceAll(h, "-", "_"))] = v
		}
	}
	emit(kernel.Firing{Source: SourceWebhook, Key: id, Payload: decodeBody(body), Meta: meta})
	writeJSON(rw, http.StatusAccepted, map[string]any{"accepted": true, "hook": id})
}

func decodeBody(body []byte) map[string]any {
	if len(strings.TrimSpace(string(body))) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return map[string]any{"body": string(body)}
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{"body": v}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
