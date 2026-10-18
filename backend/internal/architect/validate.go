package architect

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/agentdef"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
	"github.com/danielpina1/nexus/backend/internal/vault/teamdef"
	"github.com/danielpina1/nexus/backend/internal/workflow"
	"github.com/danielpina1/nexus/backend/kernel"
)

// KnownNodeTools are tools that live on nodes (not in the hub registry) and
// may appear in agent allow-lists. Group names ("browser") allow the
// group's tools.
var KnownNodeTools = map[string]nexusv1.ActionClass{
	"browser":    nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL, // can click checkout → spend gated separately
	"fs.read":    nexusv1.ActionClass_ACTION_CLASS_READ,
	"shell.exec": nexusv1.ActionClass_ACTION_CLASS_COMPUTE,
	"node.exec":  nexusv1.ActionClass_ACTION_CLASS_COMPUTE,
	"node.files": nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
	"anthrex":    nexusv1.ActionClass_ACTION_CLASS_COMPUTE, // coding agents only
}

// CodeOnlyPrefixes are tool families reserved for coding agents (the node
// drops them from other agents' runs; see internal/node/codeonly.go).
var CodeOnlyPrefixes = []string{"anthrex."}

// protectedDirs mirror the vault's agent-write protection: agents must not
// write configuration, so a drafted agent's memory.writes may not point there.
var protectedDirs = []string{"agents/", "policies/", "workflows/", "shops/", "teams/", "skills/"}

// requiredPolicy is the minimum per-agent policy every drafted agent
// carries explicitly (docs/ARCHITECT.md): money needs Face ID, external
// writes need a tap.
var requiredPolicy = map[string]nexusv1.TrustLevel{
	"spend":          nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC,
	"write_external": nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
}

// defaultLevels are the global defaults an agent may never loosen.
var defaultLevels = map[string]nexusv1.TrustLevel{
	"read":           nexusv1.TrustLevel_TRUST_LEVEL_AUTO,
	"write_internal": nexusv1.TrustLevel_TRUST_LEVEL_NOTIFY,
	"compute":        nexusv1.TrustLevel_TRUST_LEVEL_AUTO,
	"write_external": nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
	"code_push":      nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
	"spend":          nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC,
	"destructive":    nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC,
	"device_control": nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
}

// Env is what validation checks against.
type Env struct {
	// Tools is the live hub tool registry.
	Tools map[string]kernel.ToolSpec
	// Agents, Teams and Workflows are existing ids.
	Agents, Teams, Workflows map[string]bool
	// Skills are existing skill ids; nil when there is no skills catalog.
	Skills map[string]bool
	// UpdateAgentID may keep its id (an update of an existing agent).
	UpdateAgentID string
	// ExistingNotes maps a note path to its current markdown (for diffs).
	ExistingNotes map[string]string
	// TeamCheck validates a team note (TeamCatalog.UpsertTeam validateOnly).
	TeamCheck func(markdown string) error
}

// toolKnown reports whether name is an allowed allow-list entry.
func (e *Env) toolKnown(name string) bool {
	if _, ok := e.Tools[name]; ok {
		return true
	}
	if _, ok := KnownNodeTools[name]; ok {
		return true
	}
	group := strings.TrimSuffix(name, ".*")
	if _, ok := KnownNodeTools[strings.SplitN(group, ".", 2)[0]]; ok {
		return true
	}
	for t := range e.Tools { // group entry "web" / "web.*"
		if strings.HasPrefix(t, group+".") {
			return true
		}
	}
	return false
}

func (e *Env) toolClass(name string) nexusv1.ActionClass {
	if s, ok := e.Tools[name]; ok {
		return s.ActionClass
	}
	if c, ok := KnownNodeTools[strings.SplitN(strings.TrimSuffix(name, ".*"), ".", 2)[0]]; ok {
		return c
	}
	return nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED
}

func isCodeOnly(tool string) bool {
	t := strings.TrimSuffix(tool, ".*")
	for _, p := range CodeOnlyPrefixes {
		if strings.HasPrefix(t, p) || t == strings.TrimSuffix(p, ".") {
			return true
		}
	}
	return false
}

