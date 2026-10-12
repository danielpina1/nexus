package notify

import (
	"context"
	"crypto"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/notify/apns"
	"github.com/danielpina1/nexus/backend/internal/security"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Directory is what notify needs from the security module (*security.Security
// implements it): paired devices and Live Activity tokens.
type Directory interface {
	Devices(ctx context.Context) ([]*nexusv1.Device, error)
	LiveActivityTokens(ctx context.Context, kind nexusv1.LiveActivityTokenKind, activityType string) ([]security.LiveActivityToken, error)
}

// Optional security capabilities, detected by type assertion. Until the
// security module implements them, notify signs with its own key and keeps
// a local denylist of unregistered tokens.
type (
	// HubSigner exposes the hub identity signing key (envelope sender).
	HubSigner interface {
		HubSigner() crypto.Signer
	}
	// PushTokenDropper forgets a device push token after 410 Unregistered.
	PushTokenDropper interface {
		DropPushToken(ctx context.Context, deviceID, token string) error
	}
	// LiveActivityTokenDropper forgets an ActivityKit token after 410.
	LiveActivityTokenDropper interface {
		DropLiveActivityToken(ctx context.Context, deviceID string, token []byte) error
	}
)

// Pusher sends one APNs request (*apns.Client, or the dry-run logger).
type Pusher interface {
	Push(ctx context.Context, r apns.Request) (*apns.Response, error)
}

// Service delivers notifications and Live Activity pushes. It implements
// kernel.Notifier.
type Service struct {
	cfg     Config
	log     *slog.Logger
	dir     func() Directory
	pusher  Pusher
	now     func() time.Time
	encrypt bool

	sealMu     sync.Mutex
	sealer     *sealer
	signerFunc func() (crypto.Signer, error)

	deny *denylist
}

var _ kernel.Notifier = (*Service)(nil)

// Notify implements kernel.Notifier.
func (s *Service) Notify(ctx context.Context, n kernel.Notification) error {
	return s.Send(ctx, FromNotification(n))
}

func (s *Service) getSealer() (*sealer, error) {
	s.sealMu.Lock()
	defer s.sealMu.Unlock()
	if s.sealer != nil {
		return s.sealer, nil
	}
	sg, err := s.signerFunc()
	if err != nil {
		return nil, err
	}
	sl, err := newSealer(sg, s.now)
	if err != nil {
		return nil, err
	}
	s.sealer = sl
	return sl, nil
}

func pushable(d *nexusv1.Device) bool {
	if d.GetPushToken() == "" {
		return false
	}
	switch d.GetKind() {
	case nexusv1.DeviceKind_DEVICE_KIND_WEB, nexusv1.DeviceKind_DEVICE_KIND_CLI:
		return false
	}
	return true
}

// Send delivers m to its devices (all pushable devices when DeviceIDs is empty).
func (s *Service) Send(ctx context.Context, m Message) error {
	dir := s.dir()
	if dir == nil {
		return errors.New("notify: security module not available (no devices)")
	}
	devs, err := dir.Devices(ctx)
	if err != nil {
		return fmt.Errorf("notify: list devices: %w", err)
	}
	level := m.level()
	if level == LevelActive && s.cfg.QuietHours.Active(s.now()) {
		level = LevelPassive
	}
	prio := 10
	if level == LevelPassive {
		prio = 5
	}
	exp := m.Expiration
	if exp.IsZero() || exp.Before(s.now()) {
		exp = s.now().Add(24 * time.Hour)
	}
	var sl *sealer
	if s.encrypt {
		if sl, err = s.getSealer(); err != nil {
			s.log.Error("notify: envelope signer unavailable; sending generic alerts", "err", err)
		}
	}
	var errs []error
	sent := 0
	for _, d := range devs {
		if !pushable(d) || (len(m.DeviceIDs) > 0 && !slices.Contains(m.DeviceIDs, d.Id)) {
			continue
		}
		if s.deny.denied(d.PushToken, time.Time{}) {
			continue
		}
		if s.encrypt && len(d.EncryptionPublicKey) != 32 {
			s.log.Warn("notify: device has no encryption key; sending generic alert", "device", d.Id)
		}
		payload, err := buildAlert(m, level, s.encrypt, sl, d.EncryptionPublicKey)
		if err != nil {
			errs = append(errs, fmt.Errorf("device %s: %w", d.Id, err))
			continue
		}
		req := apns.Request{
			DeviceToken: d.PushToken,
			Environment: s.cfg.APNS.Environment,
			Topic:       s.cfg.APNS.BundleID,
			PushType:    apns.PushTypeAlert,
			Priority:    prio,
			CollapseID:  truncateID(m.CollapseID),
			Expiration:  exp,
			Payload:     payload,
		}
		resp, err := s.pusher.Push(ctx, req)
		if s.handle(ctx, "alert", d.Id, d.PushToken, nil, resp, err) {
			sent++
		} else if err != nil {
			errs = append(errs, err)
		}
	}
	s.log.Debug("notify: sent", "category", m.Category, "collapse", m.CollapseID, "devices", sent)
	return errors.Join(errs...)
}

// truncateID keeps apns-collapse-id within Apple's 64-byte limit.
func truncateID(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

// pushLiveActivity sends a liveactivity push to one ActivityKit token.
func (s *Service) pushLiveActivity(ctx context.Context, t security.LiveActivityToken, payload []byte, prio int, exp time.Time) bool {
	tok := hex.EncodeToString(t.Token)
	if s.deny.denied(tok, t.UpdatedAt) {
		return false
	}
	env := t.Environment
	if env == "" {
		env = s.cfg.APNS.Environment
	}
	resp, err := s.pusher.Push(ctx, apns.Request{
		DeviceToken: tok,
		Environment: env,
		Topic:       s.cfg.APNS.BundleID + apns.LiveActivityTopicSuffix,
		PushType:    apns.PushTypeLiveActivity,
		Priority:    prio,
		Expiration:  exp,
		Payload:     payload,
	})
	return s.handle(ctx, "liveactivity", t.DeviceID, tok, t.Token, resp, err)
}

// handle logs a push outcome and drops unregistered tokens. raw is the
// ActivityKit token bytes (nil for device push tokens).
func (s *Service) handle(ctx context.Context, kind, deviceID, token string, raw []byte, resp *apns.Response, err error) bool {
	short := token
	if len(short) > 8 {
		short = short[:8] + "…"
	}
	switch {
	case err != nil:
		s.log.Warn("notify: push failed", "kind", kind, "device", deviceID, "token", short, "err", err)
		return false
	case resp.OK():
		return true
	case resp.Unregistered():
		s.log.Info("notify: token unregistered; dropping", "kind", kind, "device", deviceID, "token", short)
		s.drop(ctx, deviceID, token, raw)
		return false
	default:
		s.log.Warn("notify: push rejected", "kind", kind, "device", deviceID, "token", short,
			"status", resp.StatusCode, "reason", resp.Reason)
		return false
	}
}

func (s *Service) drop(ctx context.Context, deviceID, token string, raw []byte) {
	if dir := s.dir(); dir != nil {
		if raw != nil {
			if d, ok := dir.(LiveActivityTokenDropper); ok {
				if err := d.DropLiveActivityToken(ctx, deviceID, raw); err == nil {
					return
				}
			}
		} else if d, ok := dir.(PushTokenDropper); ok {
			if err := d.DropPushToken(ctx, deviceID, token); err == nil {
				return
			}
		}
	}
	s.deny.add(token, s.now())
}

// ---------- dry run ----------

type dryRun struct{ log *slog.Logger }

func (d dryRun) Push(_ context.Context, r apns.Request) (*apns.Response, error) {
	d.log.Info("notify (dry run): would push", "type", r.PushType, "topic", r.Topic,
		"priority", r.Priority, "collapse", r.CollapseID, "bytes", len(r.Payload))
	d.log.Debug("notify (dry run): payload", "payload", string(r.Payload))
	return &apns.Response{StatusCode: 200}, nil
}

// ---------- denylist ----------

// denylist remembers tokens APNs reported as unregistered (persisted as
// JSON) when security cannot drop them itself.
type denylist struct {
	path string
	mu   sync.Mutex
	m    map[string]time.Time
}

func loadDenylist(path string) *denylist {
	d := &denylist{path: path, m: map[string]time.Time{}}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &d.m)
		}
	}
	return d
}

// denied reports whether token was unregistered at or after registeredAt
// (a token re-registered later is allowed again).
func (d *denylist) denied(token string, registeredAt time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	at, ok := d.m[token]
	return ok && !registeredAt.After(at)
}

func (d *denylist) add(token string, at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.m[token] = at
	if d.path == "" {
		return
	}
	b, _ := json.Marshal(d.m)
	tmp := d.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, d.path)
	}
}

func denylistPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "unregistered_tokens.json")
}
