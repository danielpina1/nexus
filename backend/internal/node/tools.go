package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/nodelink/wire"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Node-local tool limits.
const (
	FSReadDefaultMax  = 256 << 10
	FSReadHardMax     = 4 << 20
	FSListMaxEntries  = 5000
	ShellDefaultLimit = 60 * time.Second
	ShellMaxLimit     = 10 * time.Minute
	ShellOutputCap    = 256 << 10
)

// RootFunc returns the directory a tool call is confined to (the run's
// workspace, or the workspaces dir for calls without a run).
type RootFunc func(call kernel.ToolCall) (string, error)

// Confine resolves rel inside root, rejecting paths (including symlinks)
// that escape it. Absolute paths are accepted only when inside root.
func Confine(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = r
	}
	var p string
	if filepath.IsAbs(rel) {
		p = filepath.Clean(rel)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
	} else {
		p = filepath.Join(absRoot, rel)
	}
	if !within(absRoot, p) {
		return "", fmt.Errorf("path %q escapes the workspace", rel)
	}
	if real := resolveExisting(p); !within(absRoot, real) {
		return "", fmt.Errorf("path %q escapes the workspace via symlink", rel)
	}
	return p, nil
}

// resolveExisting evaluates symlinks of the longest existing prefix of p and
// re-appends the missing tail.
func resolveExisting(p string) string {
	tail := ""
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, tail)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

func within(root, p string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// LocalTools returns the node-local tools: fs.list, fs.read, shell.exec.
func LocalTools(root RootFunc) []kernel.Tool {
	return []kernel.Tool{
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name:        wire.ToolFSList,
			Description: "List a directory inside the run workspace.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ,
		}, F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) { return fsList(root, c) }},
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name:        "fs.read",
			Description: "Read a file inside the run workspace (bounded size).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"max_bytes":{"type":"integer"}},"required":["path"]}`),
			ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ,
		}, F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) { return fsRead(root, c) }},
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name:        "shell.exec",
			Description: "Run a shell command in the run workspace (timeout, output capped).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"workdir":{"type":"string"},"timeout_seconds":{"type":"integer"}},"required":["command"]}`),
			ActionClass: nexusv1.ActionClass_ACTION_CLASS_COMPUTE,
		}, F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
			return shellExec(ctx, root, c)
		}},
	}
}

func fsList(root RootFunc, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, err
	}
	base, err := root(c)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	dir, err := Confine(base, in.Path)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	rel := func(p string) string {
		r, err := filepath.Rel(base, p)
		if err != nil {
			return p
		}
		return filepath.ToSlash(r)
	}
	out := wire.FSListOutput{Path: rel(dir), Entries: []wire.FileEntry{}}
	for i, de := range des {
		if i >= FSListMaxEntries {
			break
		}
		e := wire.FileEntry{Name: de.Name(), Path: rel(filepath.Join(dir, de.Name())), IsDir: de.IsDir()}
		if fi, err := de.Info(); err == nil {
			if !de.IsDir() {
				e.SizeBytes = fi.Size()
			}
			e.ModifiedAt = fi.ModTime().UTC().Format(time.RFC3339Nano)
		}
		out.Entries = append(out.Entries, e)
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Name < out.Entries[j].Name })
	return kernel.ToolOutput(out)
}

func fsRead(root RootFunc, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		Path     string `json:"path"`
		MaxBytes int64  `json:"max_bytes"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, err
	}
	if in.Path == "" {
		return kernel.ToolResult{}, errors.New("path required")
	}
	if in.MaxBytes <= 0 {
		in.MaxBytes = FSReadDefaultMax
	}
	in.MaxBytes = min(in.MaxBytes, FSReadHardMax)
	base, err := root(c)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	p, err := Confine(base, in.Path)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return kernel.ToolResult{}, err
	}
	if fi.IsDir() {
		return kernel.ToolResult{}, fmt.Errorf("%s is a directory", in.Path)
	}
	b, err := io.ReadAll(io.LimitReader(f, in.MaxBytes))
	if err != nil {
		return kernel.ToolResult{}, err
	}
	return kernel.ToolOutput(map[string]any{
		"path": in.Path, "content": string(b), "size_bytes": fi.Size(),
		"truncated": fi.Size() > int64(len(b)),
	})
}

// capWriter keeps the first n bytes.
type capWriter struct {
	buf       bytes.Buffer
	n         int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.n - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
			w.truncated = true
		} else {
			w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

func shellExec(ctx context.Context, root RootFunc, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		Command        string `json:"command"`
		Workdir        string `json:"workdir"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, err
	}
	if strings.TrimSpace(in.Command) == "" {
		return kernel.ToolResult{}, errors.New("command required")
	}
	base, err := root(c)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	dir, err := Confine(base, in.Workdir)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	timeout := ShellDefaultLimit
	if in.TimeoutSeconds > 0 {
		timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, ShellMaxLimit)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", in.Command)
	cmd.Dir = dir
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	stdout, stderr := &capWriter{n: ShellOutputCap}, &capWriter{n: ShellOutputCap}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	start := time.Now()
	runErr := cmd.Run()
	exit := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exit = ee.ExitCode()
		} else if ctx.Err() == nil {
			return kernel.ToolResult{}, runErr
		} else {
			exit = -1
		}
	}
	return kernel.ToolOutput(map[string]any{
		"exit_code":   exit,
		"stdout":      stdout.buf.String(),
		"stderr":      stderr.buf.String(),
		"truncated":   stdout.truncated || stderr.truncated,
		"timed_out":   errors.Is(ctx.Err(), context.DeadlineExceeded),
		"duration_ms": time.Since(start).Milliseconds(),
	})
}