// isCodingAgent mirrors node.IsCodeAgent on the note's frontmatter.
func isCodingAgent(fm map[string]any) bool {
	if strings.TrimSpace(fmt.Sprint(fm["runtime"])) == "anthrex" {
		return true
	}
	for _, k := range []string{"claude", "codex"} {
		if m, ok := fm[k].(map[string]any); ok {
			if b, ok := m["coding"].(bool); ok && b {
				return true
			}
		}
	}
	return false
}

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(reSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	return s
}

// Result is the outcome of validating a draft's notes.
type Result struct {
	Notes    []*nexusv1.DraftNote
	Fixes    []string
	Problems []string
}

// Validate checks and auto-fixes the notes of a draft. Fixable issues are
// corrected with an explanation in Fixes; the rest are Problems (the draft
// cannot be published while there are any).
func Validate(env *Env, notes []*nexusv1.DraftNote) Result {
	var res Result
	renamed := map[string]string{} // old agent id → new
	agentIDs := map[string]bool{}
	for _, n := range notes {
		if n.Kind == "agent" {
			out, id := validateAgent(env, n, &res)
			if id != "" {
				if id != n.Id && n.Id != "" {
					renamed[n.Id] = id
				}
				agentIDs[id] = true
			}
			res.Notes = append(res.Notes, out)
		}
	}
	for _, n := range notes {
		switch n.Kind {
		case "agent":
		case "workflow":
			res.Notes = append(res.Notes, validateWorkflow(env, n, renamed, agentIDs, &res))
		case "team":
			res.Notes = append(res.Notes, validateTeam(env, n, renamed, &res))
		default:
			res.Problems = append(res.Problems, fmt.Sprintf("unknown note kind %q", n.Kind))
		}
	}
	if len(agentIDs) == 0 && !hasKind(res.Notes, "workflow") && !hasKind(res.Notes, "team") {
		res.Problems = append(res.Problems, "the draft contains no note")
	}
	for _, n := range res.Notes {
		if old, ok := env.ExistingNotes[n.Path]; ok {
			n.Exists = true
			n.Diff = unifiedDiff("a/"+n.Path, "b/"+n.Path, old, n.Markdown)
		} else {
			n.Exists, n.Diff = false, ""
		}
	}
	return res
}

func hasKind(ns []*nexusv1.DraftNote, kind string) bool {
	for _, n := range ns {
		if n.Kind == kind {
			return true
		}
	}
	return false
}

// uniqueID returns id, or id-2, id-3 … when taken.
func uniqueID(id string, taken map[string]bool) string {
	if !taken[id] {
		return id
	}
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s-%d", id, i)
		if !taken[c] {
			return c
		}
	}
}

