package claudecode

import (
	"encoding/json"
	"fmt"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// PermissionClass classifies a Claude Code permission prompt (tool name +
// input) for the approval gate and returns a one-line summary. MCP tools of
// the Nexus server are classified by the bridge itself; other MCP servers'
// tools and unknown tools are UNSPECIFIED (fail safe: approve).
func PermissionClass(tool string, input json.RawMessage) (nexusv1.ActionClass, string) {
	var in struct {
		Command      string `json:"command"`
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		Pattern      string `json:"pattern"`
		URL          string `json:"url"`
		Query        string `json:"query"`
	}
	_ = json.Unmarshal(input, &in)
	switch tool {
	case "Bash":
		return BashClass(in.Command), in.Command
	case "Write", "Edit", "MultiEdit":
		return nexusv1.ActionClass_ACTION_CLASS_COMPUTE, tool + " " + in.FilePath
	case "NotebookEdit":
		return nexusv1.ActionClass_ACTION_CLASS_COMPUTE, tool + " " + nonEmpty(in.NotebookPath, in.FilePath)
	case "WebFetch":
		return nexusv1.ActionClass_ACTION_CLASS_READ, "fetch " + in.URL
	case "WebSearch":
		return nexusv1.ActionClass_ACTION_CLASS_READ, "search " + in.Query
	case "Read", "Grep", "Glob", "LS":
		return nexusv1.ActionClass_ACTION_CLASS_READ, strings.TrimSpace(tool + " " + firstNonEmpty(in.FilePath, in.Path, in.Pattern))
	}
	if strings.HasPrefix(tool, "mcp__") {
		return nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED, ""
	}
	return ActionClass(tool), ""
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// BashClass classifies a shell command: recursive forced deletes and forced
// / deleting pushes are DESTRUCTIVE, other pushes CODE_PUSH, everything else
// COMPUTE. It is a heuristic over the command text (segments split on
// ; & | newlines, plus substring fallbacks for nested shells); it only ever
// raises the class above compute.
func BashClass(cmd string) nexusv1.ActionClass {
	class := nexusv1.ActionClass_ACTION_CLASS_COMPUTE
	raise := func(c nexusv1.ActionClass) {
		if c > class {
			class = c
		}
	}
	segments := strings.FieldsFunc(cmd, func(r rune) bool {
		return r == ';' || r == '&' || r == '|' || r == '\n' || r == '(' || r == ')' || r == '`'
	})
	for _, seg := range segments {
		f := strings.Fields(seg)
		for len(f) > 0 && (f[0] == "sudo" || f[0] == "env" || f[0] == "command" || f[0] == "exec" || strings.Contains(f[0], "=")) {
			f = f[1:]
		}
		if len(f) == 0 {
			continue
		}
		switch base(f[0]) {
		case "rm":
			if rmRecursiveForce(f[1:]) {
				raise(nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE)
			}
		case "git":
			if c, ok := gitPush(f[1:]); ok {
				raise(c)
			}
		}
	}
	// Fallbacks for commands hidden in quotes (bash -c "…", ssh host '…').
	switch {
	case strings.Contains(cmd, "rm -rf") || strings.Contains(cmd, "rm -fr"),
		strings.Contains(cmd, "git push --force") || strings.Contains(cmd, "git push -f"):
		raise(nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE)
	case strings.Contains(cmd, "git push"):
		raise(nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH)
	}
	return class
}

func base(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func rmRecursiveForce(args []string) bool {
	rec, force := false, false
	for _, a := range args {
		switch {
		case a == "--":
			return rec && force
		case a == "--recursive":
			rec = true
		case a == "--force":
			force = true
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--"):
			if strings.ContainsAny(a, "rR") {
				rec = true
			}
			if strings.Contains(a, "f") {
				force = true
			}
		}
	}
	return rec && force
}

// gitPush reports whether git args are a push and its class.
func gitPush(args []string) (nexusv1.ActionClass, bool) {
	i := 0
	// Skip global options (git -C dir, -c k=v, --git-dir=…).
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "-C" || args[i] == "-c" {
			i++
		}
		i++
	}
	if i >= len(args) || args[i] != "push" {
		return 0, false
	}
	for _, a := range args[i+1:] {
		switch {
		case a == "--force", a == "-f", strings.HasPrefix(a, "--force-with-lease"), a == "--mirror",
			a == "--delete", a == "-d", a == "--prune",
			strings.HasPrefix(a, "+"), strings.HasPrefix(a, ":"),
			strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "fd"):
			return nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE, true
		}
	}
	return nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH, true
}

// DescribePermission is the human action of a permission prompt for the
// approval title ("<Who> wants to " + it): "run: git push origin main",
// "edit src/app.ts (+12 −3)", "write notes.md (40 lines)", "fetch https://…",
// "search the web for “…”", "use github · create_issue". "" = generic title.
func DescribePermission(tool string, input json.RawMessage) string {
	var in struct {
		Command   string `json:"command"`
		FilePath  string `json:"file_path"`
		Path      string `json:"path"`
		Pattern   string `json:"pattern"`
		URL       string `json:"url"`
		Query     string `json:"query"`
		Content   string `json:"content"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
		Edits     []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	_ = json.Unmarshal(input, &in)
	switch tool {
	case "Bash":
		if c := oneLine(in.Command); c != "" {
			return "run: " + truncate(c, 100)
		}
	case "Edit", "MultiEdit":
		add, del := lineCount(in.NewString), lineCount(in.OldString)
		for _, e := range in.Edits {
			add += lineCount(e.NewString)
			del += lineCount(e.OldString)
		}
		return fmt.Sprintf("edit %s (+%d −%d)", in.FilePath, add, del)
	case "Write":
		return fmt.Sprintf("write %s (%d lines)", in.FilePath, lineCount(in.Content))
	case "NotebookEdit":
		return "edit notebook " + firstNonEmpty(in.FilePath, in.Path)
	case "WebFetch":
		return "fetch " + in.URL
	case "WebSearch":
		return "search the web for “" + in.Query + "”"
	case "Read", "Grep", "Glob", "LS":
		if t := firstNonEmpty(in.FilePath, in.Path, in.Pattern); t != "" {
			return "read " + t
		}
	}
	if strings.HasPrefix(tool, "mcp__") {
		parts := strings.SplitN(strings.TrimPrefix(tool, "mcp__"), "__", 2)
		if len(parts) == 2 {
			return "use " + parts[0] + " · " + parts[1]
		}
	}
	return ""
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
