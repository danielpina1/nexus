package vault

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
	"github.com/danielpina1/nexus/backend/internal/vault/teamdef"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Directories of team and skill notes. Team run artefacts (blackboards)
// live under teams/runs/ and are not team definitions.
const (
	TeamsDir  = "teams"
	SkillsDir = "skills"
)

// defFile is one parsed definition note.
type defFile struct {
	rel      string
	markdown string
}

// readDefs reads the top-level *.md files of dir (sorted by name).
func readDefs(v *Vault, dir string) ([]defFile, error) {
	abs := filepath.Join(v.root, dir)
	ents, err := os.ReadDir(abs)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var out []defFile
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(path.Ext(e.Name()), ".md") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(abs, e.Name()))
		if err != nil {
			v.log.Warn("vault: read definition", "path", dir+"/"+e.Name(), "err", err)
			continue
		}
		out = append(out, defFile{rel: dir + "/" + e.Name(), markdown: string(b)})
	}
	return out, nil
}

func touches(paths []string, dirs ...string) bool {
	if paths == nil {
		return true
	}
	for _, p := range paths {
		for _, d := range dirs {
			if strings.HasPrefix(p, d+"/") && !strings.HasPrefix(p, TeamsDir+"/runs/") {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------- skills

// SkillCatalog is the skill catalog backed by skills/*.md
// (kernel.SkillCatalog).
type SkillCatalog struct {
	v *Vault

	mu     sync.RWMutex
	byID   map[string]*skillEntry
	sorted []*skillEntry
}

type skillEntry struct {
	skill    *nexusv1.Skill
	markdown string
}

var _ kernel.SkillCatalog = (*SkillCatalog)(nil)

// NewSkillCatalog loads skills and reloads them when skill notes change.
func NewSkillCatalog(ctx context.Context, v *Vault) (*SkillCatalog, error) {
	c := &SkillCatalog{v: v, byID: map[string]*skillEntry{}}
	if err := c.Reload(ctx); err != nil {
		return nil, err
	}
	v.OnChange(func(paths []string) {
		if touches(paths, SkillsDir) {
			if err := c.Reload(context.Background()); err != nil {
				v.log.Warn("vault: reload skills", "err", err)
			}
		}
	})
	return c, nil
}

// Reload re-reads skills/*.md.
func (c *SkillCatalog) Reload(context.Context) error {
	files, err := readDefs(c.v, SkillsDir)
	if err != nil {
		return err
	}
	byID := map[string]*skillEntry{}
	var list []*skillEntry
	for _, f := range files {
		s := teamdef.ParseSkill(f.rel, []byte(f.markdown))
		e := &skillEntry{skill: s, markdown: f.markdown}
		if prev, dup := byID[s.Id]; dup {
			s.Errors = append(s.Errors, fmt.Sprintf("duplicate id %q (also in %s)", s.Id, prev.skill.GetNote().GetPath()))
			s.Enabled = false
		} else {
			byID[s.Id] = e
		}
		list = append(list, e)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].skill.Id < list[j].skill.Id })
	c.mu.Lock()
	c.byID, c.sorted = byID, list
	c.mu.Unlock()
	return nil
}

// ListSkills implements kernel.SkillCatalog.
func (c *SkillCatalog) ListSkills(_ context.Context, includeDisabled bool) ([]*nexusv1.Skill, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []*nexusv1.Skill{}
	for _, e := range c.sorted {
		if includeDisabled || e.skill.Enabled {
			out = append(out, e.skill)
		}
	}
	return out, nil
}

// GetSkill implements kernel.SkillCatalog.
func (c *SkillCatalog) GetSkill(_ context.Context, id string) (*nexusv1.Skill, string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byID[id]
	if !ok {
		return nil, "", fmt.Errorf("%w: skill %q", ErrNotFound, id)
	}
	return e.skill, e.markdown, nil
}

// UpsertSkill validates and writes skills/<id>.md.
func (c *SkillCatalog) UpsertSkill(ctx context.Context, markdown, commitMessage string) (*nexusv1.Skill, error) {
	id := strings.TrimSpace(note.Parse("", []byte(markdown)).ID)
	if id == "" {
		return nil, &ValidationError{Problems: []string{"missing id"}}
	}
	rel := SkillsDir + "/" + id + ".md"
	c.mu.RLock()
	if e, ok := c.byID[id]; ok {
		rel = e.skill.GetNote().GetPath()
	}
	c.mu.RUnlock()
	s := teamdef.ParseSkill(rel, []byte(markdown))
	if len(s.Errors) > 0 {
		return nil, &ValidationError{Problems: s.Errors}
	}
	if commitMessage == "" {
		commitMessage = "skill: update " + id
	}
	if _, err := c.v.WriteNote(ctx, rel, []byte(markdown), commitMessage, ""); err != nil {
		return nil, err
	}
	if err := c.Reload(ctx); err != nil {
		return nil, err
	}
	got, _, err := c.GetSkill(ctx, id)
	return got, err
}

// ---------------------------------------------------------------- teams

// TeamCatalog is the team catalog backed by teams/*.md (kernel.TeamCatalog).
// Member and orchestrator agent ids are checked against the agent catalog:
// unknown agents disable the team (errors), disabled ones are warnings.
type TeamCatalog struct {
	v      *Vault
	agents kernel.AgentCatalog

	mu     sync.RWMutex
	byID   map[string]*teamEntry
	sorted []*teamEntry
}

type teamEntry struct {
	team     *nexusv1.Team
	mission  string
	markdown string
}

var _ kernel.TeamCatalog = (*TeamCatalog)(nil)

// NewTeamCatalog loads teams and reloads them when team or agent notes change.
func NewTeamCatalog(ctx context.Context, v *Vault, agents kernel.AgentCatalog) (*TeamCatalog, error) {
	c := &TeamCatalog{v: v, agents: agents, byID: map[string]*teamEntry{}}
	if err := c.Reload(ctx); err != nil {
		return nil, err
	}
	v.OnChange(func(paths []string) {
		if touches(paths, TeamsDir, AgentsDir) {
			if err := c.Reload(context.Background()); err != nil {
				v.log.Warn("vault: reload teams", "err", err)
			}
		}
	})
	return c, nil
}

// Reload re-reads teams/*.md.
func (c *TeamCatalog) Reload(ctx context.Context) error {
	files, err := readDefs(c.v, TeamsDir)
	if err != nil {
		return err
	}
	byID := map[string]*teamEntry{}
	var list []*teamEntry
	for _, f := range files {
		t, mission := teamdef.Parse(f.rel, []byte(f.markdown))
		c.crossCheck(ctx, t)
		e := &teamEntry{team: t, mission: mission, markdown: f.markdown}
		if prev, dup := byID[t.Id]; dup {
			t.Errors = append(t.Errors, fmt.Sprintf("duplicate id %q (also in %s)", t.Id, prev.team.GetNote().GetPath()))
			t.Enabled = false
		} else {
			byID[t.Id] = e
		}
		if len(t.Errors) > 0 {
			c.v.log.Warn("vault: invalid team note", "path", f.rel, "errors", t.Errors)
		}
		list = append(list, e)
	}
	// Nested teams: unknown team members and cycles disable the team.
	all := map[string]*nexusv1.Team{}
	for id, e := range byID {
		all[id] = e.team
	}
	for _, e := range list {
		checkTeamRefs(e.team, all)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].team.Id < list[j].team.Id })
	c.mu.Lock()
	c.byID, c.sorted = byID, list
	c.mu.Unlock()
	return nil
}

