package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/triggers"
	"github.com/danielpina1/nexus/backend/kernel"
)

// SvcWorkflows is the service name under which *Service is provided.
const SvcWorkflows = "workflows"

// Workflows is the service API other modules (command, web handlers) use.
type Workflows interface {
	Start(ctx context.Context, workflowID string, inputs map[string]any, source string) (string, error)
	StartWith(ctx context.Context, o StartOptions) (string, error)
	List(ctx context.Context) ([]*Workflow, error)
	Get(ctx context.Context, workflowID string) (*Workflow, error)
	ListRuns(ctx context.Context, workflowID string, limit int) ([]*RunInfo, error)
	GetRun(ctx context.Context, runID string) (*RunInfo, error)
	Cancel(ctx context.Context, runID string) error
	Retry(ctx context.Context, runID string) error
	Answer(ctx context.Context, questionID, choice, text string) error
	OwnsQuestion(ctx context.Context, questionID string) bool
	MatchIntent(input string) (workflowID string, score float64)
	IntentInputs(workflowID, input string) map[string]any
	PauseTriggers(paused bool)
}

var _ Workflows = (*Service)(nil)

// StartOptions configures a run.
type StartOptions struct {
	WorkflowID string
	Inputs     map[string]any
	// Source: "manual", "command", "tool", "trigger:cron", "workflow:<id>"…
	Source  string
	Trigger map[string]any
	DryRun  bool
	// IdempotencyKey: a second start with the same key returns the first run.
	IdempotencyKey string
	ParentRunID    string
	depth          int
}

// Config is the "workflow" config section.
type Config struct {
	// Dir overrides the workflows directory (default <vault>/workflows).
	Dir string `yaml:"dir"`
	// ReloadInterval is how often the directory is polled (default 3s).
	ReloadInterval time.Duration `yaml:"reload_interval"`
	// PollInterval is how often waiting steps re-check tasks, approvals and
	// answers when no event wakes them (default 2s).
	PollInterval time.Duration `yaml:"poll_interval"`
	// NotifySuccess publishes a notice when a run succeeds (failures always do).
	NotifySuccess bool `yaml:"notify_success"`
	// ProgressNotices publishes a notice per finished step (noisy; debugging).
	ProgressNotices bool `yaml:"progress_notices"`
}

func (c *Config) defaults() {
	if c.ReloadInterval <= 0 {
		c.ReloadInterval = 3 * time.Second
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
}

// Service runs workflows. It implements Workflows.
type Service struct {
	h   kernel.Host
	log *slog.Logger
	cfg Config
	st  *store
	cat *Catalog
	now func() time.Time
	// backoff is the delay before retry number try+1 of a failing step.
	backoff func(try int) time.Duration

	base       context.Context
	baseCancel context.CancelCauseFunc
	wg         sync.WaitGroup
	paused     atomic.Bool
	wake       signal

	mu     sync.Mutex
	active map[string]context.CancelCauseFunc
}

// NewService migrates the store and builds a service. dir overrides the
// workflows directory (nil = <vault root>/workflows, resolved lazily).
func NewService(h kernel.Host, st kernel.Store, cfg Config, dir func() string) (*Service, error) {
	if err := st.Migrate("workflow", migrations); err != nil {
		return nil, fmt.Errorf("workflow: migrate: %w", err)
	}
	cfg.defaults()
	s := &Service{h: h, log: h.Logger(), cfg: cfg, now: time.Now, backoff: backoff, active: map[string]context.CancelCauseFunc{}}
	s.st = &store{db: st.DB(), now: func() time.Time { return s.now() }}
	if dir == nil {
		dir = s.defaultDir
	}
	s.cat = NewCatalog(dir, s.log)
	s.base, s.baseCancel = context.WithCancelCause(context.Background())
	return s, nil
}

func (s *Service) defaultDir() string {
	if s.cfg.Dir != "" {
		return s.cfg.Dir
	}
	v, err := kernel.Resolve[kernel.Vault](s.h, kernel.SvcVault)
	if err != nil || v.Root() == "" {
		return ""
	}
	return v.Root() + "/" + Dir
}

// Catalog returns the workflow catalog.
func (s *Service) Catalog() *Catalog { return s.cat }

// ---------------------------------------------------------------- lifecycle

// Run loads the catalog, wires trigger sources, resumes unfinished runs and
// keeps the catalog fresh until Stop.
func (s *Service) Run() error {
	if _, err := s.cat.Reload(true); err != nil {
		s.log.Warn("workflow: load catalog", "err", err)
	}
	s.syncTriggers()
	s.goBG(func(ctx context.Context) { s.cat.Watch(ctx, s.cfg.ReloadInterval, s.syncTriggers) })
	s.goBG(s.watchBus)
	for _, src := range s.h.Triggers().All() {
		src := src
		s.goBG(func(ctx context.Context) {
			if err := src.Run(ctx, func(f kernel.Firing) { s.OnFiring(ctx, f) }); err != nil && ctx.Err() == nil {
				s.log.Warn("workflow: trigger source stopped", "source", src.Name(), "err", err)
			}
		})
	}
	return s.resume()
}

func (s *Service) goBG(fn func(ctx context.Context)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.base.Err() != nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn(s.base)
	}()
}

