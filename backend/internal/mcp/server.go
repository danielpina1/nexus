// Package mcp is a minimal Model Context Protocol server (JSON-RPC 2.0,
// protocol revision 2025-06-18; 2025-03-26 and 2024-11-05 are accepted too)
// exposing tools only:
//
//	initialize, notifications/initialized, ping, tools/list, tools/call
//
// Transports:
//   - stdio (ServeStdio): newline-delimited JSON-RPC messages;
//   - Streamable HTTP (HTTPHandler / Listen): POST one message (or a batch)
//     and get the response as application/json (202 for notifications);
//     no SSE stream (GET → 405).
//
// The Nexus bridge (nexus.go) builds a per-run server from a run's
// kernel.Tool list, gates calls with internal/runtime/approvalgate and
// optionally adds the Claude Code permission-prompt tool.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
)

// ProtocolVersion is the latest MCP revision implemented.
const ProtocolVersion = "2025-06-18"

// SupportedVersions are accepted in initialize, newest first.
var SupportedVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ToolNamePattern is what MCP clients (Claude Code, Codex) accept as a tool name.
var ToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

var badNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// SanitizeToolName maps a Nexus tool name ("memory.search") to an MCP tool
// name ("memory_search"): every character outside [a-zA-Z0-9_-] becomes "_"
// and the result is cut to 64 characters.
func SanitizeToolName(name string) string {
	s := badNameChars.ReplaceAllString(name, "_")
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		s = "_"
	}
	return s
}

// Content is one item of a tool result.
type Content struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// CallResult is the result of tools/call.
type CallResult struct {
	Content           []Content `json:"content"`
	StructuredContent any       `json:"structuredContent,omitempty"`
	IsError           bool      `json:"isError,omitempty"`
}

// TextResult is a successful single-text result.
func TextResult(text string) *CallResult {
	return &CallResult{Content: []Content{{Type: "text", Text: text}}}
}

// ErrorResult is a tool execution error (reported in the result, not as a
// protocol error, so the model can see it).
func ErrorResult(msg string) *CallResult {
	return &CallResult{Content: []Content{{Type: "text", Text: msg}}, IsError: true}
}

// Tool is a tool served over MCP.
type Tool struct {
	// Name must match ToolNamePattern.
	Name        string
	Title       string
	Description string
	// InputSchema is a JSON Schema object (default {"type":"object"}).
	InputSchema json.RawMessage
	// Call runs the tool. A returned error becomes an isError result.
	Call func(ctx context.Context, args json.RawMessage) (*CallResult, error)
}

// Implementation identifies the server.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Server dispatches MCP messages. Safe for concurrent use.
type Server struct {
	Info         Implementation
	Instructions string
	Log          *slog.Logger

	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

// NewServer creates a server without tools.
func NewServer(name, version string) *Server {
	return &Server{Info: Implementation{Name: name, Version: version}, tools: map[string]Tool{}}
}

// AddTool registers a tool.
func (s *Server) AddTool(t Tool) error {
	if !ToolNamePattern.MatchString(t.Name) {
		return fmt.Errorf("mcp: invalid tool name %q", t.Name)
	}
	if t.Call == nil {
		return fmt.Errorf("mcp: tool %q has no handler", t.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.tools[t.Name]; dup {
		return fmt.Errorf("mcp: duplicate tool %q", t.Name)
	}
	s.tools[t.Name] = t
	s.order = append(s.order, t.Name)
	return nil
}

// ToolNames lists registered tools in registration order.
func (s *Server) ToolNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.order...)
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ---- JSON-RPC ----

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

var nullID = json.RawMessage("null")

func errorResponse(id json.RawMessage, code int, msg string) []byte {
	if len(id) == 0 {
		id = nullID
	}
	b, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}})
	return b
}

// Handle processes one JSON-RPC message or batch and returns the response
// bytes, or nil when there is nothing to answer (notifications, responses).
func (s *Server) Handle(ctx context.Context, data []byte) []byte {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return errorResponse(nil, CodeInvalidRequest, "empty message")
	}
	if data[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(data, &batch); err != nil {
			return errorResponse(nil, CodeParseError, "parse error: "+err.Error())
		}
		if len(batch) == 0 {
			return errorResponse(nil, CodeInvalidRequest, "empty batch")
		}
		out := make([]json.RawMessage, len(batch))
		var wg sync.WaitGroup
		for i, m := range batch {
			wg.Add(1)
			go func(i int, m json.RawMessage) {
				defer wg.Done()
				out[i] = s.handleOne(ctx, m)
			}(i, m)
		}
		wg.Wait()
		var resp []json.RawMessage
		for _, r := range out {
			if r != nil {
				resp = append(resp, r)
			}
		}
		if len(resp) == 0 {
			return nil
		}
		b, _ := json.Marshal(resp)
		return b
	}
	return s.handleOne(ctx, data)
}

