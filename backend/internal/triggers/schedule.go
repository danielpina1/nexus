package triggers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/danielpina1/nexus/backend/kernel"
)

// Clock abstracts time for the schedulers (tests use a fake clock).
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// CronSpec is one cron schedule. Firing.Key is ID.
type CronSpec struct {
	ID string `yaml:"id" json:"id"`
	// Expr is a standard 5-field cron expression or a descriptor
	// (@daily, @hourly, @every 10m…).
	Expr string `yaml:"expr" json:"expr"`
	// TZ is an IANA time zone (default: the hub's local zone).
	TZ string `yaml:"tz" json:"tz,omitempty"`
}

// IntervalSpec fires every Every (first firing one period after it is set).
// Firing.Key is ID.
type IntervalSpec struct {
	ID    string        `yaml:"id" json:"id"`
	Every time.Duration `yaml:"every" json:"every"`
}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ParseCron parses expr in time zone tz (empty = local).
func ParseCron(expr, tz string) (cron.Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, errors.New("empty cron expression")
	}
	if strings.HasPrefix(expr, "TZ=") || strings.HasPrefix(expr, "CRON_TZ=") {
		return cronParser.Parse(expr)
	}
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return nil, fmt.Errorf("time zone %q: %w", tz, err)
		}
		expr = "CRON_TZ=" + tz + " " + expr
	}
	return cronParser.Parse(expr)
}

// NextFire returns the first time strictly after `after` at which the cron
// spec fires.
func NextFire(expr, tz string, after time.Time) (time.Time, error) {
	s, err := ParseCron(expr, tz)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(after), nil
}

// everySchedule is a fixed-period schedule (sub-second periods allowed).
type everySchedule time.Duration

func (e everySchedule) Next(t time.Time) time.Time { return t.Add(time.Duration(e)) }

type entry struct {
	key   string
	owner string
	sched cron.Schedule
	info  map[string]any
	next  time.Time
}

// ScheduleSource is a time-based trigger source. The same type backs the
// "cron" and "interval" sources.
type ScheduleSource struct {
	name  string
	clock Clock
	log   *slog.Logger

	mu      sync.Mutex
	entries map[string]*entry // by key
	changed chan struct{}
}

var (
	_ kernel.TriggerSource = (*ScheduleSource)(nil)
	_ CronRegistrar        = (*ScheduleSource)(nil)
	_ IntervalRegistrar    = (*ScheduleSource)(nil)
)

func newScheduleSource(name string, c Clock, log *slog.Logger) *ScheduleSource {
	if c == nil {
		c = realClock{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &ScheduleSource{name: name, clock: c, log: log, entries: map[string]*entry{}, changed: make(chan struct{}, 1)}
}

// NewCronSource returns the "cron" source (nil clock = real time).
func NewCronSource(c Clock, log *slog.Logger) *ScheduleSource {
	return newScheduleSource(SourceCron, c, log)
}

// NewIntervalSource returns the "interval" source (nil clock = real time).
func NewIntervalSource(c Clock, log *slog.Logger) *ScheduleSource {
	return newScheduleSource(SourceInterval, c, log)
}

func (s *ScheduleSource) Name() string { return s.name }

// SetCron replaces all entries of owner with specs. Invalid specs are
// skipped and reported in the returned error (valid ones are still set).
func (s *ScheduleSource) SetCron(owner string, specs []CronSpec) error {
	var errs []error
	var es []*entry
	for _, sp := range specs {
		sched, err := ParseCron(sp.Expr, sp.TZ)
		if err != nil {
			errs = append(errs, fmt.Errorf("cron %q: %w", sp.ID, err))
			continue
		}
		info := map[string]any{"expr": sp.Expr}
		if sp.TZ != "" {
			info["tz"] = sp.TZ
		}
		es = append(es, &entry{key: sp.ID, owner: owner, sched: sched, info: info})
	}
	s.set(owner, es)
	return errors.Join(errs...)
}

// SetIntervals replaces all interval entries of owner.
func (s *ScheduleSource) SetIntervals(owner string, specs []IntervalSpec) error {
	var errs []error
	var es []*entry
	for _, sp := range specs {
		if sp.Every <= 0 {
			errs = append(errs, fmt.Errorf("interval %q: period must be positive", sp.ID))
			continue
		}
		es = append(es, &entry{key: sp.ID, owner: owner, sched: everySchedule(sp.Every),
			info: map[string]any{"every": sp.Every.String()}})
	}
	s.set(owner, es)
	return errors.Join(errs...)
}

func (s *ScheduleSource) set(owner string, es []*entry) {
	now := s.clock.Now()
	s.mu.Lock()
	old := map[string]*entry{}
	for k, e := range s.entries {
		if e.owner == owner {
			old[k] = e
			delete(s.entries, k)
		}
	}
	for _, e := range es {
		if e.key == "" {
			continue
		}
		// Keep the phase of unchanged entries (an interval does not restart
		// when an unrelated workflow note is edited).
		if o, ok := old[e.key]; ok && sameInfo(o.info, e.info) {
			e.next = o.next
		} else {
			e.next = e.sched.Next(now)
		}
		s.entries[e.key] = e
	}
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func sameInfo(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Keys returns the scheduled keys with their next firing time (sorted by key).
func (s *ScheduleSource) Keys() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]time.Time, len(s.entries))
	for k, e := range s.entries {
		out[k] = e.next
	}
	return out
}

// Run emits a firing for every entry whose time has come. A missed slot
// (hub asleep) fires once when the hub wakes, never repeatedly.
func (s *ScheduleSource) Run(ctx context.Context, emit func(kernel.Firing)) error {
	for {
		now := s.clock.Now()
		var due []kernel.Firing
		var earliest time.Time
		s.mu.Lock()
		keys := make([]string, 0, len(s.entries))
		for k := range s.entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			e := s.entries[k]
			if e.next.IsZero() {
				continue
			}
			if !e.next.After(now) {
				payload := map[string]any{"scheduled_at": e.next.UTC().Format(time.RFC3339), "fired_at": now.UTC().Format(time.RFC3339)}
				for ik, iv := range e.info {
					payload[ik] = iv
				}
				due = append(due, kernel.Firing{Source: s.name, Key: e.key, Payload: payload,
					Meta: map[string]string{"owner": e.owner, "scheduled_at": e.next.UTC().Format(time.RFC3339)}})
				e.next = e.sched.Next(now)
			}
			if !e.next.IsZero() && (earliest.IsZero() || e.next.Before(earliest)) {
				earliest = e.next
			}
		}
		s.mu.Unlock()
		for _, f := range due {
			emit(f)
		}

		var timer <-chan time.Time
		if !earliest.IsZero() {
			timer = s.clock.After(earliest.Sub(now))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.changed:
		case <-timer:
		}
	}
}
