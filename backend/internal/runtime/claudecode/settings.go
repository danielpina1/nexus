package claudecode

import (
	"encoding/json"
	"strconv"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/mcp"
)

// AgentSettings are the per-agent Claude Code settings from the agent note's
// `claude:` frontmatter block (forwarded by the hub in RunAssignment.context
// under "claude"):
//
//	claude:
//	  model: sonnet                 # sonnet | opus | haiku | <full model id> | a Nexus alias
//	  allowed_tools: [WebSearch, WebFetch]   # Claude Code tool rules pre-approved / re-enabled
//	  disallowed_tools: [Task]      # extra tools to remove
//	  permission_mode: default      # default | acceptEdits | plan (bypassPermissions is refused)
//	  max_turns: 30
//	  coding: false                 # true: keep Claude Code's Bash/Edit/Write (coding agents)
type AgentSettings struct {
	Model           string   `json:"model"`
	AllowedTools    []string `json:"allowed_tools"`
	DisallowedTools []string `json:"disallowed_tools"`
	PermissionMode  string   `json:"permission_mode"`
	MaxTurns        int      `json:"max_turns"`
	Coding          *bool    `json:"coding"`
}

// DefaultNonCodingDisallowed are the Claude Code built-in tools removed for
// non-coding agents: shell and file edits (Nexus tools over MCP replace
// them) and Claude Code's own web tools (Nexus web.* is the default; allow
// WebSearch/WebFetch per agent with claude.allowed_tools).
var DefaultNonCodingDisallowed = []string{"Bash", "Edit", "MultiEdit", "Write", "NotebookEdit", "WebSearch", "WebFetch"}

// agentPermissionModes are the permission modes an agent note may choose.
var agentPermissionModes = map[string]bool{"default": true, "acceptEdits": true, "plan": true}

// SettingsFrom decodes the "claude" block of the run context.
func SettingsFrom(a *nexusv1.RunAssignment) AgentSettings {
	var s AgentSettings
	v := a.GetContext().GetFields()["claude"]
	if v == nil || v.GetStructValue() == nil {
		return s
	}
	b, err := json.Marshal(v.GetStructValue().AsMap())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

// coding reports whether the run is a coding run: claude.coding wins;
// otherwise agents with a configured workdir or a repo_dir in the context
// are coding agents.
func (r *Runtime) coding(a *nexusv1.RunAssignment, s AgentSettings) bool {
	if s.Coding != nil {
		return *s.Coding
	}
	if _, ok := r.cfg.Workdirs[a.GetAgent().GetId()]; ok {
		return true
	}
	if v := a.GetContext().GetFields()["repo_dir"]; v != nil && v.GetStringValue() != "" {
		return true
	}
	return false
}

// ArgsFor builds the CLI arguments for an assignment, applying the agent's
// claude settings; nexusConfig is the per-run MCP config file ("" = none).
//
// On top of ArgsWithNexus it adds:
//
//	--allowedTools mcp__nexus[,<claude.allowed_tools>…]   (Nexus tools are gated by the bridge itself)
//	--disallowedTools <non-coding defaults minus allowed, plus claude.disallowed_tools>
//	--max-turns N
//
// and uses claude.model / claude.permission_mode when set.
func (r *Runtime) ArgsFor(a *nexusv1.RunAssignment, nexusConfig string) []string {
	s := SettingsFrom(a)
	model := a.GetAgent().GetModel()
	if s.Model != "" {
		model = s.Model
	}
	mode := r.cfg.PermissionMode
	if s.PermissionMode != "" && agentPermissionModes[s.PermissionMode] {
		mode = s.PermissionMode
	}
	args := r.args(model, a.GetInstructions(), nexusConfig, mode)
	extra := []string{}
	allowed := []string{}
	if nexusConfig != "" {
		allowed = append(allowed, mcp.ClaudeServerRule(mcp.ServerName))
	}
	allowed = append(allowed, s.AllowedTools...)
	if len(allowed) > 0 {
		extra = append(extra, "--allowedTools", strings.Join(dedupe(allowed), ","))
	}
	var disallowed []string
	if !r.coding(a, s) {
		list := r.cfg.NonCodingDisallowed
		if list == nil {
			list = DefaultNonCodingDisallowed
		}
		for _, t := range list {
			if !contains(s.AllowedTools, t) {
				disallowed = append(disallowed, t)
			}
		}
	}
	disallowed = append(disallowed, s.DisallowedTools...)
	if len(disallowed) > 0 {
		extra = append(extra, "--disallowedTools", strings.Join(dedupe(disallowed), ","))
	}
	if s.MaxTurns > 0 {
		extra = append(extra, "--max-turns", strconv.Itoa(s.MaxTurns))
	}
	// Agent settings go before the operator's extra args (cfg.Args, last).
	n := len(args) - len(r.cfg.Args)
	out := append(append(append([]string(nil), args[:n]...), extra...), args[n:]...)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
