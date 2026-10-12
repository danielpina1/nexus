package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// hubTools returns workflow.run and workflow.list. nexus.status is added by
// the module only when no other module registers it.
func (s *Service) hubTools() []kernel.Tool {
	return []kernel.Tool{
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name:        "workflow.run",
			Description: "Start a workflow (vault note in workflows/) by id with inputs. Returns the run id; with wait=true waits up to 60s for the result.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{
				"workflow_id":{"type":"string"},
				"inputs":{"type":"object"},
				"dry_run":{"type":"boolean","description":"Skip external/spend actions and agent steps; record what would run"},
				"wait":{"type":"boolean"}},"required":["workflow_id"]}`),
			ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
		}, F: s.toolRun},
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name:        "workflow.list",
			Description: "List workflows (id, name, enabled, triggers, inputs).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ,
		}, F: s.toolList},
	}
}

func (s *Service) toolRun(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		WorkflowID string         `json:"workflow_id"`
		Inputs     map[string]any `json:"inputs"`
		DryRun     bool           `json:"dry_run"`
		Wait       bool           `json:"wait"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, err
	}
	if strings.TrimSpace(in.WorkflowID) == "" {
		return kernel.ToolResult{}, errors.New("workflow.run: workflow_id required")
	}
	src := "tool"
	if c.AgentID != "" {
		src = "agent:" + c.AgentID
	}
	key := ""
	if c.ID != "" && c.RunID != "" {
		key = "tool:" + c.RunID + ":" + c.ID
	}
	id, err := s.StartWith(ctx, StartOptions{WorkflowID: in.WorkflowID, Inputs: in.Inputs, Source: src, DryRun: in.DryRun, IdempotencyKey: key})
	if err != nil {
		return kernel.ToolResult{}, err
	}
	out := map[string]any{"run_id": id, "state": RunRunning}
	if in.Wait {
		wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		var run *RunInfo
		_ = s.waitFor(wctx, func(c context.Context) (bool, error) {
			r, err := s.st.getRun(c, id)
			run = r
			return err == nil && runTerminal(r.State), err
		})
		if run != nil {
			out["state"], out["output"], out["error"] = run.State, run.Output, run.Error
		}
	}
	return kernel.ToolOutput(out)
}

func (s *Service) toolList(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
	var out []map[string]any
	for _, w := range s.cat.List() {
		out = append(out, map[string]any{"id": w.ID, "name": w.Name, "enabled": w.Enabled, "valid": w.Valid(),
			"description": w.Description, "triggers": w.Triggers, "inputs": w.Inputs, "errors": w.Errors})
	}
	return kernel.ToolOutput(map[string]any{"workflows": out})
}

// statusTool is "nexus.status": nodes, recent failed tasks, pending
// approvals and workflow runs, for briefs and watchdogs.
func (s *Service) statusTool() kernel.Tool {
	return kernel.ToolFunc{S: kernel.ToolSpec{
		Name:        "nexus.status",
		Description: "Nexus health: nodes (online, cpu, disk), recently failed tasks, pending approvals, running and failed workflow runs.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ,
	}, F: func(ctx context.Context, _ kernel.ToolCall) (kernel.ToolResult, error) {
		out := map[string]any{"at": s.now().UTC().Format(time.RFC3339)}
		if d, err := kernel.Resolve[kernel.Dispatcher](s.h, kernel.SvcDispatcher); err == nil {
			if nodes, err := d.Nodes(ctx); err == nil {
				var ns []map[string]any
				for _, n := range nodes {
					st := n.GetStatus()
					ns = append(ns, map[string]any{"id": n.GetId(), "name": n.GetName(), "online": st.GetOnline(),
						"cpu_percent": st.GetCpuPercent(), "memory_percent": st.GetMemoryPercent(),
						"disk_percent": st.GetDiskPercent(), "active_runs": st.GetActiveRuns()})
				}
				out["nodes"] = ns
			}
		}
		if te, ok := s.tasks(); ok {
			list, _, err := te.ListTasks(ctx, &nexusv1.ListTasksRequest{RootsOnly: true, Page: &nexusv1.PageRequest{PageSize: 10},
				States: []nexusv1.TaskState{nexusv1.TaskState_TASK_STATE_FAILED}})
			if err == nil {
				var ts []map[string]any
				for _, t := range list {
					ts = append(ts, map[string]any{"id": t.GetId(), "title": t.GetTitle(), "agent": t.GetAgentId(), "summary": t.GetSummary()})
				}
				out["failed_tasks"] = ts
			}
		}
		if b, ok := s.approvals(); ok {
			if list, err := b.List(ctx, []nexusv1.ApprovalState{nexusv1.ApprovalState_APPROVAL_STATE_PENDING}); err == nil {
				out["pending_approvals"] = len(list)
			}
		}
		if runs, err := s.st.listRuns(ctx, "", RunRunning, 50); err == nil {
			out["running_workflows"] = len(runs)
		}
		if runs, err := s.st.listRuns(ctx, "", RunFailed, 10); err == nil {
			var rs []map[string]any
			for _, r := range runs {
				rs = append(rs, map[string]any{"run_id": r.ID, "workflow_id": r.WorkflowID, "error": r.Error})
			}
			out["failed_workflow_runs"] = rs
		}
		return kernel.ToolOutput(out)
	}}
}