// Stop suspends running workflows (they resume on next start) and waits.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.baseCancel(errShutdown)
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) resume() error {
	runs, err := s.st.listRuns(s.base, "", RunRunning, 1000)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.def == nil {
			_ = s.st.finishRun(s.base, r.ID, RunFailed, nil, "cannot resume: definition snapshot unreadable")
			continue
		}
		s.log.Info("workflow: resuming run", "run", r.ID, "workflow", r.WorkflowID)
		s.launch(r)
	}
	return nil
}

// watchBus wakes waiting steps on task/approval changes.
func (s *Service) watchBus(ctx context.Context) {
	bus, ok := s.bus()
	if !ok {
		return
	}
	for ctx.Err() == nil {
		since, err := bus.LastSeq(ctx)
		if err != nil {
			return
		}
		ch, err := bus.Subscribe(ctx, since, kernel.EventFilter{})
		if err != nil {
			return
		}
		for ev := range ch {
			switch ev.GetBody().(type) {
			case *nexusv1.Event_TaskUpdated, *nexusv1.Event_Approval:
				s.wake.fire()
			}
		}
	}
}

// signal is a broadcast "something changed" notification.
type signal struct {
	mu sync.Mutex
	ch chan struct{}
}

func (s *signal) c() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}

func (s *signal) fire() {
	s.mu.Lock()
	if s.ch != nil {
		close(s.ch)
	}
	s.ch = make(chan struct{})
	s.mu.Unlock()
}

// waitFor re-checks cond on every wake-up and poll interval.
func (s *Service) waitFor(ctx context.Context, cond func(context.Context) (bool, error)) error {
	for {
		wake := s.wake.c()
		done, err := cond(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return err
		}
		if done {
			return nil
		}
		t := time.NewTimer(s.cfg.PollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return context.Cause(ctx)
		case <-wake:
		case <-t.C:
		}
		t.Stop()
	}
}

func (s *Service) sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}

// ---------------------------------------------------------------- services

func (s *Service) tasks() (kernel.TaskEngine, bool) {
	v, err := kernel.Resolve[kernel.TaskEngine](s.h, kernel.SvcTasks)
	return v, err == nil
}

func (s *Service) approvals() (kernel.ApprovalBroker, bool) {
	v, err := kernel.Resolve[kernel.ApprovalBroker](s.h, kernel.SvcApprovals)
	return v, err == nil
}

func (s *Service) bus() (kernel.EventBus, bool) {
	v, err := kernel.Resolve[kernel.EventBus](s.h, kernel.SvcEventBus)
	return v, err == nil
}

