package mcp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/oklog/ulid/v2"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	secapproval "github.com/danielpina1/nexus/backend/internal/security/approval"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Defaults for the per-run Nexus server.
const (
	// ServerName is the key used in client MCP configs ("nexus"), which
	// makes Claude Code name the tools mcp__nexus__<tool>.
	ServerName = "nexus"
	// PermissionToolName implements Claude Code's --permission-prompt-tool
	// contract; Claude Code refers to it as mcp__nexus__nexus_permission.
	PermissionToolName = "nexus_permission"
)

// ClaudeToolName is the name Claude Code gives an MCP tool of a server.
func ClaudeToolName(server, tool string) string { return "mcp__" + server + "__" + tool }

// ClaudeServerRule is the Claude Code permission rule ("mcp__<server>")
// matching every tool of an MCP server (for --allowedTools).
func ClaudeServerRule(server string) string { return "mcp__" + server }

// Classifier maps a client-native tool (e.g. Claude Code's "Bash") and its
// input to an action class and a one-line human summary.
type Classifier func(tool string, input json.RawMessage) (nexusv1.ActionClass, string)

// RunConfig builds a per-run Nexus MCP server.
type RunConfig struct {
	// Tools are the run's tools (RunSpec.Tools: allow-listed hub + node tools).
	Tools []kernel.Tool
	// Gate gates Nexus tool calls and permission prompts. Required.
	Gate *approvalgate.Gate
	// Permission adds the PermissionToolName tool (Claude Code
	// --permission-prompt-tool contract).
	Permission bool
	// Classify classifies non-Nexus tools in permission prompts; nil (or an
	// UNSPECIFIED result) fails safe to approval.
	Classify Classifier
	// ServerName is the client-side server key (default ServerName).
	ServerName string
	// Who names the agent in approval titles ("Claude Code", "coder"…).
	Who string

	TaskID, RunID, AgentID, NodeID string
	Version                        string
	Log                            *slog.Logger
}

// Bridge is a per-run MCP server exposing Nexus tools.
type Bridge struct {
	*Server
	cfg   RunConfig
	byMCP map[string]kernel.Tool // MCP name → tool

	mu sync.Mutex
	// preapproved counts permission-prompt approvals of Nexus tool calls
	// (tool + canonical input) so the following tools/call does not ask twice.
	preapproved map[string]int
}

// NewBridge builds the server. Tools whose sanitized names collide are
// skipped (logged).
func NewBridge(cfg RunConfig) (*Bridge, error) {
	if cfg.Gate == nil {
		return nil, fmt.Errorf("mcp: bridge needs an approval gate")
	}
	if cfg.ServerName == "" {
		cfg.ServerName = ServerName
	}
	if cfg.Who == "" {
		cfg.Who = nonEmpty(cfg.AgentID, "Agent")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	b := &Bridge{
		Server:      NewServer(cfg.ServerName, nonEmpty(cfg.Version, "dev")),
		cfg:         cfg,
		byMCP:       map[string]kernel.Tool{},
		preapproved: map[string]int{},
	}
	b.Server.Log = cfg.Log
	b.Server.Instructions = "Nexus tools: the user's memory/vault, questions and notifications to the user, sub-agents. " +
		"Tool names are Nexus names with dots replaced by underscores (memory_search = memory.search)."
	if cfg.Permission {
		if err := b.AddTool(b.permissionTool()); err != nil {
			return nil, err
		}
	}
	for _, t := range cfg.Tools {
		spec := t.Spec()
		name := SanitizeToolName(spec.Name)
		if _, dup := b.byMCP[name]; dup || (cfg.Permission && name == PermissionToolName) {
			cfg.Log.Warn("mcp: tool name collision; skipped", "tool", spec.Name, "mcp_name", name)
			continue
		}
		tool := t
		desc := spec.Description
		if name != spec.Name {
			desc = strings.TrimSpace(desc + " (Nexus tool " + spec.Name + ")")
		}
		if err := b.AddTool(Tool{
			Name: name, Description: desc, InputSchema: spec.InputSchema,
			Call: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return b.callNexus(ctx, tool, args)
			},
		}); err != nil {
			return nil, err
		}
		b.byMCP[name] = t
	}
	return b, nil
}

