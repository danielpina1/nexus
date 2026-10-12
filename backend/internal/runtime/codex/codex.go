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
// Nexus MCP (nexus_mcp, default false): when enabled, each run gets a
// loopback Streamable-HTTP MCP endpoint (internal/mcp) exposing the run's
// Nexus tools (memory.search → memory_search, vault.write, ask_user…),
// attached with codex config overrides rendered from nexus_mcp_args
// ("{url}" is replaced by the endpoint URL), by default
//
//	-c mcp_servers.nexus.url="http://127.0.0.1:<port>/mcp/<token>"
//	-c mcp_servers.nexus.tool_timeout_sec=3720
//
// (older codex versions may also need `-c experimental_use_rmcp_client=true`
// for HTTP servers — add it to nexus_mcp_args). Nexus tool calls are gated by
// the approval gate with the agent's policy (approve-level tools emit an
// Approval and wait for Resolve; denials come back to the model as tool
// errors). codex exec has no permission-prompt hook, so its built-in shell and
// file edits are NOT routed to Nexus approvals: the sandbox setting
// (read-only / workspace-write) remains their guard. Keep full_auto off and
// use a restrictive sandbox for agents whose policy requires approvals.
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
	"strconv"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/mcp"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop/cliproc"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

var (
	// ErrSteeringUnsupported is returned by Send: codex exec runs one turn.
	ErrSteeringUnsupported = errors.New("codex: sending messages to a running exec is not supported")
	// ErrApprovalsUnsupported is returned by Resolve without the Nexus MCP bridge.
	ErrApprovalsUnsupported = errors.New("codex: approvals need the Nexus MCP bridge (runtime.codex.nexus_mcp)")
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
	NexusMCP         bool              `yaml:"nexus_mcp"`
	NexusMCPArgs     []string          `yaml:"nexus_mcp_args"`
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
	return kernel.RuntimeCapabilities{Streaming: true, NativeTools: true, ApprovalHooks: r.cfg.NexusMCP}
}

// DefaultNexusMCPArgs attaches the Nexus endpoint through codex config
// overrides; "{url}" is replaced by the endpoint URL.
var DefaultNexusMCPArgs = []string{
	"-c", `mcp_servers.nexus.url="{url}"`,
	"-c", "mcp_servers.nexus.tool_timeout_sec=" + strconv.Itoa(int((approvalgate.DefaultTTL+2*time.Minute)/time.Second)),
}

// NexusArgs renders the nexus_mcp_args template for url.
func (r *Runtime) NexusArgs(url string) []string {
	tmpl := r.cfg.NexusMCPArgs
	if len(tmpl) == 0 {
		tmpl = DefaultNexusMCPArgs
	}
	out := make([]string, len(tmpl))
	for i, a := range tmpl {
		out[i] = strings.ReplaceAll(a, "{url}", url)
	}
	return out
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
func (r *Runtime) Args(modelAlias string) []string { return r.ArgsWithNexus(modelAlias, "") }

// ArgsWithNexus builds the CLI arguments, attaching the Nexus MCP endpoint
// at nexusURL ("" = none).
func (r *Runtime) ArgsWithNexus(modelAlias, nexusURL string) []string {
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
	if nexusURL != "" {
		args = append(args, r.NexusArgs(nexusURL)...)
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
	model := r.model(a.GetAgent().GetModel())
	run := newRun(a.GetTaskId(), a.GetRunId(), model, r.host.Logger().With("runtime", "codex", "run_id", a.GetRunId()))
	url := ""
	if r.cfg.NexusMCP {
		if url, err = r.startBridge(run, spec); err != nil {
			return nil, fmt.Errorf("codex: nexus mcp: %w", err)
		}
	}
	cmd := exec.Command(r.cfg.Binary, r.ArgsWithNexus(a.GetAgent().GetModel(), url)...)
	cmd.Dir = dir
	cmd.Env = cliproc.Env(spec.Env, r.cfg.Env, map[string]string{
		"NEXUS_RUN_ID":  a.GetRunId(),
		"NEXUS_TASK_ID": a.GetTaskId(),
	})
	if err := run.start(cmd, Prompt(a)); err != nil {
		run.stopBridge()
		return nil, err
	}
	return run, nil
}

// startBridge starts the run's Nexus MCP endpoint and returns its URL.
func (r *Runtime) startBridge(run *run, spec kernel.RunSpec) (string, error) {
	a := spec.Assignment
	run.gate = approvalgate.New(approvalgate.Config{
		TaskID: a.GetTaskId(), RunID: a.GetRunId(), Agent: a.GetAgent(), NodeID: spec.Env["NEXUS_NODE_ID"],
		Emit: run.emit, Log: run.log,
	})
	who := "Codex"
	if n := a.GetAgent().GetName(); n != "" {
		who = n + " (Codex)"
	}
	bridge, err := mcp.NewBridge(mcp.RunConfig{
		Tools: spec.Tools, Gate: run.gate, Who: who,
		TaskID: a.GetTaskId(), RunID: a.GetRunId(), AgentID: a.GetAgent().GetId(), NodeID: spec.Env["NEXUS_NODE_ID"],
		Log: run.log,
	})
	if err != nil {
		run.gate.Close()
		return "", err
	}
	ep, err := mcp.Listen(bridge.Server)
	if err != nil {
		run.gate.Close()
		return "", err
	}
	var once sync.Once
	run.cleanup = func() { once.Do(func() { _ = ep.Close() }) }
	return ep.URL(), nil
}
