// Package notify is the hub "notify" module: APNs delivery of end-to-end
// encrypted notifications and Live Activity pushes (IOS_LIVE.md §2–3,
// SECURITY.md §4).
//
//   - Provides kernel.SvcNotifier (*Service) for ad-hoc notifications.
//   - Subscribes to the event bus (live, from LastSeq) and maps events to
//     notifications (Composer) and Live Activity updates (LiveManager).
//   - Without APNs credentials it runs in dry-run mode (logs only).
//
// See Config for the nexus.yaml section.
package notify

import (
	"context"
	"crypto"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/notify/apns"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Module is the notify module.
type Module struct {
	host kernel.Host
	log  *slog.Logger
	cfg  Config
	svc  *Service
	comp *Composer
	live *LiveManager

	queue  chan Message
	cancel context.CancelFunc
	wg     sync.WaitGroup

	agentMu sync.Mutex
	agents  map[string]agentEntry
}

type agentEntry struct {
	info AgentInfo
	at   time.Time
}

// Info implements kernel.Module.
func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "notify",
		Description:    "APNs notifications (end-to-end encrypted) and Live Activities",
		Side:           kernel.SideHub,
		Requires:       []string{"events"},
		DefaultEnabled: true,
	}
}

// Init implements kernel.Module.
func (m *Module) Init(_ context.Context, h kernel.Host) error {
	m.host, m.log = h, h.Logger()
	if err := h.Config(&m.cfg); err != nil {
		return fmt.Errorf("notify: config: %w", err)
	}
	if err := m.cfg.defaults(); err != nil {
		return err
	}
	var pusher Pusher
	if m.cfg.APNS.credentialsConfigured() {
		ts, err := m.cfg.APNS.tokenSource()
		if err != nil {
			return fmt.Errorf("notify: apns key: %w", err)
		}
		if m.cfg.APNS.BundleID == "" {
			return fmt.Errorf("notify: apns.bundle_id is required")
		}
		c, err := apns.New(apns.Config{Tokens: ts, Environment: m.cfg.APNS.Environment, BaseURL: m.cfg.APNS.BaseURL})
		if err != nil {
			return err
		}
		pusher = c
	} else {
		m.log.Info("notify: no APNs credentials configured; dry-run mode (pushes are only logged)")
		pusher = dryRun{log: m.log}
	}
	if !m.cfg.encrypt() {
		m.log.Warn("notify: encrypt=false — notification content is sent in plaintext through Apple (DEV ONLY)")
	}
	m.svc = newService(m.cfg, m.log, pusher, m.directory, m.signer, denylistPath(h.DataDir()))
	m.comp = &Composer{OfflineAfter: m.cfg.NodeOfflineAfter, AgentName: func(ctx context.Context, id string) string {
		return m.agentInfo(ctx, id).Name
	}}
	m.live = newLiveManager(m.cfg, m.log, m.directory, m.svc.pushLiveActivity)
	m.live.agentInfo = m.agentInfo
	m.live.getTask = m.getTask
	h.Provide(kernel.SvcNotifier, kernel.Notifier(m.svc))
	return nil
}

func newService(cfg Config, log *slog.Logger, p Pusher, dir func() Directory, signer func() (crypto.Signer, error), denyPath string) *Service {
	return &Service{
		cfg: cfg, log: log, dir: dir, pusher: p, now: time.Now,
		encrypt: cfg.encrypt(), signerFunc: signer, deny: loadDenylist(denyPath),
	}
}

func newLiveManager(cfg Config, log *slog.Logger, dir func() Directory, send liveSender) *LiveManager {
	return &LiveManager{
		cfg: cfg.LiveActivity, hubName: cfg.HubName, quiet: cfg.QuietHours,
		now: time.Now, log: log, dir: dir, send: send,
	}
}

// directory resolves the security module lazily.
func (m *Module) directory() Directory {
	v, ok := m.host.Lookup(kernel.SvcSecurity)
	if !ok {
		return nil
	}
	d, _ := v.(Directory)
	return d
}

// signer returns the envelope signing key: the hub identity key when
// security exposes it (HubSigner), else a notify-owned P-256 key
// (<data>/envelope_signing.key).
func (m *Module) signer() (crypto.Signer, error) {
	if v, ok := m.host.Lookup(kernel.SvcSecurity); ok {
		if hs, ok := v.(HubSigner); ok {
			if s := hs.HubSigner(); s != nil {
				return s, nil
			}
		}
	}
	m.log.Warn("notify: security exposes no HubSigner(); signing envelopes with a notify-owned key")
	return keys.LoadOrCreatePrivateKey(filepath.Join(m.host.DataDir(), "envelope_signing.key"))
}

