package vault

import (
	"context"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// startEpisodes subscribes to the event bus (when present) and writes an
// episode note for every top-level task that reaches a final state. Keeping
// this in the vault module means the task engine never depends on the vault.
func (m *Module) startEpisodes(ctx context.Context) {
	bus, err := kernel.Resolve[kernel.EventBus](m.h, kernel.SvcEventBus)
	if err != nil {
		return
	}
	since, err := bus.LastSeq(ctx)
	if err != nil {
		m.h.Logger().Warn("vault: episodes disabled", "err", err)
		return
	}
	w := EpisodeWriter{Vault: m.V}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for ctx.Err() == nil {
			ch, err := bus.Subscribe(ctx, since, kernel.EventFilter{})
			if err != nil {
				return
			}
			for ev := range ch {
				since = ev.GetSeq()
				t := ev.GetTaskUpdated().GetTask()
				if t == nil || t.GetParentId() != "" || !finalState(t.GetState()) {
					continue
				}
				if _, err := w.WriteEpisode(WithRunID(ctx, t.GetCurrentRunId()), t, t.GetSummary()); err != nil {
					m.h.Logger().Warn("vault: write episode", "task", t.GetId(), "err", err)
				}
			}
		}
	}()
}

func finalState(s nexusv1.TaskState) bool {
	switch s {
	case nexusv1.TaskState_TASK_STATE_SUCCEEDED, nexusv1.TaskState_TASK_STATE_FAILED, nexusv1.TaskState_TASK_STATE_CANCELLED:
		return true
	}
	return false
}
