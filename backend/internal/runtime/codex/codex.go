// Package codex provides the "codex" runtime (node module "runtime.codex"):
// it runs the OpenAI Codex CLI non-interactively,
//
//	codex exec --json [--model M] [--sandbox S] [--full-auto] [--skip-git-repo-check] [extra args…] -
//
// in the run's workdir (or a per-agent / allowed repo dir). The prompt (agent
// instructions + task prompt + context) is written to stdin ("-") and stdin
// is closed; codex exec runs one turn and exits. Steering (Send) is not
// supported.
//
// stdout is JSONL; both the current event format (thread/turn/item events)
// and the legacy {"id","msg":{…}} format are parsed tolerantly:
//
//	item agent_message / msg agent_message        → RunOutput
//	item command_execution / exec_command_*        → ToolCall "shell" (compute) + ToolResult
//	item file_change / patch_apply_*               → ToolCall "apply_patch" (compute) + ToolResult
//	item mcp_tool_call / mcp_tool_call_*           → ToolCall "<server>.<tool>" + ToolResult
//	item web_search                                → ToolCall "web_search" (read) + ToolResult
//	turn.completed usage / token_count             → CostRecorded (tokens; no amount)
//	turn.failed / error                            → TaskUpdated FAILED
//	process exit 0 (or turn completed)             → TaskUpdated SUCCEEDED (summary = last agent message)
//
// Unknown events are ignored; malformed lines are logged.
//
// TODO(approvals): codex exec cannot ask for approvals interactively; route
// them through a Nexus MCP server once it exists. Sandbox/full-auto are off
// unless configured.
//
// Configuration (module section "runtime.codex"):
//
//	runtime.codex:
//	  binary: codex
//	  args: []                     # extra args (before the prompt)
//	  model: ""                    # default --model ("" = CLI default)
//	  models: { best: gpt-5-codex }  # agent model alias → codex model
//	  sandbox: ""                  # read-only | workspace-write | danger-full-access
//	  full_auto: false
//	  skip_git_repo_check: true    # run workdirs are usually not git repos
//	  env: {}
//	  workdirs: { coder: ~/src/nexus }
//	  allowed_repo_roots: [~/src]
package codex

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop/cliproc"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

var (
	// ErrSteeringUnsupported is returned by Send: codex exec runs one turn.
	ErrSteeringUnsupported = errors.New("codex: sending messages to a running exec is not supported")
	// ErrApprovalsUnsupported is returned by Resolve.
	ErrApprovalsUnsupported = errors.New("codex: approval routing not implemented")
)

// Config is the module configuration.
type Config struct {
	Binary           string            `yaml:"binary"`
	Args             []string          `yaml:"args"`
	Model            string            `yaml:"model"`
	Models           map[string]string `yaml:"models"`
	Sandbox          string            `yaml:"sandbox"`
	FullAuto         bool              `yaml:"full_auto"`
	SkipGitRepoCheck *bool             `yaml:"skip_git_repo_check"`
	Env              map[string]string `yaml:"env"`
	Workdirs         map[string]string `yaml:"workdirs"`
	AllowedRepoRoots []string          `yaml:"allowed_repo_roots"`
}

// Module registers the runtime.
type Module struct {
	kernel.BaseModule
}

func (m *Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "runtime.codex",
		Description:    "OpenAI Codex CLI runtime (codex exec --json)",
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

// Runtime is the codex runtime.
type Runtime struct {
	cfg  Config
	host kernel.Host
}

// New builds the runtime.
func New(cfg Config, h kernel.Host) *Runtime {
	if cfg.Binary == "" {
		cfg.Binary = "codex"
	}
	return &Runtime{cfg: cfg, host: h}
}

func (r *Runtime) Name() string { return "codex" }

func (r *Runtime) Capabilities() kernel.RuntimeCapabilities {
	return kernel.RuntimeCapabilities{Streaming: true, NativeTools: true}
}

func (r *Runtime) Available(ctx context.Context) (string, bool) {
	return cliproc.Version(ctx, r.cfg.Binary)
}

func (r *Runtime) model(alias string) string {
	if m, ok := r.cfg.Models[alias]; ok {
		return m
	}
	return r.cfg.Model
}

// Args builds the CLI arguments; the prompt is read from stdin ("-").
func (r *Runtime) Args(modelAlias string) []string {
	args := []string{"exec", "--json"}
	if m := r.model(modelAlias); m != "" {
		args = append(args, "--model", m)
	}
	if r.cfg.Sandbox != "" {
		args = append(args, "--sandbox", r.cfg.Sandbox)
	}
	if r.cfg.FullAuto {
		args = append(args, "--full-auto")
	}
	if r.cfg.SkipGitRepoCheck == nil || *r.cfg.SkipGitRepoCheck {
		args = append(args, "--skip-git-repo-check")
	}
	args = append(args, r.cfg.Args...)
	return append(args, "-")
}

// Prompt combines agent instructions with the task prompt (codex exec has no
// separate system prompt flag).
func Prompt(a *nexusv1.RunAssignment) string {
	task := agentloop.BuildPrompt(a)
	instr := strings.TrimSpace(a.GetInstructions())
	if instr == "" {
		return task
	}
	return "# Agent instructions\n\n" + instr + "\n\n# Task\n\n" + task
}

func (r *Runtime) Start(_ context.Context, spec kernel.RunSpec) (kernel.Run, error) {
	a := spec.Assignment
	if a == nil {
		return nil, errors.New("codex: no assignment")
	}
	dir, err := cliproc.Workdir(a, spec.Workdir, cliproc.WorkdirOptions{PerAgent: r.cfg.Workdirs, AllowedRoots: r.cfg.AllowedRepoRoots})
	if err != nil {
		return nil, fmt.Errorf("codex: %w", err)
	}
	cmd := exec.Command(r.cfg.Binary, r.Args(a.GetAgent().GetModel())...)
	cmd.Dir = dir
	cmd.Env = cliproc.Env(spec.Env, r.cfg.Env, map[string]string{
		"NEXUS_RUN_ID":  a.GetRunId(),
		"NEXUS_TASK_ID": a.GetTaskId(),
	})
	return startRun(cmd, a.GetTaskId(), a.GetRunId(), Prompt(a), r.model(a.GetAgent().GetModel()),
		r.host.Logger().With("runtime", "codex", "run_id", a.GetRunId()))
}
