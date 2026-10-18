package anthrex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CLI is the command-line fallback implementation of API: it runs
// `anthrex …` (crates/cli) and parses its --json output. It covers what the
// CLI exposes; PTY streams, AnswerAsk, TaskDetail, Conversation and
// SendInput return ErrUnsupported.
type CLI struct {
	Bin string
	// Env is added to the process environment (ANTHREX_* passthrough).
	Env map[string]string
	// PollInterval for WatchRuns (default 3s).
	PollInterval time.Duration
	version      string
}

// ExitQueued is `anthrex run start --goal`'s exit code for a goal queued
// for its repository's profile (crates/cli/src/run_cmd/adapt.rs).
const ExitQueued = 3

// NewCLI resolves the binary. socket, when set, is passed as ANTHREX_SOCKET.
func NewCLI(bin, socket string, env map[string]string) (*CLI, error) {
	p, err := LookBin(bin)
	if err != nil {
		return nil, err
	}
	e := map[string]string{}
	for k, v := range env {
		e[k] = v
	}
	if socket != "" {
		e["ANTHREX_SOCKET"] = expandHome(socket)
	}
	return &CLI{Bin: p, Env: e}, nil
}

// Mode implements API.
func (c *CLI) Mode() string { return "cli" }

// Version implements API (`anthrex --version`, cached).
func (c *CLI) Version() string {
	if c.version != "" {
		return c.version
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _, err := c.exec(ctx, "", "--version")
	if err == nil {
		c.version = strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	}
	return c.version
}

// Close implements API.
func (c *CLI) Close() error { return nil }

// ExitError is a failed CLI invocation.
type ExitError struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = "exit " + strconv.Itoa(e.Code)
	}
	return "anthrex " + strings.Join(e.Args, " ") + ": " + msg
}

func (c *CLI) exec(ctx context.Context, dir string, args ...string) (string, string, error) {
	return c.execTimeout(ctx, DefaultRequestTimeout, dir, args...)
}

func (c *CLI) execTimeout(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, string, error) {
	ctx, cancel := withDefault(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Dir = dir
	env := os.Environ()
	for k, v := range c.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Stdin = nil
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout.String(), stderr.String(), &ExitError{Args: args, Code: ee.ExitCode(), Stderr: stderr.String()}
		}
		return stdout.String(), stderr.String(), fmt.Errorf("anthrex %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), stderr.String(), nil
}

func (c *CLI) message(ctx context.Context, args ...string) (string, error) {
	out, _, err := c.exec(ctx, "", args...)
	return strings.TrimSpace(out), err
}

// ListRuns implements API (`anthrex run status --json`).
func (c *CLI) ListRuns(ctx context.Context) (*RunsSnapshot, error) {
	out, _, err := c.exec(ctx, "", "run", "status", "--json")
	if err != nil {
		return nil, err
	}
	var s RunsSnapshot
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return nil, fmt.Errorf("anthrex run status --json: %w", err)
	}
	return &s, nil
}

// WatchRuns implements API by polling ListRuns; a snapshot is sent when its
// revision changes.
func (c *CLI) WatchRuns(ctx context.Context) (<-chan *RunsSnapshot, error) {
	first, err := c.ListRuns(ctx)
	if err != nil {
		return nil, err
	}
	every := c.PollInterval
	if every <= 0 {
		every = 3 * time.Second
	}
	out := make(chan *RunsSnapshot, 1)
	out <- first
	go func() {
		defer close(out)
		last := first.Revision
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			s, err := c.ListRuns(ctx)
			if err != nil || s.Revision == last {
				continue
			}
			last = s.Revision
			offerLatest(out, s)
		}
	}()
	return out, nil
}

