package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/schema"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Assignment preparation for complex agents (docs/SWARMS.md §2): the
// effective agent of a run is its note (or an ephemeral definition) with its
// skills merged, its memory reads injected as context, its output schema
// turned into instructions, team tools and briefs added for team members,
// and its per-run limits clamped to what is left of the tree's budget.

// ephPrefix marks ephemeral agent ids (team.spawn, inline orchestrators).
// Agent ids from notes cannot contain ':'.
const ephPrefix = "eph:"

// IsEphemeral reports whether an agent id names an ephemeral agent.
func IsEphemeral(agentID string) bool { return strings.HasPrefix(agentID, ephPrefix) }

var errNoCatalog = errors.New("no agent catalog available (is the vault/agents module enabled?)")

// resolvedAgent is the effective agent of a task.
type resolvedAgent struct {
	agent        *nexusv1.Agent // a copy: tools include skill tools
	instructions string         // note body + skill instructions
	markdown     string         // agent note ("" for ephemeral agents)
	ephemeral    bool
	warnings     []string
}

func (e *Engine) skills() (kernel.SkillCatalog, bool) {
	return lookup[kernel.SkillCatalog](e.h, kernel.SvcSkills)
}

func (e *Engine) teams() (kernel.TeamCatalog, bool) {
	return lookup[kernel.TeamCatalog](e.h, kernel.SvcTeams)
}

func (e *Engine) vault() (kernel.Vault, bool) { return lookup[kernel.Vault](e.h, kernel.SvcVault) }

