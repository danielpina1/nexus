package architect

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func schema(s string) json.RawMessage { return json.RawMessage(s) }

// Tools returns the architect's hub tools.
func (s *Service) Tools() []kernel.Tool {
	return []kernel.Tool{
		kernel.ToolFunc{S: kernel.ToolSpec{Name: "architect.list_tools", ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ,
			Description: "List the tools an agent can be given (live hub tools and node tool groups) with their action classes.",
			InputSchema: schema(`{"type":"object","properties":{}}`)}, F: s.toolListTools},
		kernel.ToolFunc{S: kernel.ToolSpec{Name: "architect.list_agents", ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ,
			Description: "List existing agents (id, name, description, tools) so a new agent does not duplicate one.",
			InputSchema: schema(`{"type":"object","properties":{}}`)}, F: s.toolListAgents},
		kernel.ToolFunc{S: kernel.ToolSpec{Name: "architect.validate", ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ,
			Description: "Validate and auto-fix draft notes (agent / workflow / team markdown). Returns the fixed notes, the fixes applied and remaining problems.",
			InputSchema: schema(`{"type":"object","properties":{"agent_markdown":{"type":"string"},"workflow_markdown":{"type":"string"},"team_markdown":{"type":"string"},"update_agent_id":{"type":"string"}}}`)},
			F: s.toolValidate},
		kernel.ToolFunc{S: kernel.ToolSpec{Name: "architect.tune", ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
			Description: "Agent review: find agents with poor recent outcomes (failures, corrections) and draft improved versions for the owner to review. Nothing is published without approval.",
			InputSchema: schema(`{"type":"object","properties":{"days":{"type":"integer"},"max_drafts":{"type":"integer"}}}`)},
			F: s.toolTune},
	}
}

