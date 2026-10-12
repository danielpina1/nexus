package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/ext"
	"google.golang.org/protobuf/types/known/structpb"
)

// Expressions are CEL (https://cel.dev). Variables available everywhere:
//
//	inputs   map  workflow inputs (declared inputs default to "" / their default)
//	steps    map  id → {output, result, state, error}; every declared step
//	              id is present (state "pending" before it runs)
//	trigger  map  {source, key, payload, meta} of the firing (empty for manual runs)
//	run      map  {id, workflow_id, dry_run, source}
//	item, index   current foreach element and position (plus the `as:` name)
//	event    map  the bus event (event triggers' `where`, wait.event `where`)
//	now      timestamp
//
// Extra functions: the cel-go strings, lists, math and encoders extensions,
// optional types (`trigger.?payload.?x.orValue('')`) and toJson(dyn) → string.
var baseVars = []string{"inputs", "steps", "trigger", "run", "item", "index", "event"}

var (
	envMu    sync.Mutex
	envCache = map[string]*cel.Env{}
	prgCache = map[string]cel.Program{}
)

func envFor(extra []string) (*cel.Env, string, error) {
	names := map[string]bool{}
	for _, n := range baseVars {
		names[n] = true
	}
	var add []string
	for _, n := range extra {
		if !names[n] && n != "now" {
			names[n] = true
			add = append(add, n)
		}
	}
	sort.Strings(add)
	key := strings.Join(add, ",")
	if e, ok := envCache[key]; ok {
		return e, key, nil
	}
	opts := []cel.EnvOption{
		cel.OptionalTypes(),
		cel.CrossTypeNumericComparisons(true),
		ext.Strings(), ext.Lists(), ext.Math(), ext.Encoders(),
		cel.Variable("now", cel.TimestampType),
		cel.Function("toJson", cel.Overload("toJson_dyn", []*cel.Type{cel.DynType}, cel.StringType,
			cel.UnaryBinding(func(v ref.Val) ref.Val {
				b, err := json.Marshal(toNative(v))
				if err != nil {
					return types.NewErr("toJson: %v", err)
				}
				return types.String(b)
			}))),
	}
	for n := range names {
		opts = append(opts, cel.Variable(n, cel.DynType))
	}
	e, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, "", err
	}
	envCache[key] = e
	return e, key, nil
}

func program(expr string, extra []string) (cel.Program, error) {
	envMu.Lock()
	defer envMu.Unlock()
	env, key, err := envFor(extra)
	if err != nil {
		return nil, err
	}
	ck := key + "\x00" + expr
	if p, ok := prgCache[ck]; ok {
		return p, nil
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	p, err := env.Program(ast)
	if err != nil {
		return nil, err
	}
	if len(prgCache) > 4096 {
		prgCache = map[string]cel.Program{}
	}
	prgCache[ck] = p
	return p, nil
}

func checkExpr(expr string, scope []string) error {
	if strings.TrimSpace(expr) == "" {
		return errors.New("empty expression")
	}
	_, err := program(expr, scope)
	return err
}

// Vars are expression variables.
type Vars map[string]any

func (v Vars) extra() []string {
	var out []string
	for k := range v {
		out = append(out, k)
	}
	return out
}

func (v Vars) activation() map[string]any {
	act := make(map[string]any, len(v)+len(baseVars)+1)
	for _, n := range baseVars {
		act[n] = nil
	}
	for k, x := range v {
		act[k] = x
	}
	if _, ok := act["now"].(time.Time); !ok {
		act["now"] = time.Now()
	}
	return act
}

// Eval evaluates a CEL expression and returns a JSON-like Go value
// (string, float64, bool, nil, []any, map[string]any).
func Eval(expr string, vars Vars) (any, error) {
	p, err := program(expr, vars.extra())
	if err != nil {
		return nil, err
	}
	out, _, err := p.Eval(vars.activation())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", expr, err)
	}
	return toNative(out), nil
}

// EvalBool evaluates a condition.
func EvalBool(expr string, vars Vars) (bool, error) {
	v, err := Eval(expr, vars)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s: condition is %T, want bool", expr, v)
	}
	return b, nil
}

var structValueType = reflect.TypeOf(&structpb.Value{})

func toNative(v ref.Val) any {
	switch t := v.(type) {
	case types.String:
		return string(t)
	case types.Bool:
		return bool(t)
	case types.Int:
		return int64(t)
	case types.Uint:
		return uint64(t)
	case types.Double:
		return float64(t)
	case types.Null:
		return nil
	case types.Timestamp:
		return t.Time.UTC().Format(time.RFC3339)
	case types.Duration:
		return t.Duration.String()
	}
	if pv, err := v.ConvertToNative(structValueType); err == nil {
		if sv, ok := pv.(*structpb.Value); ok {
			return sv.AsInterface()
		}
	}
	return v.Value()
}

// ---------------------------------------------------------------- templates

var reTemplate = regexp.MustCompile(`(?s)\{\{(.+?)\}\}`)

func checkTemplate(s string, scope []string) error {
	for _, m := range reTemplate.FindAllStringSubmatch(s, -1) {
		if err := checkExpr(strings.TrimSpace(m[1]), scope); err != nil {
			return err
		}
	}
	return nil
}

func checkTemplateValue(v any, scope []string) error {
	switch t := v.(type) {
	case string:
		return checkTemplate(t, scope)
	case map[string]any:
		for _, x := range t {
			if err := checkTemplateValue(x, scope); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range t {
			if err := checkTemplateValue(x, scope); err != nil {
				return err
			}
		}
	}
	return nil
}

// Render expands `{{ expr }}` placeholders. A string that is exactly one
// placeholder yields the expression's value with its type (so
// `with: { items: "{{ steps.list.result.items }}" }` passes a list); otherwise
// values are formatted as text (maps and lists as JSON, null as "").
func Render(tmpl string, vars Vars) (any, error) {
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}
	ms := reTemplate.FindAllStringSubmatchIndex(tmpl, -1)
	if len(ms) == 1 && strings.TrimSpace(tmpl[:ms[0][0]]) == "" && strings.TrimSpace(tmpl[ms[0][1]:]) == "" {
		return Eval(strings.TrimSpace(tmpl[ms[0][2]:ms[0][3]]), vars)
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		b.WriteString(tmpl[last:m[0]])
		v, err := Eval(strings.TrimSpace(tmpl[m[2]:m[3]]), vars)
		if err != nil {
			return nil, err
		}
		b.WriteString(Text(v))
		last = m[1]
	}
	b.WriteString(tmpl[last:])
	return b.String(), nil
}

// RenderString renders a template as text.
func RenderString(tmpl string, vars Vars) (string, error) {
	v, err := Render(tmpl, vars)
	if err != nil {
		return "", err
	}
	return Text(v), nil
}

// RenderValue renders every string inside maps and lists.
func RenderValue(v any, vars Vars) (any, error) {
	switch t := v.(type) {
	case string:
		return Render(t, vars)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			r, err := RenderValue(x, vars)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			r, err := RenderValue(x, vars)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

// Text formats a value for a prompt or message.
func Text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int, int32, int64, uint, uint32, uint64:
		return fmt.Sprint(t)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