// NexusTool resolves an MCP tool name to the run's tool.
func (b *Bridge) NexusTool(mcpName string) (kernel.Tool, bool) {
	t, ok := b.byMCP[mcpName]
	return t, ok
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func decodeInput(args json.RawMessage) any {
	var v any
	if err := json.Unmarshal(args, &v); err != nil || v == nil {
		return map[string]any{}
	}
	return v
}

func preapprovalKey(tool string, input json.RawMessage) string {
	h, err := secapproval.ActionHash(input)
	if err != nil {
		return ""
	}
	return tool + "\x00" + hex.EncodeToString(h)
}

func (b *Bridge) preapprove(tool string, input json.RawMessage) {
	if k := preapprovalKey(tool, input); k != "" {
		b.mu.Lock()
		b.preapproved[k]++
		b.mu.Unlock()
	}
}

func (b *Bridge) consumePreapproval(tool string, input json.RawMessage) bool {
	k := preapprovalKey(tool, input)
	if k == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.preapproved[k] == 0 {
		return false
	}
	b.preapproved[k]--
	if b.preapproved[k] == 0 {
		delete(b.preapproved, k)
	}
	return true
}

// gateMessage runs the gate; "" means proceed, else the refusal message.
func (b *Bridge) gateMessage(ctx context.Context, req approvalgate.Request) string {
	out, err := b.cfg.Gate.Check(ctx, req)
	switch {
	case err != nil:
		return "approval failed: " + err.Error()
	case !out.Approved:
		b.cfg.Gate.Resume()
		if out.Reason == "" || out.Reason == "denied" {
			return "denied by user"
		}
		return "not approved: " + out.Reason
	}
	return ""
}

// callNexus executes a Nexus tool for tools/call, gated unless a permission
// prompt already approved this exact call.
func (b *Bridge) callNexus(ctx context.Context, t kernel.Tool, args json.RawMessage) (*CallResult, error) {
	spec := t.Spec()
	if !approvalgate.HumanInTheLoopTools[spec.Name] && !b.consumePreapproval(spec.Name, args) {
		msg := b.gateMessage(ctx, approvalgate.Request{
			Class:   spec.ActionClass,
			Title:   fmt.Sprintf("%s wants to use %s", b.cfg.Who, spec.Name),
			Summary: truncate(string(args), 500),
			Payload: map[string]any{"tool": spec.Name, "input": decodeInput(args)},
		})
		if msg != "" {
			return ErrorResult(msg), nil
		}
	}
	res, err := t.Invoke(ctx, kernel.ToolCall{
		ID: "mcp_" + ulid.Make().String(), TaskID: b.cfg.TaskID, RunID: b.cfg.RunID,
		AgentID: b.cfg.AgentID, NodeID: b.cfg.NodeID, Input: args,
	})
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(res.Output))
	if text == "" {
		text = "{}"
	}
	out := TextResult(text)
	for _, a := range res.Artifacts {
		out.Content = append(out.Content, Content{Type: "text", Text: "artifact: " + nonEmpty(a.GetName(), a.GetId())})
	}
	return out, nil
}

// PermissionDecision is the JSON text returned by the permission tool.
type PermissionDecision struct {
	Behavior     string          `json:"behavior"` // allow | deny
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Message      string          `json:"message,omitempty"`
}

var permissionSchema = json.RawMessage(`{"type":"object","properties":{
"tool_name":{"type":"string","description":"Tool the agent wants to use"},
"input":{"type":"object","description":"The tool input"},
"tool_use_id":{"type":"string"}},"required":["tool_name","input"]}`)

func (b *Bridge) permissionTool() Tool {
	return Tool{
		Name:        PermissionToolName,
		Title:       "Nexus permission prompt",
		Description: "Asks the Nexus owner (per policy) whether a tool call may run. Used by Claude Code's --permission-prompt-tool.",
		InputSchema: permissionSchema,
		Call:        b.permission,
	}
}

// permission implements Claude Code's permission prompt tool: input
// {tool_name, input, tool_use_id?} → text {"behavior":"allow","updatedInput":…}
// or {"behavior":"deny","message":…}.
func (b *Bridge) permission(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var in struct {
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.ToolName == "" {
		return decisionResult(PermissionDecision{Behavior: "deny", Message: "malformed permission request"}), nil
	}
	input := json.RawMessage(strings.TrimSpace(string(in.Input)))
	if len(input) == 0 || string(input) == "null" {
		input = json.RawMessage("{}")
	}
	class := nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED
	summary := ""
	toolName := in.ToolName
	var nexusTool kernel.Tool
	if prefix := ClaudeToolName(b.cfg.ServerName, ""); strings.HasPrefix(in.ToolName, prefix) {
		if t, ok := b.byMCP[strings.TrimPrefix(in.ToolName, prefix)]; ok {
			nexusTool = t
			class, toolName = t.Spec().ActionClass, t.Spec().Name
			if approvalgate.HumanInTheLoopTools[toolName] {
				return decisionResult(PermissionDecision{Behavior: "allow", UpdatedInput: input}), nil
			}
		} else if strings.TrimPrefix(in.ToolName, prefix) == PermissionToolName {
			return decisionResult(PermissionDecision{Behavior: "deny", Message: "the permission tool cannot be called directly"}), nil
		}
	} else if b.cfg.Classify != nil {
		class, summary = b.cfg.Classify(in.ToolName, input)
	}
	if summary == "" {
		summary = truncate(string(input), 500)
	}
	msg := b.gateMessage(ctx, approvalgate.Request{
		Class:   class,
		Title:   fmt.Sprintf("%s wants to use %s", b.cfg.Who, toolName),
		Summary: summary,
		Payload: map[string]any{"tool": toolName, "input": decodeInput(input)},
	})
	if msg != "" {
		return decisionResult(PermissionDecision{Behavior: "deny", Message: msg}), nil
	}
	if nexusTool != nil && !approvalgate.HumanInTheLoopTools[toolName] {
		b.preapprove(nexusTool.Spec().Name, input)
	}
	return decisionResult(PermissionDecision{Behavior: "allow", UpdatedInput: input}), nil
}

func decisionResult(d PermissionDecision) *CallResult {
	b, _ := json.Marshal(d)
	return TextResult(string(b))
}

// ClientConfig is the Claude Code style MCP config file content:
// {"mcpServers":{"nexus":{"type":"http","url":…}}}.
func ClientConfig(server, url string) ([]byte, error) {
	return json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{server: map[string]any{"type": "http", "url": url}},
	}, "", "  ")
}
