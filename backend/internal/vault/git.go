package vault

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Author is the git identity of every commit the vault makes.
const (
	gitName  = "Nexus"
	gitEmail = "nexus@localhost"
)

// gitRepo batches vault writes into commits. Commits never fail a write:
// errors are logged.
type gitRepo struct {
	root  string
	delay time.Duration
	log   *slog.Logger

	mu      sync.Mutex
	pending map[string]bool
	msgs    []string
	runs    []string
	timer   *time.Timer

	commitMu sync.Mutex // one git commit at a time
}

// detectGit returns a repo handle when git is installed and root is inside a
// work tree, else nil.
func detectGit(root string, delay time.Duration, log *slog.Logger) *gitRepo {
	if _, err := exec.LookPath("git"); err != nil {
		log.Info("vault: git not found; auto-commit disabled")
		return nil
	}
	g := &gitRepo{root: root, delay: delay, log: log, pending: map[string]bool{}}
	out, err := g.run(context.Background(), "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(out) != "true" {
		log.Info("vault: not a git repository; auto-commit disabled", "root", root)
		return nil
	}
	return g
}

func (g *gitRepo) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.root}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+gitName, "GIT_AUTHOR_EMAIL="+gitEmail,
		"GIT_COMMITTER_NAME="+gitName, "GIT_COMMITTER_EMAIL="+gitEmail,
		"GIT_TERMINAL_PROMPT=0",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// schedule records a changed path; the commit happens after delay (batched)
// or immediately when delay is 0.
func (g *gitRepo) schedule(paths []string, msg, runID string) {
	g.mu.Lock()
	for _, p := range paths {
		g.pending[p] = true
	}
	if msg != "" {
		g.msgs = append(g.msgs, msg)
	}
	if runID != "" && !contains(g.runs, runID) {
		g.runs = append(g.runs, runID)
	}
	if g.delay <= 0 {
		g.mu.Unlock()
		g.flush()
		return
	}
	if g.timer == nil {
		g.timer = time.AfterFunc(g.delay, g.flush)
	}
	g.mu.Unlock()
}

// isPending reports whether path has uncommitted vault writes queued.
func (g *gitRepo) isPending(p string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pending[p]
}

// flush commits everything pending.
func (g *gitRepo) flush() {
	g.commitMu.Lock()
	defer g.commitMu.Unlock()
	g.mu.Lock()
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	paths := make([]string, 0, len(g.pending))
	for p := range g.pending {
		paths = append(paths, p)
	}
	msgs, runs := g.msgs, g.runs
	g.pending, g.msgs, g.runs = map[string]bool{}, nil, nil
	g.mu.Unlock()
	if len(paths) == 0 {
		return
	}
	sort.Strings(paths)
	msg := commitMessage(paths, msgs, runs)
	ctx := context.Background()
	if _, err := g.run(ctx, append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		g.log.Warn("vault: git add failed", "err", err)
		return
	}
	// Only commit our paths, never whatever else the user has staged.
	args := []string{"commit", "-q", "--no-verify", "--no-gpg-sign", "--author", gitName + " <" + gitEmail + ">", "-m", msg, "--"}
	if _, err := g.run(ctx, append(args, paths...)...); err != nil {
		if strings.Contains(err.Error(), "nothing to commit") || strings.Contains(err.Error(), "no changes added") {
			return
		}
		g.log.Warn("vault: git commit failed", "err", err)
	}
}

func commitMessage(paths, msgs, runs []string) string {
	var b strings.Builder
	uniq := []string{}
	for _, m := range msgs {
		if m = strings.TrimSpace(m); m != "" && !contains(uniq, m) {
			uniq = append(uniq, m)
		}
	}
	switch {
	case len(uniq) == 1:
		b.WriteString(uniq[0])
	case len(uniq) == 0 && len(paths) == 1:
		b.WriteString("vault: update " + paths[0])
	default:
		fmt.Fprintf(&b, "vault: %d changes", len(paths))
		if len(uniq) > 0 {
			b.WriteString("\n")
			for _, m := range uniq {
				b.WriteString("\n- " + strings.ReplaceAll(m, "\n", " "))
			}
		}
	}
	if len(runs) > 0 {
		b.WriteString("\n")
		for _, r := range runs {
			b.WriteString("\nNexus-Run: " + r)
		}
	}
	return b.String()
}

// lastCommit returns the newest commit touching rel ("" when none).
func (g *gitRepo) lastCommit(ctx context.Context, rel string) string {
	out, err := g.run(ctx, "log", "-1", "--format=%H", "--", rel)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// clean reports whether rel has no uncommitted changes in the work tree.
func (g *gitRepo) clean(ctx context.Context, rel string) bool {
	if g.isPending(rel) {
		return false
	}
	out, err := g.run(ctx, "status", "--porcelain", "--", rel)
	return err == nil && strings.TrimSpace(out) == ""
}

// blobAt returns the blob id of rel at commit ("" if absent).
func (g *gitRepo) blobAt(ctx context.Context, commit, rel string) (string, error) {
	if _, err := g.run(ctx, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return "", fmt.Errorf("unknown commit %q", commit)
	}
	out, err := g.run(ctx, "rev-parse", "-q", "--verify", commit+":./"+rel)
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// hashFile returns the blob id the file would have ("" if missing).
func (g *gitRepo) hashFile(ctx context.Context, rel string) string {
	out, err := g.run(ctx, "hash-object", "--", rel)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