// resolveAgent returns the effective agent agentID for task taskID.
func (e *Engine) resolveAgent(ctx context.Context, taskID, agentID string) (*resolvedAgent, error) {
	ra := &resolvedAgent{}
	if IsEphemeral(agentID) {
		x, err := e.loadExt(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if len(x.AgentJSON) == 0 {
			return nil, fmt.Errorf("ephemeral agent %q has no definition on task %s", agentID, taskID)
		}
		a := &nexusv1.Agent{}
		if err := protojson.Unmarshal(x.AgentJSON, a); err != nil {
			return nil, fmt.Errorf("ephemeral agent %q: %w", agentID, err)
		}
		ra.agent, ra.instructions, ra.ephemeral = a, x.Instructions, true
	} else {
		cat, ok := e.agents()
		if !ok {
			return nil, errNoCatalog
		}
		a, instr, md, err := cat.GetAgent(ctx, agentID)
		if err != nil {
			return nil, err
		}
		ra.agent, ra.instructions, ra.markdown = proto.Clone(a).(*nexusv1.Agent), instr, md
	}
	e.mergeSkills(ctx, ra)
	return ra, nil
}

// mergeSkills appends each skill's instructions and merges its tools.
func (e *Engine) mergeSkills(ctx context.Context, ra *resolvedAgent) {
	if len(ra.agent.GetSkills()) == 0 {
		return
	}
	sc, ok := e.skills()
	if !ok {
		ra.warnings = append(ra.warnings, "skills are not available (no skill catalog)")
		return
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(ra.instructions))
	for _, id := range ra.agent.Skills {
		sk, _, err := sc.GetSkill(ctx, id)
		if err != nil || !sk.GetEnabled() {
			ra.warnings = append(ra.warnings, fmt.Sprintf("skill %q is unknown or disabled", id))
			continue
		}
		fmt.Fprintf(&b, "\n\n## Skill: %s\n%s", nonEmpty(sk.Name, sk.Id), strings.TrimSpace(sk.Instructions))
		ra.agent.Tools = addTools(ra.agent.Tools, sk.Tools...)
	}
	ra.instructions = strings.TrimSpace(b.String())
}

// addTools appends names not yet in list.
func addTools(list []string, names ...string) []string {
	seen := map[string]bool{}
	for _, n := range list {
		seen[n] = true
	}
	for _, n := range names {
		if n != "" && !seen[n] {
			seen[n] = true
			list = append(list, n)
		}
	}
	return list
}

// ---------------------------------------------------------------- memory

// Memory injection bounds.
const (
	memoryMaxNotes    = 12
	memoryNoteBytes   = 4 << 10
	memoryTotalBytes  = 24 << 10
	memoryFolderNotes = 5
)

// memoryContext reads the agent's `memory.reads` entries from the vault:
// "[[Title]]" or a path/id → that note; "folder/" → its most recently
// changed notes; "search: q" (or "?q") → the best search hits.
func (e *Engine) memoryContext(ctx context.Context, reads []string) []any {
	v, ok := e.vault()
	if !ok || len(reads) == 0 {
		return nil
	}
	var out []any
	total := 0
	seen := map[string]bool{}
	add := func(n *nexusv1.Note) {
		if n == nil || seen[n.GetRef().GetPath()] || len(out) >= memoryMaxNotes || total >= memoryTotalBytes {
			return
		}
		seen[n.GetRef().GetPath()] = true
		body := truncate(strings.TrimSpace(n.GetBody()), memoryNoteBytes)
		total += len(body)
		out = append(out, map[string]any{"path": n.GetRef().GetPath(), "title": n.GetRef().GetTitle(), "content": body})
	}
	for _, raw := range reads {
		entry := strings.TrimSpace(raw)
		switch {
		case entry == "":
		case strings.HasPrefix(entry, "search:") || strings.HasPrefix(entry, "?"):
			q := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(entry, "search:"), "?"))
			hits, err := v.Search(ctx, q, nil, 3)
			if err != nil {
				e.log.Debug("memory search", "query", q, "err", err)
				continue
			}
			for _, h := range hits {
				if n, err := v.Read(ctx, h.GetNote().GetPath()); err == nil {
					add(n)
				}
			}
		case strings.HasSuffix(entry, "/"):
			for _, p := range recentNotes(v.Root(), entry, memoryFolderNotes) {
				if n, err := v.Read(ctx, p); err == nil {
					add(n)
				}
			}
		default:
			key := strings.TrimSuffix(strings.TrimPrefix(entry, "[["), "]]")
			if i := strings.IndexAny(key, "|#"); i >= 0 {
				key = key[:i]
			}
			n, err := v.Read(ctx, key)
			if err != nil && !strings.HasSuffix(key, ".md") {
				n, err = v.Read(ctx, key+".md")
			}
			if err != nil {
				e.log.Debug("memory read", "entry", entry, "err", err)
				continue
			}
			add(n)
		}
	}
	return out
}

// recentNotes lists up to n .md files directly in folder (vault-relative),
// most recently modified first.
func recentNotes(root, folder string, n int) []string {
	if root == "" {
		return nil
	}
	folder = strings.Trim(path.Clean("/"+strings.TrimSpace(folder)), "/")
	if folder == "" || strings.HasPrefix(folder, ".") {
		return nil
	}
	ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(folder)))
	if err != nil {
		return nil
	}
	type f struct {
		p string
		t time.Time
	}
	var fs []f
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fs = append(fs, f{folder + "/" + e.Name(), info.ModTime()})
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].t.After(fs[j].t) })
	var out []string
	for i := 0; i < len(fs) && i < n; i++ {
		out = append(out, fs[i].p)
	}
	return out
}

// ---------------------------------------------------------------- output

// outputSchema returns the schema a task's final answer must match: the
// hub-imposed one (critic verdicts, team output) or the agent's.
func outputSchema(ra *resolvedAgent, x *taskExt) map[string]any {
	if len(x.OutputSchema) > 0 {
		return x.OutputSchema
	}
	if s := ra.agent.GetOutputSchema(); s != nil && len(s.Fields) > 0 {
		return s.AsMap()
	}
	return nil
}

