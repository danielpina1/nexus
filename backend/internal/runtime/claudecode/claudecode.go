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
// Nexus MCP + approvals (nexus_mcp, default true): for each run the node
// starts a loopback Streamable-HTTP MCP endpoint (internal/mcp) at
// http://127.0.0.1:<port>/mcp/<random token>, exposing the run's Nexus tools
// (RunSpec.Tools: memory.search → memory_search, vault.write, ask_user,
// notify, spawn_agent, node tools…) plus nexus_permission, an implementation
// of Claude Code's permission-prompt-tool contract. The endpoint URL is
// written to <run workspace>/.nexus-mcp.json (0600) and passed with
//
//	--mcp-config <file> --permission-prompt-tool mcp__nexus__nexus_permission
//
// (the latter omitted when permission_mode is bypassPermissions). Every
// permission prompt is classified (PermissionClass: Bash/Write/Edit/
// MultiEdit/NotebookEdit → compute, Bash with git push → code_push, rm -rf /
// forced push → destructive, Read/Grep/Glob/LS/WebFetch/WebSearch → read,
// mcp__nexus__* → the Nexus tool's class, unknown → approve) and run through
// the approval gate with the agent's policy: auto/notify allow at once,
// approve/biometric emit an Approval and wait for Resolve. Nexus tool calls
// are gated again in tools/call unless the permission prompt just approved
// that exact call. The endpoint and gate are closed when the CLI exits; Cancel
// denies pending prompts. MCP_TOOL_TIMEOUT is set (unless already set) so the
// CLI waits for human approvals.
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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielpina1/nexus/backend/internal/mcp"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop/cliproc"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// ErrApprovalsUnsupported is returned by Resolve until the MCP permission
// bridge exists.
var ErrApprovalsUnsupported = errors.New("claude-code: approvals need the Nexus MCP bridge (runtime.claudecode.nexus_mcp)")

// Config is the module configuration.
type Config struct {
	Binary             string            `yaml:"binary"`
	Args               []string          `yaml:"args"`
	Model              string            `yaml:"model"`
	Models             map[string]string `yaml:"models"`
	PermissionMode     string            `yaml:"permission_mode"`
	MCPConfig          []string          `yaml:"mcp_config"`
	NexusMCP           *bool             `yaml:"nexus_mcp"`
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
	return kernel.RuntimeCapabilities{Streaming: true, Steering: true, NativeTools: true, ApprovalHooks: r.nexusMCP()}
}

func (r *Runtime) nexusMCP() bool { return r.cfg.NexusMCP == nil || *r.cfg.NexusMCP }

// PermissionTool is the --permission-prompt-tool value.
var PermissionTool = mcp.ClaudeToolName(mcp.ServerName, mcp.PermissionToolName)

// MCPConfigFile is the per-run MCP config written into the run workspace.
const MCPConfigFile = ".nexus-mcp.json"

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

// Args builds the CLI arguments for an assignment (without the Nexus MCP
// bridge; see ArgsWithNexus).
func (r *Runtime) Args(model, instructions string) []string {
	return r.ArgsWithNexus(model, instructions, "")
}

// ArgsWithNexus builds the CLI arguments; nexusConfig is the per-run MCP
// config file ("" = no bridge).
func (r *Runtime) ArgsWithNexus(model, instructions, nexusConfig string) []string {
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
	if nexusConfig != "" {
		args = append(args, "--mcp-config", nexusConfig)
		if r.cfg.PermissionMode != "bypassPermissions" {
			args = append(args, "--permission-prompt-tool", PermissionTool)
		}
	}
	if (r.cfg.AppendInstructions == nil || *r.cfg.AppendInstructions) && strings.TrimSpace(instructions) != "" {
		args = append(args, "--append-system-prompt", instructions)
	}
	return append(args, r.cfg.Args...)
}

func (r *Runtime) Start(ctx context.Context, spec kernel.RunSpec) (kernel.Run, error) {
	a := spec.Assignment
	if a == nil {
		return nil, errors.New("claude-code: no assignment")
	}
	dir, err := cliproc.Workdir(a, spec.Workdir, cliproc.WorkdirOptions{PerAgent: r.cfg.Workdirs, AllowedRoots: r.cfg.AllowedRepoRoots})
	if err != nil {
		return nil, fmt.Errorf("claude-code: %w", err)
	}
	log := r.host.Logger().With("runtime", "claude-code", "run_id", a.GetRunId())
	run := newRun(a.GetTaskId(), a.GetRunId(), log)
	env := map[string]string{
		"NEXUS_RUN_ID":  a.GetRunId(),
		"NEXUS_TASK_ID": a.GetTaskId(),
	}
	nexusConfig := ""
	if r.nexusMCP() {
		file, err := r.startBridge(run, spec)
		if err != nil {
			return nil, fmt.Errorf("claude-code: nexus mcp: %w", err)
		}
		nexusConfig = file
		if !envSet("MCP_TOOL_TIMEOUT", spec.Env, r.cfg.Env) {
			env["MCP_TOOL_TIMEOUT"] = strconv.FormatInt((approvalgate.DefaultTTL + 2*time.Minute).Milliseconds(), 10)
		}
	}
	cmd := exec.Command(r.cfg.Binary, r.ArgsWithNexus(a.GetAgent().GetModel(), a.GetInstructions(), nexusConfig)...)
	cmd.Dir = dir
	cmd.Env = cliproc.Env(spec.Env, r.cfg.Env, env)
	if err := run.start(cmd, agentloop.BuildPrompt(a)); err != nil {
		run.stopBridge()
		return nil, err
	}
	return run, nil
}

func envSet(key string, maps ...map[string]string) bool {
	for _, m := range maps {
		if _, ok := m[key]; ok {
			return true
		}
	}
	_, ok := os.LookupEnv(key)
	return ok
}

// startBridge starts the run's Nexus MCP endpoint and writes its client
// config into the run workspace; it returns the config path.
func (r *Runtime) startBridge(run *run, spec kernel.RunSpec) (string, error) {
	a := spec.Assignment
	run.gate = approvalgate.New(approvalgate.Config{
		TaskID: a.GetTaskId(), RunID: a.GetRunId(), Agent: a.GetAgent(), NodeID: spec.Env["NEXUS_NODE_ID"],
		Emit: run.emit, Log: run.log,
	})
	who := "Claude Code"
	if n := a.GetAgent().GetName(); n != "" {
		who = n + " (Claude Code)"
	}
	bridge, err := mcp.NewBridge(mcp.RunConfig{
		Tools: spec.Tools, Gate: run.gate, Permission: true, Classify: PermissionClass, Who: who,
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
	file := filepath.Join(spec.Workdir, MCPConfigFile)
	cleanup := func() {
		_ = ep.Close()
		_ = os.Remove(file)
	}
	conf, err := mcp.ClientConfig(mcp.ServerName, ep.URL())
	if err == nil {
		err = os.WriteFile(file, conf, 0o600)
	}
	if err != nil {
		run.gate.Close()
		cleanup()
		return "", err
	}
	var once sync.Once
	run.cleanup = func() { once.Do(cleanup) }
	return file, nil
}