func (s *Service) publish(ctx context.Context, ev *nexusv1.Event) {
	bus, ok := s.bus()
	if !ok {
		return
	}
	if _, err := bus.Publish(context.WithoutCancel(ctx), ev); err != nil {
		s.log.Warn("workflow: publish", "err", err)
	}
}

func (s *Service) notice(ctx context.Context, run *RunInfo, kind nexusv1.NoticeKind, title, body string, data map[string]any) {
	data["workflow_id"], data["workflow_run_id"] = run.WorkflowID, run.ID
	ds, err := structpb.NewStruct(jsonClean(data).(map[string]any))
	if err != nil {
		ds = nil
	}
	if len(body) > 2000 {
		body = body[:2000] + "…"
	}
	s.publish(ctx, &nexusv1.Event{RunId: run.ID, Actor: "workflow:" + run.WorkflowID, Body: &nexusv1.Event_Notice{Notice: &nexusv1.Notice{
		Id: newID("ntc_"), Kind: kind, Title: title, Body: body, Data: ds,
	}}})
}

func (s *Service) progress(ctx context.Context, run *RunInfo, wf *Workflow, row *StepInfo) {
	if !s.cfg.ProgressNotices {
		return
	}
	s.notice(ctx, run, nexusv1.NoticeKind_NOTICE_KIND_UNSPECIFIED, fmt.Sprintf("%s · %s %s", wf.Name, row.Path, row.State),
		row.Error, map[string]any{"step": row.Path, "state": row.State})
}

func (s *Service) runFinished(ctx context.Context, run *RunInfo, wf *Workflow) {
	s.wake.fire()
	if run.ParentRunID != "" {
		return
	}
	data := map[string]any{"state": run.State, "dry_run": run.DryRun}
	switch {
	case run.State == RunFailed:
		s.notice(ctx, run, nexusv1.NoticeKind_NOTICE_KIND_ALERT, wf.Name+" failed", run.Error, data)
	case run.State == RunSucceeded && (s.cfg.NotifySuccess || run.DryRun):
		title := wf.Name + " finished"
		if run.DryRun {
			title = wf.Name + " (dry run) finished"
		}
		s.notice(ctx, run, nexusv1.NoticeKind_NOTICE_KIND_RESULT, title, Text(run.Output), data)
	}
}

// ---------------------------------------------------------------- start

// Start starts a workflow run and returns its id. The run executes in the
// background; follow it with GetRun.
func (s *Service) Start(ctx context.Context, workflowID string, inputs map[string]any, source string) (string, error) {
	return s.StartWith(ctx, StartOptions{WorkflowID: workflowID, Inputs: inputs, Source: source})
}

// StartWith starts a run with options. Disabled workflows can be started
// explicitly (enabled only gates triggers and intents); invalid ones cannot.
func (s *Service) StartWith(ctx context.Context, o StartOptions) (string, error) {
	if s.base.Err() != nil {
		return "", errors.New("workflow engine is stopped")
	}
	if o.IdempotencyKey != "" {
		if id, err := s.st.runByIdempotencyKey(ctx, o.IdempotencyKey); err != nil || id != "" {
			return id, err
		}
	}
	wf, ok := s.cat.Get(o.WorkflowID)
	if !ok {
		if _, err := s.cat.Reload(false); err == nil {
			wf, ok = s.cat.Get(o.WorkflowID)
		}
	}
	if !ok {
		return "", fmt.Errorf("%w: workflow %q", ErrNotFound, o.WorkflowID)
	}
	if !wf.Valid() {
		return "", fmt.Errorf("workflow %q is invalid: %s", wf.ID, strings.Join(wf.Errors, "; "))
	}
	inputs, err := resolveInputs(wf, o.Inputs)
	if err != nil {
		return "", err
	}
	if o.Source == "" {
		o.Source = "manual"
	}
	trig := map[string]any{}
	for k, v := range o.Trigger {
		trig[k] = v
	}
	for k, v := range map[string]any{"source": o.Source, "key": "", "payload": map[string]any{}, "meta": map[string]any{}} {
		if _, ok := trig[k]; !ok {
			trig[k] = v
		}
	}
	run := &RunInfo{ID: newID("wfr_"), WorkflowID: wf.ID, ParentRunID: o.ParentRunID, State: RunRunning,
		DryRun: o.DryRun || wf.DryRun, Source: o.Source, Inputs: inputs, Trigger: jsonClean(trig).(map[string]any),
		depth: o.depth, def: wf}
	if err := s.st.insertRun(ctx, run, o.IdempotencyKey); err != nil {
		if o.IdempotencyKey != "" {
			if id, err2 := s.st.runByIdempotencyKey(ctx, o.IdempotencyKey); err2 == nil && id != "" {
				return id, nil
			}
		}
		return "", err
	}
	// Reload through JSON so a fresh run and a resumed run see identical values.
	stored, err := s.st.getRun(ctx, run.ID)
	if err != nil {
		return "", err
	}
	s.log.Info("workflow run started", "run", run.ID, "workflow", wf.ID, "source", o.Source, "dry_run", run.DryRun)
	s.launch(stored)
	return run.ID, nil
}

