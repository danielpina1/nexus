package claudecode

import (
	"encoding/json"
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