func sfSymbol(icon string) string {
	if icon == "" {
		return "sparkles"
	}
	for _, r := range icon {
		if r > 127 || !(r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return "sparkles"
		}
	}
	return icon
}

// agentInfo looks up an agent's name and SF Symbol (cached 5 min).
func (m *Module) agentInfo(ctx context.Context, id string) AgentInfo {
	if id == "" {
		return AgentInfo{}
	}
	m.agentMu.Lock()
	if e, ok := m.agents[id]; ok && time.Since(e.at) < 5*time.Minute {
		m.agentMu.Unlock()
		return e.info
	}
	m.agentMu.Unlock()
	info := AgentInfo{Name: id, Symbol: "sparkles"}
	if cat, err := kernel.Resolve[kernel.AgentCatalog](m.host, kernel.SvcAgents); err == nil {
		if a, _, _, err := cat.GetAgent(ctx, id); err == nil && a != nil {
			if a.Name != "" {
				info.Name = a.Name
			}
			info.Symbol = sfSymbol(a.Icon)
		}
	}
	m.agentMu.Lock()
	if m.agents == nil || len(m.agents) > 1000 {
		m.agents = map[string]agentEntry{}
	}
	m.agents[id] = agentEntry{info: info, at: time.Now()}
	m.agentMu.Unlock()
	return info
}

func (m *Module) getTask(ctx context.Context, id string) *nexusv1.Task {
	te, err := kernel.Resolve[kernel.TaskEngine](m.host, kernel.SvcTasks)
	if err != nil {
		return nil
	}
	t, err := te.GetTask(ctx, id)
	if err != nil {
		return nil
	}
	return t
}

// Start subscribes to the event bus.
func (m *Module) Start(ctx context.Context) error {
	bus, err := kernel.Resolve[kernel.EventBus](m.host, kernel.SvcEventBus)
	if err != nil {
		return err
	}
	since, err := bus.LastSeq(ctx)
	if err != nil {
		return fmt.Errorf("notify: last seq: %w", err)
	}
	rctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.queue = make(chan Message, 256)
	m.wg.Add(2)
	go func() { defer m.wg.Done(); m.deliver(rctx) }()
	go func() { defer m.wg.Done(); m.run(rctx, bus, since) }()
	return nil
}

// Stop implements kernel.Module.
func (m *Module) Stop(context.Context) error {
	if m.cancel != nil {
		m.cancel()
		m.wg.Wait()
	}
	return nil
}

func (m *Module) liveEnabled() bool { return boolOr(m.cfg.LiveActivity.Enabled, true) }

// run is the event loop: compose notifications, drive Live Activities,
// resubscribe from the last seen seq when the bus drops us.
func (m *Module) run(ctx context.Context, bus kernel.EventBus, since int64) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		ch, err := bus.Subscribe(ctx, since, kernel.EventFilter{})
		if err != nil {
			m.log.Warn("notify: subscribe", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
	loop:
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					break loop
				}
				since = ev.GetSeq()
				m.handle(ctx, ev)
			case <-tick.C:
				m.tick(ctx)
			}
		}
	}
}

func (m *Module) handle(ctx context.Context, ev *nexusv1.Event) {
	for _, msg := range m.comp.Compose(ctx, ev) {
		m.enqueue(msg)
	}
	if m.liveEnabled() {
		m.live.Handle(ctx, ev)
	}
}

func (m *Module) tick(ctx context.Context) {
	for _, msg := range m.comp.Tick() {
		m.enqueue(msg)
	}
	if m.liveEnabled() {
		m.live.Flush(ctx)
	}
}

func (m *Module) enqueue(msg Message) {
	select {
	case m.queue <- msg:
	default:
		m.log.Warn("notify: delivery queue full; dropping notification", "category", msg.Category, "collapse", msg.CollapseID)
	}
}

func (m *Module) deliver(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-m.queue:
			sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := m.svc.Send(sctx, msg); err != nil {
				m.log.Warn("notify: delivery", "category", msg.Category, "err", err)
			}
			cancel()
		}
	}
}
