// Package teamdef parses team notes (vault teams/*.md) and skill notes
// (vault skills/*.md). See docs/SWARMS.md for the formats.
//
// Like agentdef, parsing never fails: problems are reported in Errors and
// such definitions are returned with Enabled=false. Cross references (agent
// ids) are checked by the catalog, which knows the agents.
package teamdef

import (
	"fmt"
	"math"
	"path"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/agentdef"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// Roles with a fixed meaning.
const (
	RoleOrchestrator = "orchestrator"
	RoleJudge        = "judge"
	RoleCritic       = "critic"
)

// TeamRefPrefix marks a member that is another team ("team:<id>"): it runs
// as a nested team run within the parent run's limits (docs/SWARMS.md §3).
const TeamRefPrefix = "team:"

// MemberTeam returns the team id of a nested-team member ("" for agents).
func MemberTeam(m *nexusv1.TeamMember) string {
	id, ok := strings.CutPrefix(m.GetAgentId(), TeamRefPrefix)
	if !ok {
		return ""
	}
	return id
}

// MaxMemberCount bounds a member's worker pool.
const MaxMemberCount = 20

// Strategies maps `strategy:` values to the enum.
var Strategies = map[string]nexusv1.TeamStrategy{
	"":             nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL,
	"hierarchical": nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL,
	"parallel":     nexusv1.TeamStrategy_TEAM_STRATEGY_PARALLEL,
	"pipeline":     nexusv1.TeamStrategy_TEAM_STRATEGY_PIPELINE,
	"debate":       nexusv1.TeamStrategy_TEAM_STRATEGY_DEBATE,
	"auto":         nexusv1.TeamStrategy_TEAM_STRATEGY_AUTO,
}

// StrategyName is the lower-case name of a strategy ("hierarchical").
func StrategyName(s nexusv1.TeamStrategy) string {
	if s == nexusv1.TeamStrategy_TEAM_STRATEGY_UNSPECIFIED {
		return "hierarchical"
	}
	return strings.ToLower(strings.TrimPrefix(s.String(), "TEAM_STRATEGY_"))
}

// Parse parses a team note; p is the vault-relative path. The returned
// mission is the note body.
func Parse(p string, src []byte) (*nexusv1.Team, string) {
	n := note.Parse(p, src)
	t := &nexusv1.Team{Enabled: true, Blackboard: true, DebateRounds: 2}
	var errs, warns []string
	addErr := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	if n.FrontmatterErr != nil {
		addErr("%v", n.FrontmatterErr)
	}
	if !n.HasFrontmatter {
		addErr("missing frontmatter")
	}
	fm := n.Frontmatter
	if n.Type != "" && n.Type != "team" {
		addErr("type is %q, want \"team\"", n.Type)
	}
	t.Id = strings.TrimSpace(n.ID)
	if t.Id == "" {
		t.Id = strings.TrimSuffix(path.Base(strings.ReplaceAll(p, "\\", "/")), ".md")
		if t.Id == "" || t.Id == "." {
			addErr("missing id")
		}
	}
	if t.Id != "" && !agentdef.ValidID(t.Id) {
		addErr("invalid id %q (use lower-case letters, digits, '-', '_' or '.')", t.Id)
	}
	t.Name = agentdef.Str(fm["name"])
	if t.Name == "" {
		t.Name = t.Id
	}
	t.Description = strings.Join(strings.Fields(agentdef.Str(fm["description"])), " ")
	if t.Description == "" {
		addErr("missing description (the router needs it)")
	}
	t.Icon = agentdef.Str(fm["icon"])

	if v, ok := fm["strategy"]; ok && v != nil {
		s, known := Strategies[strings.ToLower(agentdef.Str(v))]
		if !known {
			addErr("invalid strategy %q (hierarchical | parallel | pipeline | debate | auto)", agentdef.Str(v))
		}
		t.Strategy = s
	} else {
		t.Strategy = nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL
	}

	switch o := fm["orchestrator"].(type) {
	case nil:
	case string:
		t.OrchestratorAgentId = strings.TrimSpace(o)
		if !agentdef.ValidID(t.OrchestratorAgentId) {
			addErr("invalid orchestrator agent id %q", o)
		}
	case map[string]any:
		if id := agentdef.Str(o["agent"]); id != "" {
			t.OrchestratorAgentId = id
			if !agentdef.ValidID(id) {
				addErr("invalid orchestrator agent id %q", id)
			}
			break
		}
		io := &nexusv1.TeamOrchestrator{
			Instructions: strings.TrimSpace(agentdef.Str(o["instructions"])),
			Runtime:      agentdef.Str(o["runtime"]),
			Model:        agentdef.Str(o["model"]),
		}
		if io.Instructions == "" {
			addErr("orchestrator: give an agent id or {instructions: …}")
		}
		if io.Runtime != "" && !agentdef.ValidID(io.Runtime) {
			addErr("orchestrator.runtime: invalid runtime %q", io.Runtime)
		}
		if v := o["tier"]; v != nil {
			tier, err := agentdef.ParseTier(agentdef.Str(v))
			if err != nil {
				addErr("orchestrator.tier: %v", err)
			}
			io.Tier = tier
		}
		tools, err := agentdef.StringSlice(o["tools"], "orchestrator.tools")
		if err != nil {
			addErr("%v", err)
		}
		io.Tools = tools
		tags, err := agentdef.NodeTags(o["node"])
		if err != nil {
			addErr("orchestrator.%v", err)
		}
		io.NodeTags = tags
		t.InlineOrchestrator = io
	default:
		addErr("orchestrator must be an agent id or {instructions: …}")
	}
	hasOrch := t.OrchestratorAgentId != "" || t.InlineOrchestrator != nil
	switch t.Strategy {
	case nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL, nexusv1.TeamStrategy_TEAM_STRATEGY_AUTO:
		if !hasOrch {
			addErr("strategy %s needs an orchestrator", StrategyName(t.Strategy))
		}
	}

	members, merrs := parseMembers(fm["members"])
	errs = append(errs, merrs...)
	t.Members = members
	for _, m := range members {
		if sub := MemberTeam(m); sub != "" && sub == t.Id {
			addErr("member %s: a team cannot contain itself", m.Role)
		}
	}
	if len(members) == 0 {
		switch t.Strategy {
		case nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL, nexusv1.TeamStrategy_TEAM_STRATEGY_AUTO:
			warns = append(warns, "no members: the orchestrator can only spawn ephemeral specialists")
		default:
			addErr("strategy %s needs members", StrategyName(t.Strategy))
		}
	}
	if t.Strategy == nexusv1.TeamStrategy_TEAM_STRATEGY_DEBATE {
		debaters := 0
		for _, m := range members {
			if m.Role != RoleJudge {
				debaters += int(m.Count)
			}
		}
		if debaters < 2 && len(members) > 0 {
			addErr("debate needs at least two debaters (members other than the judge, counts included)")
		}
	}

	if v, ok := fm["shared"]; ok && v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			addErr("shared must be a map {blackboard: true}")
		} else if b, ok := m["blackboard"]; ok {
			bb, isBool := b.(bool)
			if !isBool {
				addErr("shared.blackboard must be true or false")
			}
			t.Blackboard = bb
		}
	}
	if v, ok := fm["approvals"]; ok && v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			addErr("approvals must be a map {bundle: true}")
		} else if b, ok := m["bundle"]; ok {
			bb, isBool := b.(bool)
			if !isBool {
				addErr("approvals.bundle must be true or false")
			}
			t.BundleApprovals = bb
		}
	}
	if v, ok := fm["limits"]; ok && v != nil {
		lim, lerrs := ParseLimits(v)
		t.Limits = lim
		errs = append(errs, lerrs...)
	}
	if v, ok := fm["output"]; ok && v != nil {
		st, err := agentdef.SchemaStruct(v)
		if err != nil {
			addErr("output: %v", err)
		}
		t.OutputSchema = st
	}
	if v, ok := fm["debate"]; ok && v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			addErr("debate must be a map {rounds: N}")
		} else if r, ok := m["rounds"]; ok {
			i, err := agentdef.ToInt(r)
			if err != nil || i < 1 || i > 5 {
				addErr("debate.rounds must be between 1 and 5")
			} else {
				t.DebateRounds = int32(i)
			}
		}
	}
	t.Triggers = triggerPhrases(fm["triggers"])
	if v, ok := fm["version"]; ok && v != nil {
		i, err := agentdef.ToInt(v)
		if err != nil || i < 0 || i > math.MaxInt32 {
			addErr("version must be a non-negative integer")
		} else {
			t.Version = int32(i)
		}
	}
	if v, ok := fm["enabled"]; ok && v != nil {
		b, isBool := v.(bool)
		if !isBool {
			addErr("enabled must be true or false")
		} else {
			t.Enabled = b
		}
	}
	t.Note = &nexusv1.NoteRef{Id: t.Id, Path: p, Title: t.Name}
	t.Warnings = warns
	if len(errs) > 0 {
		t.Errors = errs
		t.Enabled = false
	}
	return t, strings.TrimSpace(n.Body)
}

