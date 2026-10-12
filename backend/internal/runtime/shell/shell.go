// Package shell is the "shell" runtime (node module "runtime.shell"): it runs
// a command with `sh -c` in the run workspace, streams stdout/stderr lines as
// RunOutput events and maps the exit code to SUCCEEDED/FAILED. Cancel kills
// the whole process group.
//
// The command is, in order of preference:
//  1. the agent instructions when they contain "{{prompt}}" — the
//     placeholder is replaced by the shell-quoted prompt;
//  2. the prompt;
//  3. the agent instructions.
//
// Ask before acting: the command is gated with action class COMPUTE
// (payload {action:"shell.exec", command, workdir, node}) through
// internal/runtime/approvalgate. At the default level (auto) it runs at once;
// when the agent policy tightens compute to approve/biometric (the vault's
// shell agent sets `compute: approve`) the run emits an Approval, waits in
// WAITING_APPROVAL and only starts the process after Resolve delivers an
// approval. A denial ends the run FAILED "denied by user"; Cancel while
// waiting ends it CANCELLED.
package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Placeholder in agent instructions replaced by the quoted prompt.
const Placeholder = "{{prompt}}"

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Config is the "runtime.shell" section.
type Config struct {
	// Timeout when the agent sets no max_runtime_seconds (default 10m).
	Timeout time.Duration `yaml:"timeout"`
	// Shell binary (default "sh").
	Shell string `yaml:"shell"`
}

// Module registers the shell runtime.
type Module struct{ kernel.BaseModule }

func (Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name: "runtime.shell", Description: "Runs commands with sh -c in the run workspace",
		Side: kernel.SideNode, DefaultEnabled: true,
	}
}

func (Module) Init(ctx context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	return h.Runtimes().Register(&Runtime{Timeout: cfg.Timeout, Shell: cfg.Shell})
}

// Runtime is the shell runtime.
type Runtime struct {
	Timeout time.Duration
	Shell   string
}

func (*Runtime) Name() string { return "shell" }
func (*Runtime) Capabilities() kernel.RuntimeCapabilities {
	return kernel.RuntimeCapabilities{Streaming: true, ApprovalHooks: true}
}

func (r *Runtime) shell() string {
	if r.Shell != "" {
		return r.Shell
	}
	return "sh"
}

func (r *Runtime) Available(context.Context) (string, bool) {
	p, err := exec.LookPath(r.shell())
	return p, err == nil
}

// Command builds the script for an assignment.
func Command(a *nexusv1.RunAssignment) string {
	if strings.Contains(a.Instructions, Placeholder) {
		return strings.ReplaceAll(a.Instructions, Placeholder, Quote(a.Prompt))
	}
	if strings.TrimSpace(a.Prompt) != "" {
		return a.Prompt
	}
	return a.Instructions
}

// Quote single-quotes s for POSIX sh.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (r *Runtime) Start(ctx context.Context, spec kernel.RunSpec) (kernel.Run, error) {
	a := spec.Assignment
	if a == nil {
		return nil, errors.New("shell: missing assignment")
	}
	script := Command(a)
	if strings.TrimSpace(script) == "" {
		return nil, errors.New("shell: empty command")
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if s := a.GetAgent().GetLimits().GetMaxRuntimeSeconds(); s > 0 {
		timeout = time.Duration(s) * time.Second
	}
	cmd := exec.Command(r.shell(), "-c", script)
	cmd.Dir = spec.Workdir
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "NEXUS_RUN_ID="+a.RunId, "NEXUS_TASK_ID="+a.TaskId)
	setProcessGroup(cmd)

	run := &run{a: a, cmd: cmd, events: make(chan *nexusv1.Event, 64)}
	node := NodeName(spec)
	run.gate = approvalgate.New(approvalgate.Config{
		TaskID: a.TaskId, RunID: a.RunId, Agent: a.GetAgent(), NodeID: spec.Env["NEXUS_NODE_ID"], Emit: run.emit,
	})
	req := approvalgate.Request{
		Class:   nexusv1.ActionClass_ACTION_CLASS_COMPUTE,
		Title:   "Run a shell command on " + node,
		Summary: truncate(script, 500),
		Payload: map[string]any{"action": "shell.exec", "command": script, "workdir": spec.Workdir, "node": node},
	}
	if !run.gate.NeedsApproval(req.Class) {
		// auto / notify: run right away (Start reports start errors).
		if _, err := run.gate.Check(ctx, req); err != nil {
			return nil, err
		}
		stdout, stderr, err := run.startProcess()
		if err != nil {
			return nil, err
		}
		go run.wait(ctx, timeout, stdout, stderr)
		return run, nil
	}
	go run.gated(ctx, req, timeout)
	return run, nil
}

