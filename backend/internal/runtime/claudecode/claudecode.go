// Package claudecode provides the "claude-code" runtime (node module
// "runtime.claudecode"): it runs the Claude Code CLI headless with streaming
// JSON in and out,
//
//	claude -p --output-format stream-json --input-format stream-json --verbose \
//	  [--model M] [--permission-mode P] [--mcp-config F…] \
//	  [--append-system-prompt <agent instructions>] [extra args…]
//
// in the run's workdir (or a per-agent / allowed repo dir). The prompt is
// written to stdin as a stream-json user message and stdin stays open so Send
// can add user messages mid-run; it is closed once the result message
// arrives, which lets the CLI exit.
//
// Event mapping (stdout JSON lines):
//
//	system/init                 → recorded (session id, model); no event
//	assistant text blocks       → RunOutput
//	assistant tool_use blocks   → ToolCall (action class from the tool name)
//	user tool_result blocks     → ToolResult (content as {"content": text})
//	result                      → CostRecorded (total_cost_usd, usage),
//	                              RunOutput{is_final}, TaskUpdated
//	                              SUCCEEDED (summary = result) / FAILED
//	anything else               → ignored; malformed lines are logged
//
// If the process exits without a result the run FAILS with the stderr tail;
// Cancel kills the process group and the run ends CANCELLED.
//
// TODO(approvals): route Claude Code permission prompts to Nexus approvals by
// attaching a Nexus MCP server exposing a permission tool and passing
// --permission-prompt-tool mcp__nexus__approve. Until then permissions follow
// permission_mode / the CLI's own settings, and Resolve returns
// ErrApprovalsUnsupported.
//
// Configuration (module section "runtime.claudecode"):
//
//	runtime.claudecode:
//	  binary: claude
//	  args: []                    # extra CLI args, appended last
//	  model: ""                   # default --model ("" = CLI default)
//	  models: { best: opus }      # agent model alias → Claude model
//	  permission_mode: ""         # e.g. acceptEdits, plan, bypassPermissions
//	  mcp_config: []              # files passed to --mcp-config
//	  append_instructions: true   # pass agent instructions via --append-system-prompt
//	  env: {}                     # extra environment
//	  workdirs: { coder: ~/src/nexus }   # agent id → working directory
//	  allowed_repo_roots: [~/src] # roots under which context.repo_dir is honoured
package claudecode

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop/cliproc"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// ErrApprovalsUnsupported is returned by Resolve until the MCP permission
// bridge exists.
var ErrApprovalsUnsupported = errors.New("claude-code: approval routing not implemented (see TODO in package doc)")

// Config is the module configuration.
type Config struct {
	Binary             string            `yaml:"binary"`
	Args               []string          `yaml:"args"`
	Model              string            `yaml:"model"`
	Models             map[string]string `yaml:"models"`
	PermissionMode     string            `yaml:"permission_mode"`
	MCPConfig          []string          `yaml:"mcp_config"`
	AppendInstructions *bool             `yaml:"append_instructions"`
	Env                map[string]string `yaml:"env"`
	Workdirs           map[string]string `yaml:"workdirs"`
	AllowedRepoRoots   []string          `yaml:"allowed_repo_roots"`
}

// Module registers the runtime.
type Module struct {
	kernel.BaseModule
}

func (m *Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "runtime.claudecode",
		Description:    "Claude Code CLI runtime (headless stream-json)",
		Side:           kernel.SideNode,
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	return h.Runtimes().Register(New(cfg, h))
}

// Runtime is the claude-code runtime.
type Runtime struct {
	cfg  Config
	host kernel.Host
}

// New builds the runtime.
func New(cfg Config, h kernel.Host) *Runtime {
	if cfg.Binary == "" {
		cfg.Binary = "claude"
	}
	return &Runtime{cfg: cfg, host: h}
}

func (r *Runtime) Name() string { return "claude-code" }

func (r *Runtime) Capabilities() kernel.RuntimeCapabilities {
	return kernel.RuntimeCapabilities{Streaming: true, Steering: true, NativeTools: true}
}

func (r *Runtime) Available(ctx context.Context) (string, bool) {
	return cliproc.Version(ctx, r.cfg.Binary)
}

// claudeModel maps an agent's model alias to a --model value ("" = none).
func (r *Runtime) claudeModel(alias string) string {
	if m, ok := r.cfg.Models[alias]; ok {
		return m
	}
	switch a := strings.ToLower(alias); {
	case a == "opus", a == "sonnet", a == "haiku", strings.HasPrefix(a, "claude-"):
		return alias
	}
	return r.cfg.Model
}

// Args builds the CLI arguments for an assignment.
func (r *Runtime) Args(model, instructions string) []string {
	args := []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose"}
	if m := r.claudeModel(model); m != "" {
		args = append(args, "--model", m)
	}
	if r.cfg.PermissionMode != "" {
		args = append(args, "--permission-mode", r.cfg.PermissionMode)
	}
	for _, f := range r.cfg.MCPConfig {
		args = append(args, "--mcp-config", f)
	}
	if (r.cfg.AppendInstructions == nil || *r.cfg.AppendInstructions) && strings.TrimSpace(instructions) != "" {
		args = append(args, "--append-system-prompt", instructions)
	}
	return append(args, r.cfg.Args...)
}

func (r *Runtime) Start(_ context.Context, spec kernel.RunSpec) (kernel.Run, error) {
	a := spec.Assignment
	if a == nil {
		return nil, errors.New("claude-code: no assignment")
	}
	dir, err := cliproc.Workdir(a, spec.Workdir, cliproc.WorkdirOptions{PerAgent: r.cfg.Workdirs, AllowedRoots: r.cfg.AllowedRepoRoots})
	if err != nil {
		return nil, fmt.Errorf("claude-code: %w", err)
	}
	cmd := exec.Command(r.cfg.Binary, r.Args(a.GetAgent().GetModel(), a.GetInstructions())...)
	cmd.Dir = dir
	cmd.Env = cliproc.Env(spec.Env, r.cfg.Env, map[string]string{
		"NEXUS_RUN_ID":  a.GetRunId(),
		"NEXUS_TASK_ID": a.GetTaskId(),
	})
	return startRun(cmd, a.GetTaskId(), a.GetRunId(), agentloop.BuildPrompt(a), r.host.Logger().With("runtime", "claude-code", "run_id", a.GetRunId()))
}