func parseMembers(v any) ([]*nexusv1.TeamMember, []string) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, []string{"members must be a list"}
	}
	var out []*nexusv1.TeamMember
	var errs []string
	roles := map[string]bool{}
	for i, e := range list {
		m := &nexusv1.TeamMember{Count: 1}
		switch t := e.(type) {
		case string:
			m.AgentId = strings.TrimSpace(t)
		case map[string]any:
			m.AgentId = agentdef.Str(t["agent"])
			if team := agentdef.Str(t["team"]); team != "" {
				if m.AgentId != "" {
					errs = append(errs, fmt.Sprintf("members[%d]: give agent or team, not both", i))
				}
				m.AgentId = TeamRefPrefix + team
			}
			m.Role = agentdef.Str(t["role"])
			m.When = strings.Join(strings.Fields(agentdef.Str(t["when"])), " ")
			if c, ok := t["count"]; ok && c != nil {
				n, err := agentdef.ToInt(c)
				if err != nil || n < 1 || n > MaxMemberCount {
					errs = append(errs, fmt.Sprintf("members[%d].count must be between 1 and %d", i, MaxMemberCount))
				} else {
					m.Count = int32(n)
				}
			}
		default:
			errs = append(errs, fmt.Sprintf("members[%d] must be an agent id, team:<id> or {agent | team, role, count, when}", i))
			continue
		}
		if sub, ok := strings.CutPrefix(m.AgentId, TeamRefPrefix); ok {
			sub = strings.TrimSpace(sub)
			m.AgentId = TeamRefPrefix + sub
			if !agentdef.ValidID(sub) {
				errs = append(errs, fmt.Sprintf("members[%d]: invalid team id %q", i, sub))
			}
			if m.Role == "" {
				m.Role = sub
			}
		} else if m.AgentId == "" || !agentdef.ValidID(m.AgentId) {
			errs = append(errs, fmt.Sprintf("members[%d]: invalid agent id %q", i, m.AgentId))
		}
		if m.Role == "" {
			m.Role = m.AgentId
		}
		m.Role = strings.ToLower(m.Role)
		if !agentdef.ValidID(m.Role) {
			errs = append(errs, fmt.Sprintf("members[%d]: invalid role %q (lower-case letters, digits, '-', '_')", i, m.Role))
		}
		if m.Role == RoleOrchestrator {
			errs = append(errs, fmt.Sprintf("members[%d]: role %q is reserved", i, m.Role))
		}
		if roles[m.Role] {
			errs = append(errs, fmt.Sprintf("members[%d]: duplicate role %q (give each member a distinct role)", i, m.Role))
		}
		roles[m.Role] = true
		out = append(out, m)
	}
	return out, errs
}

