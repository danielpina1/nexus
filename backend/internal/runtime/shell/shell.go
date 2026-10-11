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
	return kernel.RuntimeCapabilities{Streaming: true}
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
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	run := &run{a: a, cmd: cmd, events: make(chan *nexusv1.Event, 64)}
	go run.wait(ctx, timeout, stdout, stderr)
	return run, nil
}

type run struct {
	a      *nexusv1.RunAssignment
	cmd    *exec.Cmd
	events chan *nexusv1.Event

	mu        sync.Mutex
	cancelled bool
	timedOut  bool
	last      []string // last output lines for the summary
}

func (r *run) Events() <-chan *nexusv1.Event { return r.events }

func (r *run) Send(context.Context, *nexusv1.RunInput) error {
	return errors.New("shell: runtime does not accept input")
}

func (r *run) Resolve(context.Context, *nexusv1.ApprovalResolution) error {
	return errors.New("shell: runtime does not request approvals")
}

func (r *run) Cancel(context.Context) error {
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
	return killGroup(r.cmd)
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
	r.emit(&nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{
		Id: r.a.TaskId, CurrentRunId: r.a.RunId, State: state, Summary: summary, Result: result,
	}}}})
}