func validateAgent(env *Env, n *nexusv1.DraftNote, res *Result) (*nexusv1.DraftNote, string) {
	src := []byte(strings.TrimSpace(n.Markdown) + "\n")
	fixes := func(f string, a ...any) { res.Fixes = append(res.Fixes, fmt.Sprintf(f, a...)) }
	probs := func(f string, a ...any) { res.Problems = append(res.Problems, fmt.Sprintf(f, a...)) }
	parsed := note.Parse("", src)
	if !parsed.HasFrontmatter || parsed.FrontmatterErr != nil {
		probs("agent note: frontmatter missing or invalid YAML (%v)", parsed.FrontmatterErr)
		return &nexusv1.DraftNote{Kind: "agent", Id: n.Id, Markdown: string(src)}, ""
	}
	fm := parsed.Frontmatter
	set := map[string]any{}
	var order []string
	put := func(k string, v any) {
		set[k] = v
		order = append(order, k)
	}
	if t := agentdef.Str(fm["type"]); t != "agent" {
		put("type", "agent")
		if t != "" {
			fixes("type %q changed to agent", t)
		}
	}
	id := strings.TrimSpace(parsed.ID)
	if id == "" {
		id = n.Id
	}
	if id == "" {
		id = slug(agentdef.Str(fm["name"]))
	}
	if id != "" && !agentdef.ValidID(id) {
		nid := slug(id)
		fixes("id %q is not a valid id; using %q", id, nid)
		id = nid
	}
	if id == "" {
		probs("agent note: missing id and name")
		return &nexusv1.DraftNote{Kind: "agent", Markdown: string(src)}, ""
	}
	if env.Agents[id] && id != env.UpdateAgentID {
		nid := uniqueID(id, env.Agents)
		fixes("an agent %q already exists; the new agent is %q (to change the existing one, draft an update of it)", id, nid)
		id = nid
	}
	if id != strings.TrimSpace(parsed.ID) {
		put("id", id)
	}
	if agentdef.Str(fm["runtime"]) == "" && fm["runtimes"] == nil {
		put("runtime", "auto")
		fixes("runtime missing; set to auto (the hub picks Claude Code / Codex / Ollama per policies/models.md)")
	}
	if t := agentdef.Str(fm["tier"]); t == "" {
		put("tier", "medium")
		fixes("tier missing; set to medium")
	} else if _, err := agentdef.ParseTier(t); err != nil {
		put("tier", "medium")
		fixes("tier %q is invalid; set to medium", t)
	}

	// Tools: only the live registry and known node tools; no privileged
	// architect tools; code-only tools only for coding agents.
	tools, _ := agentdef.StringSlice(fm["tools"], "tools")
	var kept []string
	maxClass := nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED
	changedTools := false
	for _, t := range tools {
		switch {
		case strings.HasPrefix(t, "architect.") && id != DefaultAgent:
			fixes("removed tool %s (architect tools are reserved for the agent-architect)", t)
			changedTools = true
		case !env.toolKnown(t):
			fixes("removed unknown tool %s (not in the live tool registry or the known node tools)", t)
			changedTools = true
		default:
			kept = append(kept, t)
			if c := env.toolClass(t); c > maxClass {
				maxClass = c
			}
		}
	}
	if changedTools {
		put("tools", kept)
	}
	if !isCodingAgent(fm) {
		for _, t := range kept {
			if isCodeOnly(t) {
				probs("tool %s is reserved for coding agents: give it only to an agent that runs on the anthrex runtime or sets `coding: true` in its claude:/codex: block (non-coding agents never receive anthrex.* tools)", t)
			}
		}
	}

	// Policy: never looser than the defaults; spend/write_external explicit.
	pol := map[string]any{}
	if m, ok := fm["policy"].(map[string]any); ok {
		for k, v := range m {
			pol[k] = v
		}
	} else if fm["policy"] != nil {
		fixes("policy was not a map; replaced")
	}
	polChanged := fm["policy"] == nil
	for _, k := range agentdef.SortedKeys(pol) {
		key := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(k), "-", "_"))
		def, isClass := defaultLevels[key]
		if !isClass {
			continue
		}
		lvl, err := agentdef.ParseTrustLevel(agentdef.Str(pol[k]))
		if err != nil || lvl < def {
			fixes("policy.%s: %v is looser than the default %s; raised (agents may only tighten policy)", k, pol[k], levelName(def))
			pol[k] = levelName(def)
			polChanged = true
		}
	}
	for k, min := range requiredPolicy {
		lvl, err := agentdef.ParseTrustLevel(agentdef.Str(pol[k]))
		if pol[k] == nil || err != nil || lvl < min {
			pol[k] = levelName(min)
			polChanged = true
		}
	}
	if maxClass >= nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE {
		if lvl, err := agentdef.ParseTrustLevel(agentdef.Str(pol["destructive"])); err != nil || lvl < nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
			pol["destructive"] = "biometric"
			polChanged = true
		}
	}
	if polChanged {
		put("policy", pol)
		fixes("policy set to %s", policyText(pol))
	}

	// Memory writes must not target configuration folders.
	if m, ok := fm["memory"].(map[string]any); ok {
		if w := agentdef.Str(m["writes"]); w != "" && isProtected(w) {
			nm := map[string]any{}
			for k, v := range m {
				nm[k] = v
			}
			nm["writes"] = "episodes/" + id + "/"
			put("memory", nm)
			fixes("memory.writes %q is a configuration folder agents cannot write; changed to episodes/%s/", w, id)
		}
	}
	if fm["limits"] == nil {
		put("limits", map[string]any{"max_runtime": "20m", "max_cost_eur": 0.5})
		fixes("limits missing; added max_runtime 20m and max_cost_eur 0.50")
	}
	if skills, _ := agentdef.StringSlice(fm["skills"], "skills"); len(skills) > 0 {
		var ok []string
		for _, sk := range skills {
			if env.Skills != nil && env.Skills[sk] {
				ok = append(ok, sk)
			} else {
				fixes("removed unknown skill %q", sk)
			}
		}
		if len(ok) != len(skills) {
			if len(ok) == 0 {
				put("skills", nil)
			} else {
				put("skills", ok)
			}
		}
	}
	if ex := note.StringList(fm["examples"]); len(ex) == 0 {
		fixes("note: no examples: — 3 to 5 sample requests help the router pick this agent")
	}
	out := src
	if len(set) > 0 {
		b, err := note.SetFields(src, set, order...)
		if err != nil {
			probs("agent note: %v", err)
		} else {
			out = b
		}
	}
	a, instr := agentdef.Parse("agents/"+id+".md", out)
	for _, e := range a.Errors {
		probs("agent %s: %s", id, e)
	}
	if strings.TrimSpace(instr) == "" {
		probs("agent %s: the note body (instructions) is empty", id)
	}
	return &nexusv1.DraftNote{Kind: "agent", Id: id, Path: "agents/" + id + ".md", Markdown: string(out)}, id
}