// IsNotification reports whether data is a notification or response only
// (no reply expected); used by transports to pick 202 vs 200.
func IsNotification(data []byte) bool {
	data = bytes.TrimSpace(data)
	var msgs []rpcMessage
	if len(data) > 0 && data[0] == '[' {
		if json.Unmarshal(data, &msgs) != nil {
			return false
		}
	} else {
		var m rpcMessage
		if json.Unmarshal(data, &m) != nil {
			return false
		}
		msgs = []rpcMessage{m}
	}
	for _, m := range msgs {
		if m.Method != "" && len(m.ID) > 0 {
			return false
		}
		if m.Method == "" && len(m.ID) == 0 {
			return false // malformed: answer with an error
		}
	}
	return true
}

func (s *Server) handleOne(ctx context.Context, data []byte) []byte {
	var m rpcMessage
	if err := json.Unmarshal(data, &m); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			return errorResponse(nil, CodeParseError, "parse error: "+err.Error())
		}
		return errorResponse(nil, CodeInvalidRequest, "invalid request: "+err.Error())
	}
	if m.JSONRPC != "2.0" {
		return errorResponse(m.ID, CodeInvalidRequest, `jsonrpc must be "2.0"`)
	}
	if m.Method == "" {
		if len(m.ID) > 0 && (len(m.Result) > 0 || len(m.Error) > 0) {
			return nil // a response to a server request; we send none
		}
		return errorResponse(m.ID, CodeInvalidRequest, "missing method")
	}
	notification := len(m.ID) == 0
	if !notification && bytes.Equal(bytes.TrimSpace(m.ID), nullID) {
		return errorResponse(nil, CodeInvalidRequest, "id must not be null")
	}
	result, rerr := s.dispatch(ctx, m.Method, m.Params, notification)
	if notification {
		return nil
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: m.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		if result == nil {
			result = struct{}{}
		}
		resp.Result = result
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return errorResponse(m.ID, CodeInternalError, "marshal: "+err.Error())
	}
	return b
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage, notification bool) (any, *RPCError) {
	switch method {
	case "initialize":
		return s.initialize(params)
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return s.listTools(), nil
	case "tools/call":
		return s.callTool(ctx, params)
	}
	if notification {
		return nil, nil // notifications/initialized, notifications/cancelled, …
	}
	return nil, &RPCError{Code: CodeMethodNotFound, Message: "method not found: " + method}
}

func (s *Server) initialize(params json.RawMessage) (any, *RPCError) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "invalid initialize params: " + err.Error()}
		}
	}
	version := ProtocolVersion
	for _, v := range SupportedVersions {
		if v == p.ProtocolVersion {
			version = v
		}
	}
	if p.ClientInfo != nil {
		s.log().Debug("mcp: client initialized", "client", p.ClientInfo.Name, "version", p.ClientInfo.Version, "protocol", version)
	}
	res := map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      s.Info,
	}
	if s.Instructions != "" {
		res["instructions"] = s.Instructions
	}
	return res, nil
}

type toolDesc struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

var defaultSchema = json.RawMessage(`{"type":"object"}`)

func (s *Server) listTools() any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]toolDesc, 0, len(s.order))
	for _, n := range s.order {
		t := s.tools[n]
		schema := t.InputSchema
		if len(bytes.TrimSpace(schema)) == 0 || !json.Valid(schema) {
			schema = defaultSchema
		}
		list = append(list, toolDesc{Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: schema})
	}
	return map[string]any{"tools": list}
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "tools/call needs a name"}
	}
	s.mu.RLock()
	t, ok := s.tools[p.Name]
	s.mu.RUnlock()
	if !ok {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "unknown tool: " + p.Name}
	}
	args := bytes.TrimSpace(p.Arguments)
	if len(args) == 0 || bytes.Equal(args, nullID) {
		args = []byte("{}")
	}
	res, err := safeCall(ctx, t, args)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	if res == nil {
		res = TextResult("")
	}
	if res.Content == nil {
		res.Content = []Content{}
	}
	return res, nil
}

func safeCall(ctx context.Context, t Tool, args json.RawMessage) (res *CallResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("tool panicked: %v", p)
		}
	}()
	return t.Call(ctx, args)
}