func (s *Service) launch(run *RunInfo) {
	ctx, cancel := context.WithCancelCause(s.base)
	s.mu.Lock()
	if s.base.Err() != nil { // stopping: the run resumes on next start
		s.mu.Unlock()
		cancel(errShutdown)
		return
	}
	s.active[run.ID] = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.active, run.ID)
			s.mu.Unlock()
			cancel(nil)
		}()
		(&runner{s: s, run: run, wf: run.def}).execute(ctx)
	}()
}

// resolveInputs applies defaults and checks required inputs. Unknown inputs
// are kept (available as inputs.<name>).
func resolveInputs(wf *Workflow, in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	var missing []string
	for _, d := range wf.Inputs {
		v, ok := out[d.Name]
		if !ok || v == nil || v == "" {
			if d.Default != nil {
				out[d.Name] = d.Default
				continue
			}
			if d.Required {
				missing = append(missing, d.Name)
				continue
			}
			if !ok || v == nil {
				out[d.Name] = zeroFor(d.Type)
			}
		}
		if len(d.Choices) > 0 {
			if sv, ok := out[d.Name].(string); ok && sv != "" && !contains(d.Choices, sv) {
				return nil, fmt.Errorf("input %s: %q is not one of %v", d.Name, sv, d.Choices)
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required inputs: %s", strings.Join(missing, ", "))
	}
	return jsonClean(out).(map[string]any), nil
}

func zeroFor(typ string) any {
	switch strings.ToLower(typ) {
	case "number", "int", "integer", "float":
		return 0
	case "bool", "boolean":
		return false
	case "list", "array":
		return []any{}
	case "object", "map":
		return map[string]any{}
	}
	return ""
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- queries

// List returns every workflow note (invalid ones carry Errors).
func (s *Service) List(context.Context) ([]*Workflow, error) { return s.cat.List(), nil }

// Get returns one workflow.
func (s *Service) Get(_ context.Context, id string) (*Workflow, error) {
	w, ok := s.cat.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: workflow %q", ErrNotFound, id)
	}
	return w, nil
}

// ListRuns returns recent runs (all workflows when workflowID is empty).
func (s *Service) ListRuns(ctx context.Context, workflowID string, limit int) ([]*RunInfo, error) {
	return s.st.listRuns(ctx, workflowID, "", limit)
}

// GetRun returns a run with its steps.
func (s *Service) GetRun(ctx context.Context, id string) (*RunInfo, error) {
	r, err := s.st.getRun(ctx, id)
	if err != nil {
		return nil, err
	}
	r.Steps, err = s.st.listSteps(ctx, id)
	return r, err
}

// Cancel cancels a run (its in-flight agent tasks and child runs too).
func (s *Service) Cancel(ctx context.Context, id string) error {
	r, err := s.st.getRun(ctx, id)
	if err != nil {
		return err
	}
	if runTerminal(r.State) {
		return nil
	}
	s.mu.Lock()
	cancel, ok := s.active[id]
	s.mu.Unlock()
	if ok {
		cancel(errCancelled)
		return nil
	}
	return s.st.finishRun(ctx, id, RunCancelled, r.Output, "cancelled")
}

// Retry re-runs a failed or cancelled run from its failed steps (finished
// steps keep their outputs; external tools are called again with the same
// idempotency key).
func (s *Service) Retry(ctx context.Context, id string) error {
	r, err := s.st.getRun(ctx, id)
	if err != nil {
		return err
	}
	if r.State != RunFailed && r.State != RunCancelled {
		return fmt.Errorf("run %s is %s; only failed or cancelled runs can be retried", id, r.State)
	}
	if r.def == nil {
		return errors.New("run definition snapshot unreadable")
	}
	if _, err := s.st.markRetry(ctx, id); err != nil {
		return err
	}
	if err := s.st.setRunState(ctx, id, RunRunning); err != nil {
		return err
	}
	r.State, r.Error = RunRunning, ""
	s.launch(r)
	return nil
}

// Answer delivers the answer to a workflow `ask` step (the Question event's
// question_id). choice wins over text when both are set.
func (s *Service) Answer(ctx context.Context, questionID, choice, text string) error {
	if choice == "" && strings.TrimSpace(text) == "" {
		return errors.New("choice or text required")
	}
	b, _ := json.Marshal(Answer{Choice: choice, Text: text})
	if _, err := s.st.answer(ctx, questionID, string(b)); err != nil {
		return err
	}
	s.wake.fire()
	return nil
}

// OwnsQuestion reports whether a question id belongs to a workflow run.
func (s *Service) OwnsQuestion(ctx context.Context, questionID string) bool {
	return questionID != "" && s.st.hasQuestion(ctx, questionID)
}

// PauseTriggers stops (or resumes) starting runs from trigger firings
// (kill switch with pause_triggers). Manual starts still work.
func (s *Service) PauseTriggers(p bool) { s.paused.Store(p) }

// ---------------------------------------------------------------- triggers

func cronKey(wfID string, i int) string { return fmt.Sprintf("workflow:%s#%d", wfID, i) }

func parseCronKey(k string) (string, int, bool) {
	rest, ok := strings.CutPrefix(k, "workflow:")
	if !ok {
		return "", 0, false
	}
	i := strings.LastIndexByte(rest, '#')
	if i < 0 {
		return "", 0, false
	}
	var n int
	if _, err := fmt.Sscanf(rest[i+1:], "%d", &n); err != nil {
		return "", 0, false
	}
	return rest[:i], n, true
}

// syncTriggers pushes cron, interval and webhook triggers of enabled, valid
// workflows to the trigger sources.
func (s *Service) syncTriggers() {
	var crons []triggers.CronSpec
	var ivs []triggers.IntervalSpec
	var hooks []triggers.HookSpec
	for _, w := range s.cat.List() {
		if !w.Enabled || !w.Valid() {
			continue
		}
		for i, t := range w.Triggers {
			switch t.Kind {
			case TriggerCron:
				crons = append(crons, triggers.CronSpec{ID: cronKey(w.ID, i), Expr: t.Value, TZ: t.TZ})
			case TriggerInterval:
				ivs = append(ivs, triggers.IntervalSpec{ID: cronKey(w.ID, i), Every: time.Duration(t.Every)})
			case TriggerWebhook:
				hooks = append(hooks, triggers.HookSpec{ID: t.Value, SecretEnv: t.SecretEnv})
			}
		}
	}
	for _, src := range s.h.Triggers().All() {
		var err error
		switch src.Name() {
		case triggers.SourceCron:
			if r, ok := src.(triggers.CronRegistrar); ok {
				err = r.SetCron("workflow", crons)
			}
		case triggers.SourceInterval:
			if r, ok := src.(triggers.IntervalRegistrar); ok {
				err = r.SetIntervals("workflow", ivs)
			}
		case triggers.SourceWebhook:
			if r, ok := src.(triggers.HookRegistrar); ok {
				err = r.SetHooks("workflow", hooks)
			}
		}
		if err != nil {
			s.log.Warn("workflow: register triggers", "source", src.Name(), "err", err)
		}
	}
}

type triggerMatch struct {
	wf  *Workflow
	idx int
}

func firingMap(f kernel.Firing) map[string]any {
	meta := map[string]any{}
	for k, v := range f.Meta {
		meta[k] = v
	}
	payload := f.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	return jsonClean(map[string]any{"source": f.Source, "key": f.Key, "payload": payload, "meta": meta}).(map[string]any)
}

// match returns the workflows (and trigger index) a firing starts.
func (s *Service) match(f kernel.Firing) []triggerMatch {
	var out []triggerMatch
	trig := firingMap(f)
	payload, _ := trig["payload"].(map[string]any)
	ev := payload["event"]
	whereOK := func(w *Workflow, t Trigger) bool {
		if t.Where == "" {
			return true
		}
		ok, err := EvalBool(t.Where, Vars{"trigger": trig, "event": ev, "inputs": map[string]any{}, "steps": map[string]any{}})
		if err != nil {
			s.log.Debug("workflow: trigger where", "workflow", w.ID, "err", err)
		}
		return err == nil && ok
	}
	if wfID, idx, ok := parseCronKey(f.Key); ok && (f.Source == triggers.SourceCron || f.Source == triggers.SourceInterval) {
		if w, ok := s.cat.Get(wfID); ok && w.Enabled && w.Valid() && idx < len(w.Triggers) && whereOK(w, w.Triggers[idx]) {
			out = append(out, triggerMatch{w, idx})
		}
		return out
	}
	for _, w := range s.cat.List() {
		if !w.Enabled || !w.Valid() {
			continue
		}
		for i, t := range w.Triggers {
			hit := false
			switch t.Kind {
			case TriggerCron, TriggerInterval, TriggerIntent:
			case TriggerWebhook:
				hit = f.Source == triggers.SourceWebhook && t.Value == f.Key
			case TriggerEvent:
				hit = f.Source == triggers.SourceEvent && triggers.MatchTopic(t.Value, f.Key)
			default:
				hit = f.Source == t.Kind && (t.Value == "" || t.Value == f.Key)
			}
			if hit && whereOK(w, t) {
				out = append(out, triggerMatch{w, i})
			}
		}
	}
	return out
}

// OnFiring starts the workflows a firing matches.
func (s *Service) OnFiring(ctx context.Context, f kernel.Firing) {
	if s.paused.Load() {
		s.log.Info("workflow: triggers paused; firing dropped", "source", f.Source, "key", f.Key)
		return
	}
	trig := firingMap(f)
	for _, m := range s.match(f) {
		t := m.wf.Triggers[m.idx]
		inputs := map[string]any{}
		payload, _ := trig["payload"].(map[string]any)
		for _, d := range m.wf.Inputs {
			if v, ok := payload[d.Name]; ok {
				inputs[d.Name] = v
			}
		}
		if len(t.Inputs) > 0 {
			v, err := RenderValue(t.Inputs, Vars{"trigger": trig, "event": payload["event"], "inputs": map[string]any{}, "steps": map[string]any{}})
			if err != nil {
				s.log.Warn("workflow: trigger inputs", "workflow", m.wf.ID, "err", err)
				continue
			}
			for k, x := range v.(map[string]any) {
				inputs[k] = x
			}
		}
		key := ""
		switch {
		case f.Meta["scheduled_at"] != "":
			key = fmt.Sprintf("trigger:%s:%s:%s", m.wf.ID, f.Key, f.Meta["scheduled_at"])
		case f.Source == triggers.SourceEvent && f.Meta["seq"] != "":
			key = fmt.Sprintf("trigger:%s:event:%s:%s", m.wf.ID, f.Meta["seq"], f.Key)
		case f.Meta["x_github_delivery"] != "":
			key = fmt.Sprintf("trigger:%s:webhook:%s", m.wf.ID, f.Meta["x_github_delivery"])
		}
		id, err := s.StartWith(ctx, StartOptions{WorkflowID: m.wf.ID, Inputs: inputs, Source: "trigger:" + f.Source,
			Trigger: trig, DryRun: t.DryRun, IdempotencyKey: key})
		if err != nil {
			s.log.Warn("workflow: start from trigger", "workflow", m.wf.ID, "source", f.Source, "key", f.Key, "err", err)
			continue
		}
		s.log.Info("workflow: triggered", "workflow", m.wf.ID, "run", id, "source", f.Source, "key", f.Key)
	}
}

// ---------------------------------------------------------------- intents

func normText(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// phraseScore scores how well input matches an intent phrase (0..1).
func phraseScore(input, phrase string) float64 {
	in, ph := normText(input), normText(phrase)
	if in == "" || ph == "" {
		return 0
	}
	if in == ph {
		return 1
	}
	if strings.HasPrefix(in+" ", ph+" ") {
		return 0.9
	}
	if strings.Contains(" "+in+" ", " "+ph+" ") {
		return 0.8
	}
	// Token overlap (all phrase words present, any order).
	words := strings.Fields(ph)
	have := map[string]bool{}
	for _, w := range strings.Fields(in) {
		have[w] = true
	}
	n := 0
	for _, w := range words {
		if have[w] {
			n++
		}
	}
	return 0.7 * float64(n) / float64(len(words)) * float64(n) / float64(len(words))
}

// MatchIntent returns the enabled workflow whose intent phrases best match
// input, with a score in [0,1] (1 = exact phrase, 0.9 = input starts with
// the phrase, 0.8 = contains it, 0.5–0.7 = most phrase words present; weaker
// matches return ""). The command module should route to the workflow when
// score ≥ ~0.8 and offer it as a choice below that.
func (s *Service) MatchIntent(input string) (string, float64) {
	best, bestScore := "", 0.0
	list := s.cat.List()
	sort.SliceStable(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	for _, w := range list {
		if !w.Enabled || !w.Valid() {
			continue
		}
		for _, t := range w.Triggers {
			if t.Kind != TriggerIntent {
				continue
			}
			for _, p := range t.Phrases {
				if sc := phraseScore(input, p); sc > bestScore {
					best, bestScore = w.ID, sc
				}
			}
		}
	}
	if bestScore < minIntentScore {
		return "", 0
	}
	return best, bestScore
}

// minIntentScore drops weak word-overlap matches.
const minIntentScore = 0.5

// IntentInputs derives inputs from a command: the text after the matched
// phrase (e.g. "buy groceries: milk, eggs" → "milk, eggs") goes to the first
// text input. It always sets inputs.text to the full command only when the
// workflow declares a "text" input.
func (s *Service) IntentInputs(workflowID, input string) map[string]any {
	out := map[string]any{}
	w, ok := s.cat.Get(workflowID)
	if !ok {
		return out
	}
	rest := ""
	lower := strings.ToLower(input)
	bestLen := 0
	for _, t := range w.Triggers {
		for _, p := range t.Phrases {
			pl := strings.ToLower(strings.TrimSpace(p))
			if i := strings.Index(lower, pl); i >= 0 && len(pl) > bestLen {
				bestLen = len(pl)
				rest = input[i+len(pl):]
			}
		}
	}
	rest = strings.TrimFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(":,-–—.;", r) })
	for _, d := range w.Inputs {
		if d.Name == "text" {
			out["text"] = input
		}
	}
	if rest != "" {
		for _, d := range w.Inputs {
			if d.Name == "text" {
				continue
			}
			if t := strings.ToLower(d.Type); t == "text" || t == "string" || t == "" {
				out[d.Name] = rest
				break
			}
		}
	}
	return out
}
