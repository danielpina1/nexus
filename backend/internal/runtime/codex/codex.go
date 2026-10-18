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
// Model: a zero CostRecorded{model, runtime:"codex"} is emitted when the run
// starts (model = the --model passed, "" = CLI default) and again when a
// legacy session_configured event reports a different model, so clients can
// label the reply with the model that actually served it.
//
// Chat runs (context chat=true + conversation_id): no CLI session is
// resumed; the prompt carries the last turns stored on the node plus the
// live Nexus status (chatsession.Prompt), and the reply is stored as a turn.
//
// Background work: codex exec has no background-shell or built-in subagent
// tool (its turn ends when its commands end), so nothing needs blocking;
// sub-work goes through the Nexus spawn_agent / team tools over MCP.
//
// Subscriptions: codex is used as logged in on the node (`codex login`, Sign
// in with ChatGPT). Available() also runs `codex login status` (check_login,
// default true) and reports the runtime unavailable when it says "not logged
// in" (re-probed when the node reconnects). Usage limits / missing logins
// end runs FAILED with result.error_kind rate_limited / auth (see claudecode)
// so the hub falls back to the agent's next runtime. max_concurrent (default
// 2) caps parallel runs per node. Per-agent settings (agent note `codex:`
// block): model, sandbox (read-only | workspace-write), coding; non-coding
// agents default to --sandbox read-only (non_coding_sandbox) and act through
// Nexus MCP tools.
//
// Nexus MCP (nexus_mcp, default true): each run gets a
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
//	  non_coding_sandbox: read-only
//	  nexus_mcp: true
//	  check_login: true
//	  max_concurrent: 2
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
	"github.com/danielpina1/nexus/backend/internal/runtime/chatsession"
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
	NexusMCP         *bool             `yaml:"nexus_mcp"`
	NexusMCPArgs     []string          `yaml:"nexus_mcp_args"`
	Env              map[string]string `yaml:"env"`
	Workdirs         map[string]string `yaml:"workdirs"`
	AllowedRepoRoots []string          `yaml:"allowed_repo_roots"`
	// MaxConcurrent caps parallel codex runs on this node (default 2;
	// 0 = unlimited).
	MaxConcurrent *int `yaml:"max_concurrent"`
	// NonCodingSandbox is the sandbox of non-coding agents when neither the
	// agent nor `sandbox` sets one (default read-only).
	NonCodingSandbox string `yaml:"non_coding_sandbox"`
	// CheckLogin (default true) runs `codex login status` in Available and
	// reports the runtime unavailable when it says "not logged in".
	CheckLogin *bool `yaml:"check_login"`
}

// DefaultMaxConcurrent is the default per-node limit of codex runs.
const DefaultMaxConcurrent = 2

// AgentSettings are the per-agent Codex settings from the agent note's
// `codex:` block (forwarded in RunAssignment.context under "codex"):
//
//	codex:
//	  model: gpt-5-codex            # or a Nexus alias mapped by runtime.codex.models
//	  sandbox: read-only            # read-only | workspace-write (danger-full-access is refused)
//	  coding: false                 # true: coding agent (module sandbox default applies)
type AgentSettings struct {
	Model   string `json:"model"`
	Sandbox string `json:"sandbox"`
	Coding  *bool  `json:"coding"`
}

var agentSandboxes = map[string]bool{"read-only": true, "workspace-write": true}

// SettingsFrom decodes the "codex" block of the run context.
func SettingsFrom(a *nexusv1.RunAssignment) AgentSettings {
	var s AgentSettings
	v := a.GetContext().GetFields()["codex"].GetStructValue()
	if v == nil {
		return s
	}
	m := v.AsMap()
	s.Model, _ = m["model"].(string)
	s.Sandbox, _ = m["sandbox"].(string)
	if b, ok := m["coding"].(bool); ok {
		s.Coding = &b
	}
	return s
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
	return kernel.RuntimeCapabilities{Streaming: true, NativeTools: true, ApprovalHooks: r.nexusMCP()}
}

func (r *Runtime) nexusMCP() bool { return r.cfg.NexusMCP == nil || *r.cfg.NexusMCP }