func levelName(l nexusv1.TrustLevel) string {
	return strings.ToLower(strings.TrimPrefix(l.String(), "TRUST_LEVEL_"))
}

func policyText(p map[string]any) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %v", k, p[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func isProtected(p string) bool {
	p = strings.ToLower(strings.TrimLeft(strings.TrimSpace(p), "/"))
	for _, d := range protectedDirs {
		if strings.HasPrefix(p+"/", d) || strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

func validateWorkflow(env *Env, n *nexusv1.DraftNote, renamed map[string]string, agentIDs map[string]bool, res *Result) *nexusv1.DraftNote {
	src := []byte(strings.TrimSpace(n.Markdown) + "\n")
	parsed := note.Parse("", src)
	id := strings.TrimSpace(parsed.ID)
	if id == "" {
		id = n.Id
	}
	if id == "" {
		id = slug(agentdef.Str(parsed.Frontmatter["name"]))
	}
	set := map[string]any{}
	if !agentdef.ValidID(id) {
		res.Problems = append(res.Problems, fmt.Sprintf("workflow note: invalid or missing id %q", id))
		return &nexusv1.DraftNote{Kind: "workflow", Markdown: string(src)}
	}
	if env.Workflows[id] {
		nid := uniqueID(id, env.Workflows)
		res.Fixes = append(res.Fixes, fmt.Sprintf("a workflow %q already exists; the new workflow is %q", id, nid))
		id = nid
	}
	if id != strings.TrimSpace(parsed.ID) {
		set["id"] = id
	}
	if agentdef.Str(parsed.Frontmatter["type"]) != "workflow" {
		set["type"] = "workflow"
	}
	out := src
	if len(set) > 0 {
		if b, err := note.SetFields(src, set); err == nil {
			out = b
		}
	}
	// Agent steps follow renamed agents.
	s := string(out)
	for old, nw := range renamed {
		s = regexp.MustCompile(`(?m)(\bagent:\s*)`+regexp.QuoteMeta(old)+`\b`).ReplaceAllString(s, "${1}"+nw)
	}
	out = []byte(s)
	w := workflow.Parse("workflows/"+id+".md", out)
	for _, e := range w.Errors {
		res.Problems = append(res.Problems, "workflow "+id+": "+e)
	}
	var walk func(steps []*workflow.Step)
	walk = func(steps []*workflow.Step) {
		for _, st := range steps {
			if st.Agent != "" && !strings.Contains(st.Agent, "{{") && !env.Agents[st.Agent] && !agentIDs[st.Agent] {
				res.Problems = append(res.Problems, fmt.Sprintf("workflow %s step %s: unknown agent %q", id, st.ID, st.Agent))
			}
			if st.Tool != "" && !env.toolKnown(st.Tool) {
				res.Problems = append(res.Problems, fmt.Sprintf("workflow %s step %s: unknown tool %q", id, st.ID, st.Tool))
			}
			walk(st.Do)
			walk(st.Then)
			walk(st.Else)
		}
	}
	walk(w.Steps)
	return &nexusv1.DraftNote{Kind: "workflow", Id: id, Path: "workflows/" + id + ".md", Markdown: string(out)}
}

func validateTeam(env *Env, n *nexusv1.DraftNote, renamed map[string]string, res *Result) *nexusv1.DraftNote {
	src := []byte(strings.TrimSpace(n.Markdown) + "\n")
	parsed := note.Parse("", src)
	id := strings.TrimSpace(parsed.ID)
	if id == "" {
		id = n.Id
	}
	if !agentdef.ValidID(id) {
		res.Problems = append(res.Problems, fmt.Sprintf("team note: invalid or missing id %q", id))
		return &nexusv1.DraftNote{Kind: "team", Markdown: string(src)}
	}
	out := src
	set := map[string]any{}
	if env.Teams[id] {
		nid := uniqueID(id, env.Teams)
		res.Fixes = append(res.Fixes, fmt.Sprintf("a team %q already exists; the new team is %q", id, nid))
		id = nid
	}
	if id != strings.TrimSpace(parsed.ID) {
		set["id"] = id
	}
	if len(set) > 0 {
		if b, err := note.SetFields(src, set); err == nil {
			out = b
		}
	}
	s := string(out)
	for old, nw := range renamed {
		s = regexp.MustCompile(`\b`+regexp.QuoteMeta(old)+`\b`).ReplaceAllString(s, nw)
	}
	out = []byte(s)
	if env.TeamCheck != nil {
		if err := env.TeamCheck(string(out)); err != nil {
			res.Problems = append(res.Problems, "team "+id+": "+err.Error())
		}
	} else if t, _ := teamdef.Parse("teams/"+id+".md", out); len(t.Errors) > 0 {
		for _, e := range t.Errors {
			res.Problems = append(res.Problems, "team "+id+": "+e)
		}
	}
	return &nexusv1.DraftNote{Kind: "team", Id: id, Path: "teams/" + id + ".md", Markdown: string(out)}
}

// BuildEnv gathers the validation environment from the host's services.
func (s *Service) BuildEnv(ctx context.Context, updateAgentID string) *Env {
	env := &Env{Tools: map[string]kernel.ToolSpec{}, Agents: map[string]bool{}, Teams: map[string]bool{},
		Workflows: map[string]bool{}, UpdateAgentID: updateAgentID, ExistingNotes: map[string]string{}}
	for _, t := range s.h.Tools().List() {
		env.Tools[t.Name] = t
	}
	if cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents); ok {
		if list, err := cat.ListAgents(ctx, true); err == nil {
			for _, a := range list {
				env.Agents[a.Id] = true
			}
		}
		if updateAgentID != "" {
			if _, _, md, err := cat.GetAgent(ctx, updateAgentID); err == nil {
				env.ExistingNotes["agents/"+updateAgentID+".md"] = md
			}
		}
	}
	if tc, ok := lookup[kernel.TeamCatalog](s.h, kernel.SvcTeams); ok {
		if list, err := tc.ListTeams(ctx, true); err == nil {
			for _, t := range list {
				env.Teams[t.Id] = true
			}
		}
		env.TeamCheck = func(md string) error {
			_, err := tc.UpsertTeam(ctx, md, "", true)
			return err
		}
	}
	if sc, ok := lookup[kernel.SkillCatalog](s.h, kernel.SvcSkills); ok {
		env.Skills = map[string]bool{}
		if list, err := sc.ListSkills(ctx, false); err == nil {
			for _, sk := range list {
				env.Skills[sk.Id] = true
			}
		}
	}
	if v, ok := s.h.Lookup(workflow.SvcWorkflows); ok {
		if wl, ok := v.(interface {
			List(ctx context.Context) ([]*workflow.Workflow, error)
		}); ok {
			if list, err := wl.List(ctx); err == nil {
				for _, w := range list {
					env.Workflows[w.ID] = true
				}
			}
		}
	}
	return env
}

// ---------------------------------------------------------------- diff

// unifiedDiff is a small line diff (LCS) for approval payloads.
func unifiedDiff(from, to, a, b string) string {
	if a == b {
		return ""
	}
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	n, m := len(x), len(y)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", from, to)
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && x[i] == y[j]:
			sb.WriteString(" " + x[i] + "\n")
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			sb.WriteString("+" + y[j] + "\n")
			j++
		default:
			sb.WriteString("-" + x[i] + "\n")
			i++
		}
	}
	return sb.String()
}
