package tasks

import (
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// ForwardedAgentKeys are the agent-note frontmatter keys copied into
// RunAssignment.context (under the same names) for runtimes on the node:
// the Agent proto has no field for them. `browser` carries allowed_domains,
// profile and restrict_domains (internal/runtime/browser); `shops` and
// `shopping` carry the stores and shopping defaults (docs/SHOPPING.md).
var ForwardedAgentKeys = []string{"browser", "shops", "shopping"}

// mergeAgentConfig copies ForwardedAgentKeys from the agent note's
// frontmatter into ctx. Values from the agent note replace task params of
// the same name: they are the agent's configuration (e.g. its domain
// allow-list) and a task must not loosen them. A note without frontmatter
// or without these keys leaves ctx unchanged.
func mergeAgentConfig(ctx *structpb.Struct, markdown string) error {
	if ctx == nil || markdown == "" {
		return nil
	}
	n := note.Parse("", []byte(markdown))
	if n.FrontmatterErr != nil {
		return n.FrontmatterErr
	}
	var errs []error
	for _, k := range ForwardedAgentKeys {
		v, ok := n.Frontmatter[k]
		if !ok || v == nil {
			continue
		}
		pv, err := structpb.NewValue(jsonSafe(v))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
			continue
		}
		if ctx.Fields == nil {
			ctx.Fields = map[string]*structpb.Value{}
		}
		ctx.Fields[k] = pv
	}
	if len(errs) > 0 {
		return fmt.Errorf("%v", errs)
	}
	return nil
}

// jsonSafe converts YAML-decoded values (ints, map[string]any, []any,
// time values…) into types structpb accepts.
func jsonSafe(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = jsonSafe(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprint(k)] = jsonSafe(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = jsonSafe(e)
		}
		return out
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case uint64:
		return float64(t)
	case float32:
		return float64(t)
	case nil, bool, float64, string:
		return t
	default:
		return fmt.Sprint(t)
	}
}
