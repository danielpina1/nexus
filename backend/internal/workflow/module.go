// Package workflow is the hub workflow engine: it parses workflows/*.md
// vault notes (docs/WORKFLOWS.md), starts runs from triggers, intents, the
// workflow.run tool or the service API, and executes their steps (agent,
// tool, if, foreach, parallel, approve, ask, wait, workflow, do-groups).
//
// Module "workflow" (hub, default enabled). Requires store, events and tasks.
// Provides SvcWorkflows ("workflows", *Service implementing Workflows) and
// the hub tools workflow.run, workflow.list and — when no other module
// registers it — nexus.status. Optional services resolved per call: vault
// (workflow directory), approvals, dispatcher.
//
// Runs. The task engine has no way to create a root task without
// dispatching it to an agent, so a run is NOT a task: it is a row in
// workflow_runs (id "wfr_…") with one workflow_steps row per executed step
// (path "id", "loop[2]/inner", "group/inner"). Agent steps create ordinary
// root tasks (source "workflow", params workflow_id / workflow_run_id /
// workflow_step, idempotency key "<run>/<path>#<attempt>"). A finished root
// run publishes a Notice (alert on failure; result on success when
// notify_success or dry run); ask steps publish task-less Question events
// (Event.run_id = run id) answered through Service.Answer.
//
// Resume. Runs in state running are resumed at Start from the definition
// snapshot stored with the run: finished steps are restored from their rows,
// in-flight agent/approval/question/child-workflow steps re-attach to their
// task/approval/question/run, waits continue from their original start.
// An external tool (class other than read/write_internal/compute) that was
// in flight when the hub stopped is failed as "outcome unknown"; Retry
// re-runs it with the same idempotency key (ToolCall.ID).
//
// Config (nexus.yaml):
//
//	workflow:
//	  dir: ""                # default <vault>/workflows
//	  reload_interval: 3s    # directory polling
//	  poll_interval: 2s      # waiting steps re-check (events also wake them)
//	  notify_success: false
//	  progress_notices: false
package workflow

import (
	"context"
	"fmt"

	"github.com/danielpina1/nexus/backend/kernel"
)

func init() { kernel.RegisterModule(func() kernel.Module { return &Module{} }) }

// Module is the workflow module.
type Module struct {
	Service *Service
}

func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "workflow",
		Description:    "Workflow engine: workflows/*.md notes, triggers, steps, approvals, resume",
		Side:           kernel.SideHub,
		Requires:       []string{"store", "events", "tasks"},
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("workflow: config: %w", err)
	}
	st, err := kernel.Resolve[kernel.Store](h, kernel.SvcStore)
	if err != nil {
		return err
	}
	s, err := NewService(h, st, cfg, nil)
	if err != nil {
		return err
	}
	for _, t := range s.hubTools() {
		if err := h.Tools().Register(t); err != nil {
			return fmt.Errorf("workflow: %w", err)
		}
	}
	if _, taken := h.Tools().Get("nexus.status"); !taken {
		if err := h.Tools().Register(s.statusTool()); err != nil {
			return fmt.Errorf("workflow: %w", err)
		}
	}
	m.Service = s
	h.Provide(SvcWorkflows, s)
	return nil
}

func (m *Module) Start(context.Context) error { return m.Service.Run() }

func (m *Module) Stop(ctx context.Context) error { return m.Service.Stop(ctx) }