// NodeName identifies this machine in approval payloads: NEXUS_NODE_ID from
// the run env when the node provides it, else the host name.
func NodeName(spec kernel.RunSpec) string {
	if id := spec.Env["NEXUS_NODE_ID"]; id != "" {
		return id
	}
	h, _ := os.Hostname()
	return h
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type run struct {
	a      *nexusv1.RunAssignment
	cmd    *exec.Cmd
	gate   *approvalgate.Gate
	events chan *nexusv1.Event

	mu        sync.Mutex
	started   bool
	cancelled bool
	timedOut  bool
	last      []string // last output lines for the summary
}

var errCancelled = errors.New("shell: cancelled")

// startProcess starts the command unless the run was cancelled.
func (r *run) startProcess() (io.Reader, io.Reader, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelled {
		return nil, nil, errCancelled
	}
	stdout, err := r.cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := r.cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := r.cmd.Start(); err != nil {
		return nil, nil, err
	}
	r.started = true
	return stdout, stderr, nil
}

// gated waits for the approval, then runs the command.
func (r *run) gated(ctx context.Context, req approvalgate.Request, timeout time.Duration) {
	finish := func(state nexusv1.TaskState, summary string) {
		r.gate.Close()
		r.state(state, summary, nil)
		close(r.events)
	}
	out, err := r.gate.Check(ctx, req)
	r.mu.Lock()
	cancelled := r.cancelled
	r.mu.Unlock()
	switch {
	case cancelled || errors.Is(err, approvalgate.ErrClosed) || ctx.Err() != nil:
		finish(nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled")
		return
	case err != nil:
		finish(nexusv1.TaskState_TASK_STATE_FAILED, "approval failed: "+err.Error())
		return
	case !out.Approved:
		summary := "denied by user"
		if out.Reason != "" && out.Reason != "denied" {
			summary = "not approved: " + out.Reason
		}
		finish(nexusv1.TaskState_TASK_STATE_FAILED, summary)
		return
	}
	stdout, stderr, err := r.startProcess()
	if errors.Is(err, errCancelled) {
		finish(nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled")
		return
	}
	if err != nil {
		finish(nexusv1.TaskState_TASK_STATE_FAILED, "shell: "+err.Error())
		return
	}
	r.wait(ctx, timeout, stdout, stderr)
}

func (r *run) Events() <-chan *nexusv1.Event { return r.events }

func (r *run) Send(context.Context, *nexusv1.RunInput) error {
	return errors.New("shell: runtime does not accept input")
}

// Resolve delivers an approval decision (already verified by the node) to
// the pending command approval.
func (r *run) Resolve(ctx context.Context, res *nexusv1.ApprovalResolution) error {
	return r.gate.Resolve(ctx, res)
}

func (r *run) Cancel(context.Context) error {
	r.mu.Lock()
	r.cancelled = true
	started := r.started
	r.mu.Unlock()
	r.gate.Close()
	if !started {
		return nil
	}
	return killGroup(r.cmd)
}

func (r *run) state(s nexusv1.TaskState, summary string, result *structpb.Struct) {
	r.emit(&nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{
		Id: r.a.TaskId, CurrentRunId: r.a.RunId, State: s, Summary: summary, Result: result,
	}}}})
}

func (r *run) emit(ev *nexusv1.Event) {
	ev.TaskId, ev.RunId, ev.At = r.a.TaskId, r.a.RunId, timestamppb.Now()
	r.events <- ev
}

func (r *run) pipe(wg *sync.WaitGroup, rd io.Reader, prefix string) {
	defer wg.Done()
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := prefix + sc.Text()
		r.mu.Lock()
		r.last = append(r.last, line)
		if len(r.last) > 5 {
			r.last = r.last[1:]
		}
		r.mu.Unlock()
		r.emit(&nexusv1.Event{Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{Text: line + "\n"}}})
	}
	_, _ = io.Copy(io.Discard, rd)
}

func (r *run) wait(ctx context.Context, timeout time.Duration, stdout, stderr io.Reader) {
	defer close(r.events)
	defer r.gate.Close()
	timer := time.AfterFunc(timeout, func() {
		r.mu.Lock()
		r.timedOut = true
		r.mu.Unlock()
		_ = killGroup(r.cmd)
	})
	defer timer.Stop()
	stop := context.AfterFunc(ctx, func() { _ = r.Cancel(context.Background()) })
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go r.pipe(&wg, stdout, "")
	go r.pipe(&wg, stderr, "[stderr] ")
	wg.Wait()
	err := r.cmd.Wait()

	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	r.mu.Lock()
	cancelled, timedOut, last := r.cancelled, r.timedOut, strings.Join(r.last, "\n")
	r.mu.Unlock()

	state := nexusv1.TaskState_TASK_STATE_SUCCEEDED
	summary := fmt.Sprintf("exit code %d", exit)
	switch {
	case cancelled:
		state, summary = nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled"
	case timedOut:
		state, summary = nexusv1.TaskState_TASK_STATE_FAILED, fmt.Sprintf("timed out after %s", timeout)
	case exit != 0:
		state = nexusv1.TaskState_TASK_STATE_FAILED
	}
	if last != "" && !cancelled {
		summary += "\n" + last
	}
	result, _ := structpb.NewStruct(map[string]any{"exit_code": exit})
	r.emit(&nexusv1.Event{Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{IsFinal: true}}})
	r.state(state, summary, result)
}
