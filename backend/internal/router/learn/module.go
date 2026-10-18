package learn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() { kernel.RegisterModule(func() kernel.Module { return &Module{} }) }

// Module is the "router.learn" module.
type Module struct {
	Learner *Learner
}

// Info implements kernel.Module.
func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "router.learn",
		Description:    "Learning router stage: embeddings kNN, online classifier, runtime bandit, outcome feedback",
		Side:           kernel.SideHub,
		Requires:       []string{"store", "router"},
		DefaultEnabled: true,
	}
}

// Init migrates and provides SvcLearn (config section "router.learn").
func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("router.learn: config: %w", err)
	}
	if !cfg.IsEnabled() {
		h.Logger().Info("router.learn: disabled")
		return nil
	}
	st, err := kernel.Resolve[kernel.Store](h, kernel.SvcStore)
	if err != nil {
		return err
	}
	l, err := New(h, st, cfg)
	if err != nil {
		return err
	}
	m.Learner = l
	h.Provide(SvcLearn, l)
	return nil
}

// Start loads persisted weights, rebuilds from history in the background
// and starts the feedback loop.
func (m *Module) Start(context.Context) error {
	if m.Learner == nil {
		return nil
	}
	return m.Learner.Start()
}

// Stop ends the background work.
func (m *Module) Stop(context.Context) error {
	if m.Learner != nil {
		m.Learner.Stop()
	}
	return nil
}

