package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Subscription providers: the hub calls the Claude Code or Codex CLI, logged
// in with the user's Claude / ChatGPT subscription, instead of an HTTP API.
// They serve plain chat (router, architect, summaries): one turn, no tools,
// no streaming deltas, no embeddings.
//
//	llm:
//	  providers:
//	    claude: { api: claude-cli }               # binary: claude (default)
//	    codex:  { api: codex-cli, timeout: 120s } # binary: codex (default)
//	  aliases:
//	    fast-local: { provider: claude, model: haiku }
const (
	APIClaudeCLI = "claude-cli"
	APICodexCLI  = "codex-cli"
)

// cliModels are the model names offered to aliases_auto for CLI providers.
var cliModels = map[string][]string{
	APIClaudeCLI: {"haiku", "sonnet", "opus"},
	APICodexCLI:  {"gpt-5-codex", "gpt-5"},
}

func isCLIAPI(api string) bool { return api == APIClaudeCLI || api == APICodexCLI }

// ErrCLIUnsupported is returned for operations a CLI provider cannot serve.
var ErrCLIUnsupported = errors.New("llm: not supported by a subscription CLI provider")

func (p ProviderConfig) binary() string {
	if b := strings.TrimSpace(p.Binary); b != "" {
		return b
	}
	if p.api() == APICodexCLI {
		return "codex"
	}
	return "claude"
}

// cliPrompt flattens a chat into one prompt: the system messages become the
// system prompt, the rest a transcript ending with the latest user turn.
func cliPrompt(req kernel.ChatRequest) (system, prompt string) {
	var sys, conv []string
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			sys = append(sys, m.Content)
		case "assistant":
			conv = append(conv, "Assistant: "+m.Content)
		default:
			conv = append(conv, "User: "+m.Content)
		}
	}
	if len(conv) == 1 {
		conv[0] = strings.TrimPrefix(conv[0], "User: ")
	}
	system = strings.Join(sys, "\n\n")
	if req.JSON {
		system = strings.TrimSpace(system + "\n\nAnswer with one JSON object only: no prose, no code fences.")
	}
	return system, strings.Join(conv, "\n\n")
}

// cliChat runs one non-interactive turn of the provider's CLI.
func (c *Client) cliChat(ctx context.Context, t Target, pc ProviderConfig, req kernel.ChatRequest) (*ChatResult, error) {
	timeout := pc.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	system, prompt := cliPrompt(req)
	dir, err := os.MkdirTemp("", "nexus-llm-cli-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	var args []string
	switch pc.api() {
	case APIClaudeCLI:
		args = []string{"-p", "--output-format", "json", "--max-turns", "1"}
		if t.Model != "" {
			args = append(args, "--model", t.Model)
		}
		if system != "" {
			args = append(args, "--append-system-prompt", system)
		}
	case APICodexCLI:
		args = []string{"exec", "--json", "--sandbox", "read-only", "--skip-git-repo-check"}
		if t.Model != "" {
			args = append(args, "--model", t.Model)
		}
		args = append(args, "-")
		if system != "" {
			prompt = system + "\n\n" + prompt
		}
	}
	cmd := exec.CommandContext(ctx, pc.binary(), args...)
	cmd.Dir = dir // an empty folder: no project instructions or files
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("llm: %s: %s timed out after %s", t.Provider, pc.binary(), timeout)
	}
	if runErr != nil && (errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, os.ErrNotExist)) {
		return nil, &APIError{Provider: t.Provider, StatusCode: 503, Message: pc.binary() + " is not installed or not on PATH"}
	}
	var text string
	var cost *nexusv1.Cost
	if pc.api() == APIClaudeCLI {
		text, cost, err = parseClaudeJSON(stdout.Bytes())
	} else {
		text, cost, err = parseCodexJSONL(stdout.Bytes())
	}
	if runErr != nil && (err != nil || text == "") {
		err = runErr // the exit status says more than unparsable output
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return nil, cliError(t.Provider, pc.binary(), err, msg)
	}
	if req.JSON {
		text = stripCodeFence(text)
	}
	return &ChatResult{
		Response:   &kernel.ChatResponse{Message: kernel.ChatMessage{Role: "assistant", Content: text}, Cost: cost},
		StopReason: "stop",
	}, nil
}

// cliError classifies CLI failures like HTTP ones so health tracking and
// fallbacks work: a missing binary or login is a provider failure.
func cliError(provider, bin string, err error, stderr string) error {
	low := strings.ToLower(err.Error() + " " + stderr)
	status := 500
	switch {
	case strings.Contains(low, "login") || strings.Contains(low, "logged in") || strings.Contains(low, "unauthorized") || strings.Contains(low, "auth"):
		status = 401
	case strings.Contains(low, "limit") || strings.Contains(low, "rate"):
		status = 429
	}
	msg := err.Error()
	if stderr != "" {
		msg += ": " + stderr
	}
	return &APIError{Provider: provider, StatusCode: status, Message: msg}
}

// parseClaudeJSON reads `claude -p --output-format json` output.
func parseClaudeJSON(b []byte) (string, *nexusv1.Cost, error) {
	var r struct {
		Type         string  `json:"type"`
		Subtype      string  `json:"subtype"`
		IsError      bool    `json:"is_error"`
		Result       string  `json:"result"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		Usage        struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	b = bytes.TrimSpace(b)
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 && !json.Valid(b) {
		b = b[i+1:] // some versions print a line before the result object
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", nil, fmt.Errorf("unexpected claude output: %v", err)
	}
	if r.IsError || (r.Subtype != "" && r.Subtype != "success") {
		return "", nil, fmt.Errorf("claude: %s", nonEmptyStr(r.Result, r.Subtype))
	}
	// Subscription usage: tokens only (the CLI's dollar figure is notional).
	return r.Result, &nexusv1.Cost{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}, nil
}

// parseCodexJSONL reads `codex exec --json` events: the last agent message
// is the answer; turn.completed carries usage.
func parseCodexJSONL(b []byte) (string, *nexusv1.Cost, error) {
	var text, failure string
	cost := &nexusv1.Cost{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var l struct {
			Type string `json:"type"`
			Item *struct {
				Type     string `json:"type"`
				ItemType string `json:"item_type"`
				Text     string `json:"text"`
			} `json:"item"`
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
			Message string `json:"message"`
			Error   *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		switch l.Type {
		case "item.completed":
			if l.Item != nil && (l.Item.Type == "agent_message" || l.Item.ItemType == "agent_message" || l.Item.ItemType == "assistant_message") {
				text = l.Item.Text
			}
		case "turn.completed":
			if l.Usage != nil {
				cost.InputTokens, cost.OutputTokens = l.Usage.InputTokens, l.Usage.OutputTokens
			}
		case "turn.failed", "error":
			failure = l.Message
			if failure == "" && l.Error != nil {
				failure = l.Error.Message
			}
			failure = nonEmptyStr(failure, "codex error")
		}
	}
	if text == "" && failure != "" {
		return "", nil, errors.New("codex: " + failure)
	}
	if text == "" {
		return "", nil, errors.New("codex: no answer in output")
	}
	return text, cost, nil
}

func stripCodeFence(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```json")
		t = strings.TrimPrefix(t, "```")
		t = strings.TrimSuffix(strings.TrimSpace(t), "```")
	}
	return strings.TrimSpace(t)
}

func nonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