// crossCheck validates agent references.
func (c *TeamCatalog) crossCheck(ctx context.Context, t *nexusv1.Team) {
	if c.agents == nil {
		return
	}
	check := func(what, id string) {
		if strings.HasPrefix(id, teamdef.TeamRefPrefix) {
			return // nested team: checked by checkTeamRefs
		}
		a, _, _, err := c.agents.GetAgent(ctx, id)
		switch {
		case err != nil:
			t.Errors = append(t.Errors, fmt.Sprintf("%s: unknown agent %q", what, id))
		case !a.GetEnabled():
			t.Warnings = append(t.Warnings, fmt.Sprintf("%s: agent %q is disabled", what, id))
		}
	}
	if t.OrchestratorAgentId != "" {
		check("orchestrator", t.OrchestratorAgentId)
	}
	for _, m := range t.Members {
		if m.AgentId != "" {
			check("member "+m.Role, m.AgentId)
		}
	}
	if len(t.Errors) > 0 {
		t.Enabled = false
	}
}

// ListTeams implements kernel.TeamCatalog.
func (c *TeamCatalog) ListTeams(_ context.Context, includeDisabled bool) ([]*nexusv1.Team, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []*nexusv1.Team{}
	for _, e := range c.sorted {
		if includeDisabled || e.team.Enabled {
			out = append(out, e.team)
		}
	}
	return out, nil
}

// GetTeam implements kernel.TeamCatalog.
func (c *TeamCatalog) GetTeam(_ context.Context, id string) (*nexusv1.Team, string, string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byID[id]
	if !ok {
		return nil, "", "", fmt.Errorf("%w: team %q", ErrNotFound, id)
	}
	return e.team, e.mission, e.markdown, nil
}

