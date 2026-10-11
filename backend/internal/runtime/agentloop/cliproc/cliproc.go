// Package cliproc holds helpers shared by runtimes that drive an external
// agent CLI (claude, codex): process groups, version probes, workdir
// resolution, stderr capture and line reading.
package cliproc

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Version runs "<binary> --version" and returns the first output line.
func Version(ctx context.Context, binary string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line, true
}

// WorkdirOptions resolve where a CLI agent runs.
type WorkdirOptions struct {
	// PerAgent maps agent id → directory (e.g. a repo checkout).
	PerAgent map[string]string
	// AllowedRoots bounds context.repo_dir: it is honoured only when it lies
	// under one of these directories. Empty = context.repo_dir is ignored.
	AllowedRoots []string
}

// Workdir picks the run directory: context "repo_dir" (if allowed), then the
// per-agent directory, then the run's own workdir.
func Workdir(a *nexusv1.RunAssignment, runWorkdir string, o WorkdirOptions) (string, error) {
	if v := a.GetContext().GetFields()["repo_dir"]; v != nil && v.GetStringValue() != "" {
		dir := filepath.Clean(expandHome(v.GetStringValue()))
		if !underAny(dir, o.AllowedRoots) {
			return "", errors.New("repo_dir " + dir + " is not under an allowed root")
		}
		return dir, nil
	}
	if d := o.PerAgent[a.GetAgent().GetId()]; d != "" {
		return filepath.Clean(expandHome(d)), nil
	}
	return runWorkdir, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func underAny(dir string, roots []string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for _, r := range roots {
		ra, err := filepath.Abs(expandHome(r))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(ra, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Env merges the process environment with extra maps (later wins).
func Env(extra ...map[string]string) []string {
	env := os.Environ()
	for _, m := range extra {
		for k, v := range m {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Tail keeps the last N bytes written to it (stderr capture).
type Tail struct {
	mu  sync.Mutex
	N   int
	buf []byte
}

func (t *Tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.N
	if n <= 0 {
		n = 4096
	}
	t.buf = append(t.buf, p...)
	if len(t.buf) > n {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-n:]...)
	}
	return len(p), nil
}

func (t *Tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// ReadLines calls fn for each non-empty line of r (no length limit).
func ReadLines(r io.Reader, fn func(line []byte)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if l := strings.TrimSpace(string(line)); l != "" {
			fn([]byte(l))
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Process wraps a started command running in its own process group.
type Process struct {
	Cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// Start starts cmd in a new process group.
func Start(cmd *exec.Cmd) (*Process, error) {
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Process{Cmd: cmd, done: make(chan struct{})}, nil
}

// Wait waits for the process (call once, after its pipes are drained).
func (p *Process) Wait() error {
	p.err = p.Cmd.Wait()
	close(p.done)
	return p.err
}

// Exited is closed once Wait has returned.
func (p *Process) Exited() <-chan struct{} { return p.done }

// Kill terminates the process group: SIGTERM, then SIGKILL after grace.
func (p *Process) Kill(grace time.Duration) {
	select {
	case <-p.done:
		return
	default:
	}
	terminateGroup(p.Cmd)
	go func() {
		t := time.NewTimer(grace)
		defer t.Stop()
		select {
		case <-p.done:
		case <-t.C:
			killGroup(p.Cmd)
		}
	}()
}
