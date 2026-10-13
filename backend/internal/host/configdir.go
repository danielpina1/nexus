package host

import (
	"os"
	"path/filepath"
	"strings"
)

// EnterConfigDir makes relative paths in a config file mean "relative to the
// file": it finds a -config/--config argument in args, changes the working
// directory to that file's directory and rewrites the argument to the base
// name. Commands call it once from main so `nexus-node runtimes -config
// ~/Nexus/node/node.yaml` works from any directory. Args without -config are
// returned unchanged (the default config is looked up in the current dir).
func EnterConfigDir(args []string) ([]string, error) {
	out := append([]string(nil), args...)
	for i := 0; i < len(out); i++ {
		a := out[i]
		var val string
		var set func(string)
		switch {
		case a == "-config" || a == "--config":
			if i+1 >= len(out) {
				return out, nil
			}
			idx := i + 1
			val, set = out[idx], func(v string) { out[idx] = v }
		case strings.HasPrefix(a, "-config=") || strings.HasPrefix(a, "--config="):
			idx := i
			prefix := a[:strings.Index(a, "=")+1]
			val, set = a[len(prefix):], func(v string) { out[idx] = prefix + v }
		default:
			continue
		}
		dir := filepath.Dir(val)
		if dir == "." || dir == "" {
			return out, nil
		}
		if err := os.Chdir(dir); err != nil {
			return out, err
		}
		set(filepath.Base(val))
		return out, nil
	}
	return out, nil
}
