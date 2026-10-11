// Package agentdef parses agent notes (vault agents/*.md) into *nexusv1.Agent.
//
// Parsing never fails: problems are reported in Agent.Errors and such agents
// are returned with Enabled=false so the catalog can still list them.
package agentdef

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// KnownRuntimes are the runtimes documented in docs/AGENTS.md. Other names
// are accepted (runtimes are plug-ins) as long as they are well-formed.
var KnownRuntimes = []string{"claude-code", "codex", "openai", "ollama", "anthrex", "shell", "browser", "workflow"}

var reID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Parse parses an agent note. p is the vault-relative path. The returned
// instructions are the note body (without frontmatter), trimmed.
func Parse(p string, src []byte) (*nexusv1.Agent, string) {
	n := note.Parse(p, src)
	a := &nexusv1.Agent{Enabled: true, Policy: map[string]nexusv1.TrustLevel{}}
	var errs []string
	addErr := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	if n.FrontmatterErr != nil {
		addErr("%v", n.FrontmatterErr)
	}
	if !n.HasFrontmatter {
		addErr("missing frontmatter")
	}
	fm := n.Frontmatter

	if n.Type != "" && n.Type != "agent" {
		addErr("type is %q, want \"agent\"", n.Type)
	}
	a.Id = strings.TrimSpace(n.ID)
	if a.Id == "" {
		a.Id = strings.TrimSuffix(path.Base(strings.ReplaceAll(p, "\\", "/")), ".md")
		if a.Id == "" || a.Id == "." {
			addErr("missing id")
		}
	}
	if a.Id != "" && !reID.MatchString(a.Id) {
		addErr("invalid id %q (use lower-case letters, digits, '-', '_' or '.')", a.Id)
	}
	a.Name = str(fm["name"])
	if a.Name == "" {
		a.Name = str(fm["title"])
	}
	if a.Name == "" {
		a.Name = a.Id
	}
	a.Description = strings.Join(strings.Fields(str(fm["description"])), " ")
	if a.Description == "" {
		addErr("missing description (the router needs it)")
	}
	a.Icon = str(fm["icon"])
	a.Model = str(fm["model"])

	a.Runtime = str(fm["runtime"])
	switch {
	case a.Runtime == "":
		addErr("missing runtime")
	case !reID.MatchString(a.Runtime):
		addErr("invalid runtime %q", a.Runtime)
	}

	if v, ok := fm["tier"]; ok && v != nil {
		t, err := ParseTier(str(v))
		if err != nil {
			addErr("%v", err)
		}
		a.Tier = t
	}

	tags, err := nodeTags(fm["node"])
	if err != nil {
		addErr("%v", err)
	}
	a.NodeTags = tags

	tools, err := stringSlice(fm["tools"], "tools")
	if err != nil {
		addErr("%v", err)
	}
	a.Tools = tools

	if v, ok := fm["limits"]; ok && v != nil {
		lim, lerrs := parseLimits(v)
		a.Limits = lim
		errs = append(errs, lerrs...)
	}

	if v, ok := fm["policy"]; ok && v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			addErr("policy must be a map of action class → trust level")
		} else {
			keys := sortedKeys(m)
			for _, k := range keys {
				lvl, err := ParseTrustLevel(str(m[k]))
				if err != nil {
					addErr("policy.%s: %v", k, err)
					continue
				}
				a.Policy[strings.ToLower(strings.TrimSpace(k))] = lvl
			}
		}
	}

	if v, ok := fm["version"]; ok && v != nil {
		i, err := toInt(v)
		if err != nil || i < 0 || i > math.MaxInt32 {
			addErr("version must be a non-negative integer")
		} else {
			a.Version = int32(i)
		}
	}
	if v, ok := fm["enabled"]; ok && v != nil {
		b, isBool := v.(bool)
		if !isBool {
			addErr("enabled must be true or false")
		} else {
			a.Enabled = b
		}
	}

	a.Note = &nexusv1.NoteRef{Id: a.Id, Path: p, Title: a.Name}
	if len(errs) > 0 {
		a.Errors = errs
		a.Enabled = false
	}
	return a, strings.TrimSpace(n.Body)
}

// ParseTier maps simple|medium|heavy to the enum.
func ParseTier(s string) (nexusv1.Tier, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return nexusv1.Tier_TIER_UNSPECIFIED, nil
	case "simple":
		return nexusv1.Tier_TIER_SIMPLE, nil
	case "medium":
		return nexusv1.Tier_TIER_MEDIUM, nil
	case "heavy":
		return nexusv1.Tier_TIER_HEAVY, nil
	}
	return nexusv1.Tier_TIER_UNSPECIFIED, fmt.Errorf("invalid tier %q (simple | medium | heavy)", s)
}

