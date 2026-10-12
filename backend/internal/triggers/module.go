// Package triggers provides the hub's trigger sources (kernel.TriggerSource)
// that start workflows: cron, interval, webhook and event.
//
// Module "triggers" (hub, default enabled). It registers four sources in
// h.Triggers() and mounts POST /hooks/{id} for the webhook source. It does
// not run the sources itself: the consumer (the workflow module) calls
// TriggerSource.Run for every registered source at Start and maps firings
// to workflow runs.
//
// Schedules and hooks come from two places:
//   - static entries in this module's config (owner "config"), and
//   - dynamic entries pushed by other modules through the Set* methods of
//     the sources (the workflow module pushes the `triggers:` of every
//     workflow note, owner "workflow"). Use the Registrar interfaces to find
//     them in h.Triggers().All().
//
// Config (nexus.yaml):
//
//	triggers:
//	  cron:
//	    - { id: nightly, expr: "0 3 * * *", tz: Europe/Lisbon }
//	  intervals:
//	    - { id: poll-prices, every: 30m }
//	  webhooks:
//	    - { id: github, secret_env: NEXUS_HOOK_GITHUB }   # or secret: "..."
//	  webhook_max_body: 1048576
//
// Webhooks must be reachable without the API token: the hub auth middleware
// only lets them through when "/hooks/" is a public path (security
// public_paths). Each hook authenticates itself with its own secret (HMAC
// signature or bearer secret), and hooks without a secret are rejected.
package triggers

import (
	"context"
	"fmt"
	"time"

	_ "time/tzdata" // per-trigger time zones on hosts without zoneinfo

	"github.com/danielpina1/nexus/backend/kernel"
)

func init() { kernel.RegisterModule(func() kernel.Module { return &Module{} }) }

// Source names.
const (
	SourceCron     = "cron"
	SourceInterval = "interval"
	SourceWebhook  = "webhook"
	SourceEvent    = "event"
)

// OwnerConfig is the owner of entries declared in this module's config.
const OwnerConfig = "config"

// Config is the "triggers" config section.
type Config struct {
	Cron      []CronSpec     `yaml:"cron"`
	Intervals []intervalConf `yaml:"intervals"`
	Webhooks  []HookSpec     `yaml:"webhooks"`
	// WebhookMaxBody limits webhook request bodies (bytes, default 1 MiB).
	WebhookMaxBody int64 `yaml:"webhook_max_body"`
}

type intervalConf struct {
	ID    string `yaml:"id"`
	Every string `yaml:"every"`
}

// Module is the triggers module.
type Module struct {
	kernel.BaseModule
	Cron     *ScheduleSource
	Interval *ScheduleSource
	Webhook  *WebhookSource
	Event    *EventSource
}

func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "triggers",
		Description:    "Trigger sources for workflows: cron, interval, webhook (/hooks/{id}) and event-bus events",
		Side:           kernel.SideHub,
		Requires:       []string{"events"},
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("triggers: config: %w", err)
	}
	log := h.Logger()
	m.Cron = NewCronSource(nil, log)
	m.Interval = NewIntervalSource(nil, log)
	m.Webhook = NewWebhookSource(cfg.WebhookMaxBody, log)
	m.Event = NewEventSource(func() (kernel.EventBus, error) {
		return kernel.Resolve[kernel.EventBus](h, kernel.SvcEventBus)
	}, log)

	if len(cfg.Cron) > 0 {
		if err := m.Cron.SetCron(OwnerConfig, cfg.Cron); err != nil {
			return fmt.Errorf("triggers: %w", err)
		}
	}
	if len(cfg.Intervals) > 0 {
		var iv []IntervalSpec
		for _, c := range cfg.Intervals {
			d, err := time.ParseDuration(c.Every)
			if err != nil {
				return fmt.Errorf("triggers: interval %q: %w", c.ID, err)
			}
			iv = append(iv, IntervalSpec{ID: c.ID, Every: d})
		}
		if err := m.Interval.SetIntervals(OwnerConfig, iv); err != nil {
			return fmt.Errorf("triggers: %w", err)
		}
	}
	if len(cfg.Webhooks) > 0 {
		if err := m.Webhook.SetHooks(OwnerConfig, cfg.Webhooks); err != nil {
			return fmt.Errorf("triggers: %w", err)
		}
	}
	for _, s := range []kernel.TriggerSource{m.Cron, m.Interval, m.Webhook, m.Event} {
		if err := h.Triggers().Register(s); err != nil {
			return fmt.Errorf("triggers: %w", err)
		}
	}
	h.Handle("POST "+HookPathPrefix+"{id}", m.Webhook)
	return nil
}

// CronRegistrar is implemented by the cron source.
type CronRegistrar interface {
	SetCron(owner string, specs []CronSpec) error
}

// IntervalRegistrar is implemented by the interval source.
type IntervalRegistrar interface {
	SetIntervals(owner string, specs []IntervalSpec) error
}

// HookRegistrar is implemented by the webhook source.
type HookRegistrar interface {
	SetHooks(owner string, hooks []HookSpec) error
}