// StartGoal implements API (`anthrex --dir D run start --goal G …`).
func (c *CLI) StartGoal(ctx context.Context, g StartGoalRequest) (*StartResult, error) {
	if _, err := ReqStartGoal(g); err != nil { // same validation as the socket
		return nil, err
	}
	args := []string{"--dir", g.Dir, "run", "start", "--goal", g.Goal}
	if g.Yes {
		args = append(args, "--yes")
	}
	if g.TrustProject {
		args = append(args, "--trust-project")
	}
	if g.UnconfinedChecks {
		args = append(args, "--unconfined-checks")
	}
	if g.Orchestrator != "" {
		args = append(args, "--orchestrator", g.Orchestrator)
	}
	if g.Delivery != "" {
		args = append(args, "--delivery", g.Delivery)
	}
	if g.ContinueFrom != "" {
		args = append(args, "--continue", g.ContinueFrom)
	}
	if g.Design != "" {
		args = append(args, "--design", g.Design)
	}
	out, stderr, err := c.execTimeout(ctx, GoalRequestTimeout, g.Dir, args...)
	var ee *ExitError
	if errors.As(err, &ee) && ee.Code == ExitQueued {
		return &StartResult{Queued: true, Message: strings.TrimSpace(stderr)}, nil
	}
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	if id == "" {
		return nil, fmt.Errorf("anthrex run start: no run id printed (%s)", strings.TrimSpace(stderr))
	}
	return &StartResult{RunID: id, Message: strings.TrimSpace(stderr)}, nil
}

// Approve implements API.
func (c *CLI) Approve(ctx context.Context, runID string) (string, error) {
	return c.message(ctx, "run", "approve", runID)
}

// Reject implements API.
func (c *CLI) Reject(ctx context.Context, runID string) (string, error) {
	return c.message(ctx, "run", "reject", runID, "--confirm", runID)
}

// ApproveHold implements API.
func (c *CLI) ApproveHold(ctx context.Context, runID, hold string) (string, error) {
	return c.message(ctx, "run", "approve", runID, "--hold", hold)
}

// RejectHold implements API.
func (c *CLI) RejectHold(ctx context.Context, runID, hold string) (string, error) {
	return c.message(ctx, "run", "reject", runID, "--hold", hold)
}

// DocGate implements API (`run approve --gate`, `run changes`, `run
// rethink`, `run back`; edit and reject have no one-line CLI form here).
func (c *CLI) DocGate(ctx context.Context, runID, gate string, a DocGateAction) (string, error) {
	if err := checkEnum("gate", gate, GateBrainstorm, GateSpec, GatePlan); err != nil {
		return "", err
	}
	switch a.Kind {
	case "approve":
		args := []string{"run", "approve", runID, "--gate", gate}
		if a.Version != nil {
			args = append(args, "--version", strconv.FormatUint(uint64(*a.Version), 10))
		}
		return c.message(ctx, args...)
	case "changes":
		args := []string{"run", "changes", runID, "--gate", gate, "--note", a.Note}
		if a.Review {
			args = append(args, "--review")
		}
		return c.message(ctx, args...)
	case "rethink":
		return c.message(ctx, "run", "rethink", runID, "--note", a.Note)
	case "back":
		return c.message(ctx, "run", "back", runID, "--gate", gate, "--note", a.Note)
	}
	return "", fmt.Errorf("%w: doc gate action %q over the CLI", ErrUnsupported, a.Kind)
}

// AnswerAsk implements API: not exposed by the CLI.
func (c *CLI) AnswerAsk(context.Context, string, uint64, *uint32) (string, error) {
	return "", fmt.Errorf("%w: answering the orchestrator needs the daemon socket", ErrUnsupported)
}

// Message implements API (`run message RUN --kind K TO TEXT`).
func (c *CLI) Message(ctx context.Context, runID, to, text, kind string) (string, error) {
	if _, err := EditMessage(to, text, kind); err != nil {
		return "", err
	}
	if kind == "" {
		kind = MessageInfo
	}
	return c.message(ctx, "run", "message", runID, "--kind", kind, to, text)
}

// Cancel implements API.
func (c *CLI) Cancel(ctx context.Context, runID string) (string, error) {
	return c.message(ctx, "run", "cancel", runID)
}

// Resume implements API.
func (c *CLI) Resume(ctx context.Context, runID string, rebaseline bool) (string, error) {
	args := []string{"run", "resume", runID}
	if rebaseline {
		args = append(args, "--rebaseline")
	}
	return c.message(ctx, args...)
}