// UpsertTeam validates the Markdown and (unless validateOnly) writes
// teams/<id>.md. With validateOnly the parsed team is returned with its
// errors and warnings instead of a validation error.
func (c *TeamCatalog) UpsertTeam(ctx context.Context, markdown, commitMessage string, validateOnly bool) (*nexusv1.Team, error) {
	id := strings.TrimSpace(note.Parse("", []byte(markdown)).ID)
	rel := TeamsDir + "/" + id + ".md"
	c.mu.RLock()
	if e, ok := c.byID[id]; ok && id != "" {
		rel = e.team.GetNote().GetPath()
	}
	c.mu.RUnlock()
	t, _ := teamdef.Parse(rel, []byte(markdown))
	if id == "" {
		t.Errors = append([]string{"missing id"}, t.Errors...)
		t.Enabled = false
	}
	c.crossCheck(ctx, t)
	all := map[string]*nexusv1.Team{}
	c.mu.RLock()
	for tid, e := range c.byID {
		all[tid] = e.team
	}
	c.mu.RUnlock()
	if id != "" {
		all[id] = t
	}
	checkTeamRefs(t, all)
	if validateOnly {
		return t, nil
	}
	if len(t.Errors) > 0 {
		return nil, &ValidationError{Problems: t.Errors}
	}
	if commitMessage == "" {
		commitMessage = "team: update " + id
	}
	if _, err := c.v.WriteNote(ctx, rel, []byte(markdown), commitMessage, ""); err != nil {
		return nil, err
	}
	if err := c.Reload(ctx); err != nil {
		return nil, err
	}
	got, _, _, err := c.GetTeam(ctx, id)
	return got, err
}

// ---------------------------------------------------------------- AgentService skill RPCs

func (h AgentHandler) skills() (*SkillCatalog, error) {
	if h.S == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("skill catalog not available"))
	}
	return h.S, nil
}

func (h AgentHandler) ListSkills(ctx context.Context, r *connect.Request[nexusv1.ListSkillsRequest]) (*connect.Response[nexusv1.ListSkillsResponse], error) {
	sc, err := h.skills()
	if err != nil {
		return nil, err
	}
	list, err := sc.ListSkills(ctx, r.Msg.GetIncludeDisabled())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.ListSkillsResponse{Skills: list}), nil
}

func (h AgentHandler) GetSkill(ctx context.Context, r *connect.Request[nexusv1.GetSkillRequest]) (*connect.Response[nexusv1.GetSkillResponse], error) {
	sc, err := h.skills()
	if err != nil {
		return nil, err
	}
	s, md, err := sc.GetSkill(ctx, r.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.GetSkillResponse{Skill: s, Markdown: md}), nil
}

func (h AgentHandler) UpsertSkill(ctx context.Context, r *connect.Request[nexusv1.UpsertSkillRequest]) (*connect.Response[nexusv1.UpsertSkillResponse], error) {
	sc, err := h.skills()
	if err != nil {
		return nil, err
	}
	s, err := sc.UpsertSkill(ctx, r.Msg.GetMarkdown(), r.Msg.GetCommitMessage())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&nexusv1.UpsertSkillResponse{Skill: s}), nil
}

// checkTeamRefs validates a team's nested-team members ("team:<id>")
// against all teams (t included): unknown teams are errors, disabled ones
// warnings, and a member chain that leads back to t is an error (a team run
// cannot contain itself, directly or indirectly).
func checkTeamRefs(t *nexusv1.Team, all map[string]*nexusv1.Team) {
	bad := false
	for _, m := range t.Members {
		sub := teamdef.MemberTeam(m)
		if sub == "" || sub == t.Id { // self-reference: reported by Parse
			continue
		}
		st, ok := all[sub]
		if !ok {
			t.Errors = append(t.Errors, fmt.Sprintf("member %s: unknown team %q", m.Role, sub))
			bad = true
			continue
		}
		if !st.GetEnabled() && len(st.GetErrors()) == 0 {
			t.Warnings = append(t.Warnings, fmt.Sprintf("member %s: team %q is disabled", m.Role, sub))
		}
		if path := teamCycle(sub, t.Id, all, map[string]bool{}); path != nil {
			t.Errors = append(t.Errors, fmt.Sprintf("member %s: nested teams form a cycle (%s)", m.Role,
				strings.Join(append([]string{t.Id}, path...), " → ")))
			bad = true
		}
	}
	if bad {
		t.Enabled = false
	}
}

// teamCycle returns the chain of team ids from `from` to `target` through
// nested-team members, or nil when target is not reachable.
func teamCycle(from, target string, all map[string]*nexusv1.Team, seen map[string]bool) []string {
	if from == target {
		return []string{target}
	}
	if seen[from] {
		return nil
	}
	seen[from] = true
	t, ok := all[from]
	if !ok {
		return nil
	}
	for _, m := range t.Members {
		if sub := teamdef.MemberTeam(m); sub != "" {
			if rest := teamCycle(sub, target, all, seen); rest != nil {
				return append([]string{from}, rest...)
			}
		}
	}
	return nil
}
