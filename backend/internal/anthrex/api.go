package anthrex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// API is what Nexus uses of Anthrex. Two implementations: the socket client
// (Supervisor, full protocol) and the CLI fallback (CLI, the subset the
// `anthrex` command line exposes). Methods returning a string return the
// daemon's Done message.
type API interface {
	// Mode is "socket" or "cli".
	Mode() string
	// Version is the daemon (socket) or binary (cli) version.
	Version() string

	ListRuns(ctx context.Context) (*RunsSnapshot, error)
	// WatchRuns streams runs snapshots (latest wins) until ctx ends. The
	// socket client re-subscribes across reconnects; the CLI polls.
	WatchRuns(ctx context.Context) (<-chan *RunsSnapshot, error)

	StartGoal(ctx context.Context, r StartGoalRequest) (*StartResult, error)
	Approve(ctx context.Context, runID string) (string, error)
	Reject(ctx context.Context, runID string) (string, error)
	ApproveHold(ctx context.Context, runID, hold string) (string, error)
	RejectHold(ctx context.Context, runID, hold string) (string, error)
	DocGate(ctx context.Context, runID, gate string, a DocGateAction) (string, error)
	AnswerAsk(ctx context.Context, runID string, ask uint64, choice *uint32) (string, error)
	// Message sends PlanEdit::Message: to is "t1,t2", "stage:<n>" or
	// "running"; kind info | change | stop_and_wait.
	Message(ctx context.Context, runID, to, text, kind string) (string, error)
	Cancel(ctx context.Context, runID string) (string, error)
	Resume(ctx context.Context, runID string, rebaseline bool) (string, error)
	// Finish sends RunRequest::Finish. With confirm "" the daemon answers
	// ConfirmNeeded (FinishResult.ConfirmNeeded); confirm "<run id>" (or
	// "<run id>@<sha>" for a moved base) then performs it.
	Finish(ctx context.Context, runID, action, confirm string) (*FinishResult, error)

	ShowDoc(ctx context.Context, runID, kind string, version *uint32) (any, error)
	TaskDetail(ctx context.Context, runID, taskID string) (any, error)
	Stats(ctx context.Context, dir string) (any, error)
	ProfileStatus(ctx context.Context, dir string) (any, error)

	ListWindows(ctx context.Context) ([]WindowInfo, error)
	CreateWindow(ctx context.Context, spec WindowSpec) (uint32, error)
	SendInput(ctx context.Context, windowID uint32, b []byte) error
	// Conversation returns one snapshot of a window's (or sub-agent's)
	// conversation.
	Conversation(ctx context.Context, windowID uint32, agentID string) (*Conversation, error)

	Close() error
}

// StartResult is StartGoal's outcome.
type StartResult struct {
	RunID string `json:"run_id,omitempty"`
	State string `json:"state,omitempty"`
	// Queued: the repository has no confirmed profile yet; the goal waits
	// (GoalID) and starts as a run once the profile is set up.
	Queued  bool   `json:"queued,omitempty"`
	GoalID  string `json:"goal_id,omitempty"`
	Message string `json:"message,omitempty"`
}

// FinishResult is Finish's outcome.
type FinishResult struct {
	Done          bool           `json:"done"`
	Message       string         `json:"message,omitempty"`
	ConfirmNeeded bool           `json:"confirm_needed,omitempty"`
	Prompt        string         `json:"prompt,omitempty"`
	BaseMoved     *BaseMovedInfo `json:"base_moved,omitempty"`
}

// OpenOptions select and configure a client.
type OpenOptions struct {
	Options
	// Mode: "auto" (socket, else CLI), "socket" or "cli".
	Mode string
	// Bin is the anthrex binary for the CLI fallback (default "anthrex").
	Bin string
	// Env is added to the CLI's environment (ANTHREX_* passthrough).
	Env map[string]string
}

// Open returns a client: the socket protocol when the daemon answers the
// handshake with our version, else the CLI when its binary is found. A
// version mismatch on the socket falls back to the CLI (which always
// matches its own daemon).
func Open(ctx context.Context, o OpenOptions) (API, error) {
	var sockErr error
	if o.Mode != "cli" {
		s := NewSupervisor(o.Options)
		if err := s.Ping(ctx); err == nil {
			return s, nil
		} else {
			sockErr = err
			s.Close()
		}
		if o.Mode == "socket" {
			return nil, sockErr
		}
	}
	cli, err := NewCLI(o.Bin, o.Socket, o.Env)
	if err != nil {
		if sockErr != nil {
			return nil, fmt.Errorf("%w (cli: %v)", sockErr, err)
		}
		return nil, err
	}
	if sockErr != nil && !errors.Is(sockErr, ErrProtoVersion) && !errors.Is(sockErr, ErrUnavailable) {
		return nil, sockErr
	}
	return cli, nil
}

// LookBin resolves the anthrex binary ("" = "anthrex" on PATH).
func LookBin(bin string) (string, error) {
	if bin == "" {
		bin = "anthrex"
	}
	bin = expandHome(bin)
	p, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("%w: binary %q not found", ErrUnavailable, bin)
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", fmt.Errorf("%w: binary %q not usable", ErrUnavailable, bin)
	}
	return p, nil
}