// MaxConcurrent implements kernel.ConcurrencyLimited.
func (r *Runtime) MaxConcurrent() int {
	if r.cfg.MaxConcurrent == nil {
		return DefaultMaxConcurrent
	}
	return *r.cfg.MaxConcurrent
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

// Available reports the CLI version. Unless check_login is false it also
// runs `codex login status`: output saying "not logged in" makes the runtime
// unavailable (the hub then picks another runtime for `runtime: auto`
// agents); a CLI without that command is assumed logged in.
func (r *Runtime) Available(ctx context.Context) (string, bool) {
	v, ok := cliproc.Version(ctx, r.cfg.Binary)
	if !ok || (r.cfg.CheckLogin != nil && !*r.cfg.CheckLogin) {
		return v, ok
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(cctx, r.cfg.Binary, "login", "status").CombinedOutput()
	if NotLoggedIn(string(out)) {
		return v + " (not logged in: run `codex login`)", false
	}
	return v, true
}

// NotLoggedIn reports whether `codex login status` output says so.
func NotLoggedIn(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "not logged in") || strings.Contains(l, "not authenticated")
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
	return r.args(r.model(modelAlias), r.cfg.Sandbox, nexusURL)
}

// ArgsFor builds the CLI arguments for an assignment: codex.model and
// codex.sandbox from the agent note win; non-coding agents (no codex.coding,
// no configured workdir or repo_dir) default to the non_coding_sandbox
// (read-only) when no sandbox is configured.
func (r *Runtime) ArgsFor(a *nexusv1.RunAssignment, nexusURL string) []string {
	s := SettingsFrom(a)
	model := r.model(a.GetAgent().GetModel())
	if s.Model != "" {
		model = r.model(s.Model)
		if model == r.cfg.Model && r.cfg.Models[s.Model] == "" {
			model = s.Model // a concrete codex model name
		}
	}
	sandbox := r.cfg.Sandbox
	switch {
	case agentSandboxes[s.Sandbox]:
		sandbox = s.Sandbox
	case sandbox == "" && !r.coding(a, s):
		sandbox = r.cfg.NonCodingSandbox
		if sandbox == "" {
			sandbox = "read-only"
		}
	}
	return r.args(model, sandbox, nexusURL)
}

func (r *Runtime) coding(a *nexusv1.RunAssignment, s AgentSettings) bool {
	if s.Coding != nil {
		return *s.Coding
	}
	if _, ok := r.cfg.Workdirs[a.GetAgent().GetId()]; ok {
		return true
	}
	return a.GetContext().GetFields()["repo_dir"].GetStringValue() != ""
}

func (r *Runtime) args(model, sandbox, nexusURL string) []string {
	args := []string{"exec", "--json"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
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
	args0 := r.ArgsFor(a, "")
	model := ""
	for i := 0; i+1 < len(args0); i++ {
		if args0[i] == "--model" {
			model = args0[i+1]
		}
	}
	run := newRun(a.GetTaskId(), a.GetRunId(), model, r.host.Logger().With("runtime", "codex", "run_id", a.GetRunId()))
	run.nodeID = spec.Env["NEXUS_NODE_ID"]
	url := ""
	if r.nexusMCP() {
		if url, err = r.startBridge(run, spec); err != nil {
			return nil, fmt.Errorf("codex: nexus mcp: %w", err)
		}
	}
	cmd := exec.Command(r.cfg.Binary, r.ArgsFor(a, url)...)
	cmd.Dir = dir
	cmd.Env = cliproc.Env(spec.Env, r.cfg.Env, map[string]string{
		"NEXUS_RUN_ID":  a.GetRunId(),
		"NEXUS_TASK_ID": a.GetTaskId(),
	})
	prompt := Prompt(a)
	if chatsession.IsChat(a) {
		prompt = r.chatPrompt(run, a)
	}
	if err := run.start(cmd, prompt); err != nil {
		run.stopBridge()
		return nil, err
	}
	return run, nil
}

// chatPrompt builds a chat run's prompt. codex exec keeps no session Nexus
// resumes (`codex exec resume` differs across CLI versions), so every
// message of a conversation carries the last turns stored on the node
// (internal/runtime/chatsession) plus the live Nexus status.
func (r *Runtime) chatPrompt(run *run, a *nexusv1.RunAssignment) string {
	var turns []chatsession.Turn
	if conv := chatsession.ConversationID(a); conv != "" {
		run.chat = chatsession.Open(r.host.DataDir())
		run.chatKey = chatsession.Key("codex", a.GetAgent().GetId(), conv)
		run.chatUser = strings.TrimSpace(a.GetPrompt())
		turns = run.chat.Get(run.chatKey).Turns
	}
	task := chatsession.Prompt(a, turns)
	instr := strings.TrimSpace(a.GetInstructions())
	if instr == "" {
		return task
	}
	return "# Agent instructions\n\n" + instr + "\n\n# Message\n\n" + task
}

// startBridge starts the run's Nexus MCP endpoint and returns its URL.
func (r *Runtime) startBridge(run *run, spec kernel.RunSpec) (string, error) {
	a := spec.Assignment
	run.gate = approvalgate.New(approvalgate.Config{
		TaskID: a.GetTaskId(), RunID: a.GetRunId(), Agent: a.GetAgent(), NodeID: spec.Env["NEXUS_NODE_ID"],
		Emit: run.emit, Log: run.log,
	})
	// Run tools (browser checkout…) ask for approvals through this gate.
	agentloop.BindGate(spec.Gate, run.gate)
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