// ParseLimits parses a team `limits:` map.
func ParseLimits(v any) (*nexusv1.TeamLimits, []string) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, []string{"limits must be a map"}
	}
	lim := &nexusv1.TeamLimits{}
	var errs []string
	for _, k := range agentdef.SortedKeys(m) {
		val := m[k]
		switch k {
		case "max_parallel", "max_tasks", "max_depth", "max_runs":
			i, err := agentdef.ToInt(val)
			if err != nil || i < 0 || i > math.MaxInt32 {
				errs = append(errs, "limits."+k+" must be a non-negative integer")
				continue
			}
			switch k {
			case "max_parallel":
				lim.MaxParallel = int32(i)
			case "max_tasks":
				lim.MaxTasks = int32(i)
			case "max_depth":
				lim.MaxDepth = int32(i)
			case "max_runs":
				lim.MaxRuns = int32(i)
			}
		case "max_cost_eur", "max_cost":
			mo, err := agentdef.ParseEUR(val)
			if err != nil {
				errs = append(errs, "limits."+k+": "+err.Error())
			}
			lim.MaxCost = mo
		case "timeout", "max_runtime":
			d, err := agentdef.ParseDuration(val)
			if err != nil {
				errs = append(errs, "limits."+k+": "+err.Error())
			}
			lim.TimeoutSeconds = int64(d / time.Second)
		}
	}
	return lim, errs
}