// Start runs the background loops (feedback subscription, correction sync,
// periodic rebuild). It returns immediately.
func (l *Learner) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	l.stop = cancel
	if b, err := loadState(ctx, l.db, "classifier"); err == nil && b != nil {
		if c, err := UnmarshalClassifier(b); err == nil && c.Model == l.embedder().Model() {
			l.mu.Lock()
			l.sc.clf, l.sc.labeled = c, c.Labeled
			l.mu.Unlock()
		}
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		if err := l.Rebuild(ctx); err != nil && ctx.Err() == nil {
			l.log.Warn("router.learn: initial rebuild", "err", err)
		}
		t := time.NewTicker(l.cfg.RebuildEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := l.Rebuild(ctx); err != nil && ctx.Err() == nil {
					l.log.Warn("router.learn: rebuild", "err", err)
				}
			}
		}
	}()
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		t := time.NewTicker(l.cfg.SyncEvery)
		defer t.Stop()
		for {
			if err := l.SyncCorrections(ctx); err != nil && ctx.Err() == nil {
				l.log.Warn("router.learn: sync corrections", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	if bus, err := kernel.Resolve[kernel.EventBus](l.h, kernel.SvcEventBus); err == nil {
		since, _ := bus.LastSeq(ctx)
		ch, err := bus.Subscribe(ctx, since, kernel.EventFilter{})
		if err != nil {
			l.log.Warn("router.learn: subscribe", "err", err)
		} else {
			l.wg.Add(1)
			go func() {
				defer l.wg.Done()
				for ev := range ch {
					l.HandleEvent(ctx, ev)
				}
			}()
		}
	}
	return nil
}

// Stop cancels the loops and waits for them.
func (l *Learner) Stop() {
	if l.stop != nil {
		l.stop()
	}
	l.wg.Wait()
}

// HandleEvent feeds outcomes from the event log: terminal TaskUpdated
// events become outcomes; escalations and denied approvals are remembered
// as penalties for the task's eventual outcome.
func (l *Learner) HandleEvent(ctx context.Context, ev *nexusv1.Event) {
	switch b := ev.Body.(type) {
	case *nexusv1.Event_Approval:
		a := b.Approval
		if a.GetTaskId() != "" && (a.State == nexusv1.ApprovalState_APPROVAL_STATE_DENIED || a.State == nexusv1.ApprovalState_APPROVAL_STATE_EXPIRED) {
			l.flag(a.TaskId).denied = true
		}
	case *nexusv1.Event_TaskUpdated:
		t := b.TaskUpdated.GetTask()
		if t == nil || t.Id == "" {
			return
		}
		switch t.State {
		case nexusv1.TaskState_TASK_STATE_ESCALATING:
			l.flag(t.Id).escalated = true
		case nexusv1.TaskState_TASK_STATE_SUCCEEDED, nexusv1.TaskState_TASK_STATE_FAILED, nexusv1.TaskState_TASK_STATE_CANCELLED:
			if err := l.taskFinished(ctx, t); err != nil && ctx.Err() == nil {
				l.log.Warn("router.learn: outcome", "task", t.Id, "err", err)
			}
		}
	}
}

func (l *Learner) flag(taskID string) *taskFlags {
	l.flagMu.Lock()
	defer l.flagMu.Unlock()
	f, ok := l.flags[taskID]
	if !ok {
		if len(l.flags) > 10000 { // bound memory: forget the oldest generation
			l.flags = map[string]*taskFlags{}
		}
		f = &taskFlags{}
		l.flags[taskID] = f
	}
	return f
}

func (l *Learner) taskFinished(ctx context.Context, t *nexusv1.Task) error {
	var decisionID, corrected string
	err := l.db.QueryRowContext(ctx, `SELECT id, corrected_agent FROM router_decisions WHERE task_id = ? ORDER BY created_at DESC LIMIT 1`, t.Id).
		Scan(&decisionID, &corrected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // not routed (agent given explicitly, workflow step…)
	}
	if err != nil {
		return err
	}
	l.flagMu.Lock()
	f := l.flags[t.Id]
	delete(l.flags, t.Id)
	l.flagMu.Unlock()
	o := Outcome{DecisionID: decisionID, TaskID: t.Id, State: t.State, Corrected: corrected != "",
		CostEUR: float64(t.GetCost().GetAmount().GetMinorUnits()) / 100, Runtime: t.GetCost().GetRuntime()}
	if f != nil {
		o.Denied, o.Escalated = f.denied, f.escalated
	}
	if t.CreatedAt != nil && t.UpdatedAt != nil {
		o.Latency = t.UpdatedAt.AsTime().Sub(t.CreatedAt.AsTime())
	}
	if o.Runtime == "" {
		o.Runtime = l.runRuntime(ctx, t.Id)
	}
	return l.RecordOutcome(ctx, o)
}

// runRuntime reads the runtime of the task's last run from the task
// engine's runs table (best effort: the column may not exist).
func (l *Learner) runRuntime(ctx context.Context, taskID string) string {
	var rt string
	if err := l.db.QueryRowContext(ctx, `SELECT runtime FROM runs WHERE task_id = ? ORDER BY attempt DESC LIMIT 1`, taskID).Scan(&rt); err != nil {
		return ""
	}
	return rt
}

// SyncCorrections learns router_corrections rows added since the last sync.
func (l *Learner) SyncCorrections(ctx context.Context) error {
	var cursor int64
	if b, err := loadState(ctx, l.db, "corrections_cursor"); err == nil && b != nil {
		fmt.Sscan(string(b), &cursor)
	}
	rows, err := l.db.QueryContext(ctx, `SELECT decision_id, created_at FROM router_corrections WHERE created_at > ? ORDER BY created_at`, cursor)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	type corr struct {
		decision string
		at       int64
	}
	var list []corr
	for rows.Next() {
		var c corr
		if err := rows.Scan(&c.decision, &c.at); err != nil {
			rows.Close()
			return err
		}
		list = append(list, c)
	}
	rows.Close()
	for _, c := range list {
		if c.decision != "" {
			if err := l.recordCorrection(ctx, c.decision); err != nil {
				l.log.Warn("router.learn: correction", "decision", c.decision, "err", err)
			}
		}
		cursor = c.at
	}
	if len(list) > 0 {
		return saveState(ctx, l.db, "corrections_cursor", []byte(fmt.Sprint(cursor)))
	}
	return nil
}

// recordCorrection marks a decision corrected and learns the correction
// (positive for the chosen agent, weak negative for the replaced one).
func (l *Learner) recordCorrection(ctx context.Context, decisionID string) error {
	var was int
	_ = l.db.QueryRowContext(ctx, `SELECT corrected FROM router_learn_outcomes WHERE decision_id = ?`, decisionID).Scan(&was)
	if was != 0 {
		return nil
	}
	if _, err := l.db.ExecContext(ctx, `INSERT INTO router_learn_outcomes (decision_id, created_at, corrected) VALUES (?, ?, 1)
		ON CONFLICT(decision_id) DO UPDATE SET corrected = 1`, decisionID, l.now().UnixMilli()); err != nil {
		return err
	}
	return l.learnDecision(ctx, decisionID, -1, "")
}