// Finish implements API. The CLI has no two-step form: an empty confirm
// reports ConfirmNeeded with a generic prompt; a confirm performs it
// (accept --yes [--base <sha>], discard --confirm <run>).
func (c *CLI) Finish(ctx context.Context, runID, action, confirm string) (*FinishResult, error) {
	if confirm == "" {
		return &FinishResult{ConfirmNeeded: true, Prompt: action + " run " + runID + "?"}, nil
	}
	var args []string
	switch action {
	case FinishAccept:
		args = []string{"run", "accept", runID, "--yes"}
		if i := strings.IndexByte(confirm, '@'); i >= 0 {
			args = append(args, "--base", confirm[i+1:])
		}
	case FinishDiscard:
		args = []string{"run", "discard", runID, "--confirm", runID}
	default:
		return nil, fmt.Errorf("anthrex: finish action %q", action)
	}
	out, _, err := c.execTimeout(ctx, FinishRequestTimeout, "", args...)
	if err != nil {
		return nil, err
	}
	return &FinishResult{Done: true, Message: strings.TrimSpace(out)}, nil
}

// ShowDoc implements API (`run show RUN --doc K`, text).
func (c *CLI) ShowDoc(ctx context.Context, runID, kind string, version *uint32) (any, error) {
	args := []string{"run", "show", runID, "--doc", kind}
	if version != nil {
		args = append(args, "--version", strconv.FormatUint(uint64(*version), 10))
	}
	out, _, err := c.exec(ctx, "", args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": out}, nil
}

// TaskDetail implements API: not exposed by the CLI.
func (c *CLI) TaskDetail(context.Context, string, string) (any, error) {
	return nil, fmt.Errorf("%w: task detail needs the daemon socket", ErrUnsupported)
}

func (c *CLI) jsonOut(ctx context.Context, dir string, args ...string) (any, error) {
	out, _, err := c.exec(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	return DecodeJSON([]byte(out))
}

// Stats implements API (`run stats --json` in dir).
func (c *CLI) Stats(ctx context.Context, dir string) (any, error) {
	return c.jsonOut(ctx, dir, "--dir", dir, "run", "stats", "--json")
}

// ProfileStatus implements API (`profile status --json` in dir).
func (c *CLI) ProfileStatus(ctx context.Context, dir string) (any, error) {
	return c.jsonOut(ctx, dir, "--dir", dir, "profile", "status", "--json")
}

// ListWindows implements API (`ls --json`).
func (c *CLI) ListWindows(ctx context.Context) ([]WindowInfo, error) {
	out, _, err := c.exec(ctx, "", "ls", "--json")
	if err != nil {
		return nil, err
	}
	var w []WindowInfo
	if err := json.Unmarshal([]byte(out), &w); err != nil {
		return nil, fmt.Errorf("anthrex ls --json: %w", err)
	}
	return w, nil
}

// CreateWindow implements API (`anthrex --dir CWD new …`, prints the id).
func (c *CLI) CreateWindow(ctx context.Context, spec WindowSpec) (uint32, error) {
	args := []string{"--dir", spec.Cwd, "new"}
	if spec.Runtime != "" {
		args = append(args, "--runtime", spec.Runtime)
	}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	if spec.WorktreeBranch != "" {
		args = append(args, "--worktree", spec.WorktreeBranch)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.InitialPrompt != "" {
		args = append(args, "--prompt", spec.InitialPrompt)
	}
	out, _, err := c.execTimeout(ctx, WindowRequestTimeout+10*time.Second, spec.Cwd, args...)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("anthrex new: unexpected output %q", strings.TrimSpace(out))
	}
	return uint32(n), nil
}

// SendInput implements API: not exposed by the CLI.
func (c *CLI) SendInput(context.Context, uint32, []byte) error {
	return fmt.Errorf("%w: terminal input needs the daemon socket", ErrUnsupported)
}

// Conversation implements API: not exposed by the CLI.
func (c *CLI) Conversation(context.Context, uint32, string) (*Conversation, error) {
	return nil, fmt.Errorf("%w: conversations need the daemon socket", ErrUnsupported)
}