func (s *Service) toolListTools(_ context.Context, _ kernel.ToolCall) (kernel.ToolResult, error) {
	type tool struct {
		Name        string `json:"name"`
		Class       string `json:"class"`
		Description string `json:"description,omitempty"`
		Node        bool   `json:"node,omitempty"`
		CodingOnly  bool   `json:"coding_only,omitempty"`
	}
	var out []tool
	for _, t := range s.h.Tools().List() {
		if strings.HasPrefix(t.Name, "architect.") {
			continue
		}
		out = append(out, tool{Name: t.Name, Class: className(t.ActionClass), Description: oneLine(t.Description, 200), CodingOnly: isCodeOnly(t.Name)})
	}
	for name, c := range KnownNodeTools {
		out = append(out, tool{Name: name, Class: className(c), Node: true, CodingOnly: isCodeOnly(name + ".")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return kernel.ToolOutput(map[string]any{"tools": out,
		"note": "anthrex.* tools are only for coding agents (runtime anthrex or claude/codex coding: true)"})
}

func (s *Service) toolListAgents(ctx context.Context, _ kernel.ToolCall) (kernel.ToolResult, error) {
	cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents)
	if !ok {
		return kernel.ToolOutput(map[string]any{"agents": []any{}})
	}
	list, err := cat.ListAgents(ctx, false)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	var out []map[string]any
	for _, a := range list {
		out = append(out, map[string]any{"id": a.Id, "name": a.Name, "description": a.Description, "tools": a.Tools,
			"tier": strings.ToLower(strings.TrimPrefix(a.Tier.String(), "TIER_"))})
	}
	return kernel.ToolOutput(map[string]any{"agents": out})
}

func (s *Service) toolValidate(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		AgentMarkdown    string `json:"agent_markdown"`
		WorkflowMarkdown string `json:"workflow_markdown"`
		TeamMarkdown     string `json:"team_markdown"`
		UpdateAgentID    string `json:"update_agent_id"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, err
	}
	var notes []*nexusv1.DraftNote
	for kind, md := range map[string]string{"agent": in.AgentMarkdown, "workflow": in.WorkflowMarkdown, "team": in.TeamMarkdown} {
		if strings.TrimSpace(md) != "" {
			notes = append(notes, &nexusv1.DraftNote{Kind: kind, Markdown: md})
		}
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Kind < notes[j].Kind })
	res := Validate(s.BuildEnv(ctx, in.UpdateAgentID), notes)
	out := map[string]any{"fixes": res.Fixes, "problems": res.Problems, "valid": len(res.Problems) == 0}
	for _, n := range res.Notes {
		out[n.Kind+"_markdown"] = n.Markdown
	}
	return kernel.ToolOutput(out)
}

func (s *Service) toolTune(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		Days      int `json:"days"`
		MaxDrafts int `json:"max_drafts"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, err
	}
	rep, err := s.Tune(ctx, in.Days, in.MaxDrafts)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	return kernel.ToolOutput(rep)
}

// TuneReport is what architect.tune returns.
type TuneReport struct {
	Reviewed int          `json:"reviewed"`
	Drafts   []TuneDraft  `json:"drafts"`
	Skipped  []string     `json:"skipped,omitempty"`
	Summary  string       `json:"summary"`
	Stats    []AgentStats `json:"stats,omitempty"`
}

// TuneDraft is one improvement draft started by Tune.
type TuneDraft struct {
	AgentID string `json:"agent_id"`
	DraftID string `json:"draft_id"`
	Reason  string `json:"reason"`
}

// AgentStats are an agent's recent outcomes.
type AgentStats struct {
	AgentID     string   `json:"agent_id"`
	Tasks       int      `json:"tasks"`
	Failed      int      `json:"failed"`
	Corrections int      `json:"corrections"`
	Failures    []string `json:"failures,omitempty"` // summaries of failed tasks
	CorrectedTo []string `json:"corrected_to,omitempty"`
}

// Poor reports whether the agent needs a review.
func (a AgentStats) poor(cfg Config) (bool, string) {
	if a.Tasks >= cfg.TuneMinTasks && float64(a.Failed)/float64(a.Tasks) >= cfg.TuneFailRate {
		return true, fmt.Sprintf("%d of %d tasks failed", a.Failed, a.Tasks)
	}
	if a.Corrections >= 2 {
		return true, fmt.Sprintf("%d routing corrections away from it", a.Corrections)
	}
	return false, ""
}

// Tune is the periodic agent review: for agents with poor outcomes in the
// window it drafts improved versions (Draft with update_agent_id) with the
// evidence as context. Drafts still need the owner's review and approval.
func (s *Service) Tune(ctx context.Context, days, maxDrafts int) (*TuneReport, error) {
	window := s.cfg.TuneWindow
	if days > 0 {
		window = timeDays(days)
	}
	if maxDrafts <= 0 {
		maxDrafts = s.cfg.TuneMaxDrafts
	}
	stats, err := s.agentStats(ctx, s.now().Add(-window))
	if err != nil {
		return nil, err
	}
	open := map[string]bool{}
	for _, d := range mustList(s.list(ctx, []nexusv1.DraftState{stDrafting, stReady, stInvalid, stTesting, stPending}, 500)) {
		if d.UpdateAgentId != "" {
			open[d.UpdateAgentId] = true
		}
	}
	rep := &TuneReport{Reviewed: len(stats), Stats: stats}
	for _, st := range stats {
		poor, why := st.poor(s.cfg)
		if !poor {
			continue
		}
		if open[st.AgentID] || st.AgentID == s.cfg.Agent {
			rep.Skipped = append(rep.Skipped, st.AgentID+" (a draft is already open)")
			continue
		}
		if len(rep.Drafts) >= maxDrafts {
			rep.Skipped = append(rep.Skipped, st.AgentID+" (max drafts reached)")
			continue
		}
		var ctxb strings.Builder
		fmt.Fprintf(&ctxb, "Agent review: %s. ", why)
		if len(st.Failures) > 0 {
			fmt.Fprintf(&ctxb, "Failed task summaries: %s. ", strings.Join(st.Failures, " | "))
		}
		if len(st.CorrectedTo) > 0 {
			fmt.Fprintf(&ctxb, "The owner re-routed its tasks to: %s (tighten its description/examples so the router stops picking it for those). ",
				strings.Join(st.CorrectedTo, ", "))
		}
		d, err := s.StartDraft(ctx, DraftInput{Request: "Improve agent " + st.AgentID + " based on its recent outcomes",
			Kind: nexusv1.DraftKind_DRAFT_KIND_AGENT, Context: ctxb.String(), UpdateAgentID: st.AgentID})
		if err != nil {
			rep.Skipped = append(rep.Skipped, st.AgentID+" ("+err.Error()+")")
			continue
		}
		rep.Drafts = append(rep.Drafts, TuneDraft{AgentID: st.AgentID, DraftID: d.Id, Reason: why})
	}
	switch len(rep.Drafts) {
	case 0:
		rep.Summary = fmt.Sprintf("Reviewed %d agents; none needs changes.", rep.Reviewed)
	default:
		var ids []string
		for _, d := range rep.Drafts {
			ids = append(ids, d.AgentID+" ("+d.Reason+")")
		}
		rep.Summary = fmt.Sprintf("Reviewed %d agents; drafted improvements for %s. Review them in Agents → Architect.", rep.Reviewed, strings.Join(ids, ", "))
	}
	return rep, nil
}

// agentStats aggregates tasks (and router corrections, when the router's
// table exists) since a time, per agent.
func (s *Service) agentStats(ctx context.Context, since timeT) ([]AgentStats, error) {
	te, ok := lookup[kernel.TaskEngine](s.h, kernel.SvcTasks)
	if !ok {
		return nil, fmt.Errorf("architect: the tasks module is not enabled")
	}
	by := map[string]*AgentStats{}
	get := func(id string) *AgentStats {
		a, ok := by[id]
		if !ok {
			a = &AgentStats{AgentID: id}
			by[id] = a
		}
		return a
	}
	token := ""
	for page := 0; page < 50; page++ {
		list, next, err := te.ListTasks(ctx, &nexusv1.ListTasksRequest{Page: &nexusv1.PageRequest{PageSize: 200, PageToken: token},
			States: []nexusv1.TaskState{nexusv1.TaskState_TASK_STATE_SUCCEEDED, nexusv1.TaskState_TASK_STATE_FAILED}})
		if err != nil {
			return nil, err
		}
		older := false
		for _, t := range list {
			if t.AgentId == "" || strings.Contains(t.AgentId, ":") {
				continue
			}
			if t.UpdatedAt != nil && t.UpdatedAt.AsTime().Before(since) {
				older = true
				continue
			}
			a := get(t.AgentId)
			a.Tasks++
			if t.State == nexusv1.TaskState_TASK_STATE_FAILED {
				a.Failed++
				if len(a.Failures) < 5 && t.Summary != "" {
					a.Failures = append(a.Failures, oneLine(t.Summary, 160))
				}
			}
		}
		if next == "" || len(list) == 0 || older {
			break
		}
		token = next
	}
	rows, err := s.db.QueryContext(ctx, `SELECT from_agent, to_agent FROM router_corrections WHERE created_at >= ?`, since.UnixMilli())
	if err == nil {
		var pairs [][2]string
		for rows.Next() {
			var f, t string
			if rows.Scan(&f, &t) == nil && f != "" {
				pairs = append(pairs, [2]string{f, t})
			}
		}
		rows.Close()
		for _, p := range pairs {
			a := get(p[0])
			a.Corrections++
			a.CorrectedTo = append(a.CorrectedTo, p[1])
		}
	}
	out := make([]AgentStats, 0, len(by))
	for _, a := range by {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out, nil
}

// DraftFromCommand starts a draft for a command-bar request that no agent
// fits (the "Create a new agent for this" choice).
func (s *Service) DraftFromCommand(ctx context.Context, input string, c *nexusv1.CommandContext) (*nexusv1.AgentDraft, error) {
	note := "Requested from the command bar"
	if src := c.GetSource(); src != "" {
		note += " (" + src + ")"
	}
	return s.StartDraft(ctx, DraftInput{Request: input, Kind: nexusv1.DraftKind_DRAFT_KIND_AUTO, Context: note + "; no existing agent fitted."})
}

type timeT = time.Time

func timeDays(d int) time.Duration { return time.Duration(d) * 24 * time.Hour }