// outputInstructions tells the runtime how to answer.
func outputInstructions(doc map[string]any) string {
	js, _ := json.MarshalIndent(doc, "", "  ")
	return "## Output format (required)\n" +
		"Your final answer must be ONLY one JSON value matching this JSON Schema — no prose before or after it, no code fences. " +
		"The hub validates it; an answer that does not match is sent back to you once with the validation error.\n" +
		"```json\n" + string(js) + "\n```"
}

// ---------------------------------------------------------------- prepare

// prepared is the effective assignment of a run.
type prepared struct {
	agent        *nexusv1.Agent
	instructions string
	context      *structpb.Struct
}

// prepare builds the run's agent copy, instructions and context. An error
// means the task cannot run (it is failed by the caller with the message).
func (e *Engine) prepare(ctx context.Context, r *taskRec, ra *resolvedAgent, tier nexusv1.Tier, o assignOpts, budget *nexusv1.Money) (*prepared, error) {
	runCtx, err := e.assignmentContext(r, tier, o)
	if err != nil {
		return nil, err
	}
	if err := mergeAgentConfig(runCtx, ra.markdown); err != nil {
		e.log.Warn("agent note config not forwarded", "agent", ra.agent.GetId(), "err", err)
	}
	x, err := e.loadExt(ctx, r.task.Id)
	if err != nil {
		return nil, err
	}
	p := &prepared{agent: proto.Clone(ra.agent).(*nexusv1.Agent), instructions: ra.instructions, context: runCtx}
	set := func(k string, v any) {
		if pv, err := structpb.NewValue(jsonSafe(v)); err == nil {
			runCtx.Fields[k] = pv
		} else {
			e.log.Warn("assignment context value dropped", "key", k, "err", err)
		}
	}
	for k, v := range x.Context {
		set(k, v)
	}

	// Structured inputs.
	if is := ra.agent.GetInputSchema(); is != nil && len(is.Fields) > 0 {
		sch, err := schema.Compile(is.AsMap())
		if err != nil {
			return nil, fmt.Errorf("agent %q has an invalid input schema: %v", ra.agent.GetId(), err)
		}
		params := map[string]any{}
		if r.req.GetParams() != nil {
			params = r.req.Params.AsMap()
		}
		if err := sch.Validate(params); err != nil {
			return nil, fmt.Errorf("task inputs do not match agent %q's input schema: %v", ra.agent.GetId(), err)
		}
	}

	// Memory reads.
	if mem := e.memoryContext(ctx, ra.agent.GetMemory().GetReads()); len(mem) > 0 {
		set("memory", mem)
	}

	// Team membership: tools, brief, roster, messages, blackboard.
	if r.task.TeamId != "" {
		if err := e.prepareTeam(ctx, r, p, set); err != nil {
			return nil, err
		}
	}

	// Structured output.
	if doc := outputSchema(ra, x); doc != nil {
		p.instructions = strings.TrimSpace(p.instructions + "\n\n" + outputInstructions(doc))
		set("output_schema", doc)
	}

	// Per-run limits never exceed what the tree has left.
	timeLeft := e.remainingTime(ctx, r.task.RootId)
	if budget != nil || timeLeft > 0 {
		lim := proto.Clone(p.agent.GetLimits()).(*nexusv1.AgentLimits)
		if lim == nil {
			lim = &nexusv1.AgentLimits{}
		}
		if budget != nil && (lim.MaxCost == nil || lim.MaxCost.MinorUnits <= 0 || lim.MaxCost.MinorUnits > budget.MinorUnits) {
			lim.MaxCost = proto.Clone(budget).(*nexusv1.Money)
		}
		if s := int64(timeLeft / time.Second); timeLeft > 0 && (lim.MaxRuntimeSeconds <= 0 || lim.MaxRuntimeSeconds > s) {
			lim.MaxRuntimeSeconds = max(s, 1)
		}
		p.agent.Limits = lim
	}
	for _, w := range ra.warnings {
		e.log.Warn("agent warning", "agent", ra.agent.GetId(), "task", r.task.Id, "warning", w)
	}
	return p, nil
}