// ParseTrustLevel maps auto|notify|approve|biometric to the enum.
func ParseTrustLevel(s string) (nexusv1.TrustLevel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto":
		return nexusv1.TrustLevel_TRUST_LEVEL_AUTO, nil
	case "notify":
		return nexusv1.TrustLevel_TRUST_LEVEL_NOTIFY, nil
	case "approve":
		return nexusv1.TrustLevel_TRUST_LEVEL_APPROVE, nil
	case "biometric":
		return nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC, nil
	}
	return nexusv1.TrustLevel_TRUST_LEVEL_UNSPECIFIED, fmt.Errorf("invalid trust level %q (auto | notify | approve | biometric)", s)
}

// ParseDuration accepts Go durations ("30m", "1h30m"), days ("2d") and bare
// numbers, which are seconds.
func ParseDuration(v any) (time.Duration, error) {
	switch t := v.(type) {
	case int, int64, uint64, float64:
		f, _ := toFloat(t)
		if f < 0 {
			return 0, fmt.Errorf("negative duration")
		}
		return time.Duration(f * float64(time.Second)), nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return ParseDuration(f)
		}
		if strings.HasSuffix(s, "d") {
			days, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			return time.Duration(days * 24 * float64(time.Hour)), nil
		}
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return 0, fmt.Errorf("invalid duration %q (e.g. 30m, 1h30m)", s)
		}
		return d, nil
	}
	return 0, fmt.Errorf("invalid duration %v", v)
}

// ParseEUR parses an amount in euros (0.5, "0.50", "€1,20", "2 EUR") into
// minor units.
func ParseEUR(v any) (*nexusv1.Money, error) {
	var f float64
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		s = strings.TrimPrefix(s, "€")
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(s, "EUR"), "eur"))
		s = strings.ReplaceAll(s, ",", ".")
		var err error
		if f, err = strconv.ParseFloat(s, 64); err != nil {
			return nil, fmt.Errorf("invalid amount %q", t)
		}
	default:
		var ok bool
		if f, ok = toFloat(v); !ok {
			return nil, fmt.Errorf("invalid amount %v", v)
		}
	}
	if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("invalid amount %v", v)
	}
	return &nexusv1.Money{Currency: "EUR", MinorUnits: int64(math.Round(f * 100))}, nil
}

func parseLimits(v any) (*nexusv1.AgentLimits, []string) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, []string{"limits must be a map"}
	}
	lim := &nexusv1.AgentLimits{}
	var errs []string
	for _, k := range sortedKeys(m) {
		val := m[k]
		switch k {
		case "max_runtime":
			d, err := ParseDuration(val)
			if err != nil {
				errs = append(errs, "limits.max_runtime: "+err.Error())
			}
			lim.MaxRuntimeSeconds = int64(d / time.Second)
		case "max_cost_eur", "max_cost":
			mo, err := ParseEUR(val)
			if err != nil {
				errs = append(errs, "limits."+k+": "+err.Error())
			}
			lim.MaxCost = mo
		case "max_spend_eur", "max_spend":
			mo, err := ParseEUR(val)
			if err != nil {
				errs = append(errs, "limits."+k+": "+err.Error())
			}
			lim.MaxSpend = mo
		case "max_subtasks":
			i, err := toInt(val)
			if err != nil || i < 0 || i > math.MaxInt32 {
				errs = append(errs, "limits.max_subtasks must be a non-negative integer")
			}
			lim.MaxSubtasks = int32(i)
		default:
			// Unknown limits (max_tokens…) are tolerated for forward compatibility.
		}
	}
	return lim, errs
}

func nodeTags(v any) ([]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []string{s}, nil
		}
		return nil, nil
	case []any:
		return stringSlice(t, "node")
	case map[string]any:
		return stringSlice(t["tags"], "node.tags")
	}
	return nil, fmt.Errorf("node must be a map with tags, a tag list or a tag")
}

func stringSlice(v any, field string) ([]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		// Accept a single value or a comma-separated list.
		var out []string
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			switch s := e.(type) {
			case string:
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			default:
				return out, fmt.Errorf("%s must be a list of strings", field)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s must be a list of strings", field)
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case map[string]any, []any:
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint64:
		return float64(t), true
	case float64:
		return t, true
	}
	return 0, false
}

func toInt(v any) (int64, error) {
	switch t := v.(type) {
	case int:
		return int64(t), nil
	case int64:
		return t, nil
	case uint64:
		return int64(t), nil
	case float64:
		if t == math.Trunc(t) {
			return int64(t), nil
		}
	case string:
		return strconv.ParseInt(strings.TrimSpace(t), 10, 64)
	}
	return 0, fmt.Errorf("not an integer: %v", v)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