// triggerPhrases accepts `triggers: [a, b]` or `triggers: [{phrase: [a, b]}]`.
func triggerPhrases(v any) []string {
	list, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return []string{strings.TrimSpace(s)}
		}
		return nil
	}
	var out []string
	for _, e := range list {
		switch t := e.(type) {
		case string:
			out = append(out, strings.TrimSpace(t))
		case map[string]any:
			ps, _ := agentdef.StringSlice(t["phrase"], "phrase")
			out = append(out, ps...)
		}
	}
	return out
}

// ---------------------------------------------------------------- skills

// ParseSkill parses a skill note (vault skills/<id>.md): frontmatter id,
// name, description, tools; the body is the instructions.
func ParseSkill(p string, src []byte) *nexusv1.Skill {
	n := note.Parse(p, src)
	s := &nexusv1.Skill{Enabled: true}
	var errs []string
	if n.FrontmatterErr != nil {
		errs = append(errs, n.FrontmatterErr.Error())
	}
	if !n.HasFrontmatter {
		errs = append(errs, "missing frontmatter")
	}
	fm := n.Frontmatter
	if n.Type != "" && n.Type != "skill" {
		errs = append(errs, fmt.Sprintf("type is %q, want \"skill\"", n.Type))
	}
	s.Id = strings.TrimSpace(n.ID)
	if s.Id == "" {
		s.Id = strings.TrimSuffix(path.Base(strings.ReplaceAll(p, "\\", "/")), ".md")
	}
	if !agentdef.ValidID(s.Id) {
		errs = append(errs, fmt.Sprintf("invalid id %q", s.Id))
	}
	s.Name = agentdef.Str(fm["name"])
	if s.Name == "" {
		s.Name = s.Id
	}
	s.Description = strings.Join(strings.Fields(agentdef.Str(fm["description"])), " ")
	if s.Description == "" {
		errs = append(errs, "missing description")
	}
	tools, err := agentdef.StringSlice(fm["tools"], "tools")
	if err != nil {
		errs = append(errs, err.Error())
	}
	s.Tools = tools
	s.Instructions = strings.TrimSpace(n.Body)
	if s.Instructions == "" {
		errs = append(errs, "empty body (the body holds the skill's instructions)")
	}
	if v, ok := fm["version"]; ok && v != nil {
		if i, err := agentdef.ToInt(v); err == nil && i >= 0 && i <= math.MaxInt32 {
			s.Version = int32(i)
		}
	}
	if v, ok := fm["enabled"]; ok {
		if b, isBool := v.(bool); isBool {
			s.Enabled = b
		}
	}
	s.Note = &nexusv1.NoteRef{Id: s.Id, Path: p, Title: s.Name}
	if len(errs) > 0 {
		s.Errors = errs
		s.Enabled = false
	}
	return s
}
