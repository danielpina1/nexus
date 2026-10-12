package workflow

import (
	"reflect"
	"testing"
)

func TestRender(t *testing.T) {
	vars := Vars{
		"inputs": map[string]any{"list": "", "guidelines": "organic", "n": 3, "flag": true},
		"steps": map[string]any{
			"usual": map[string]any{"output": "milk, eggs", "result": map[string]any{"total": 130.5}, "state": "succeeded"},
			"cart":  map[string]any{"output": map[string]any{"total": 99}, "result": map[string]any{}, "state": "succeeded"},
		},
		"trigger": map[string]any{"source": "cron", "key": "k", "payload": map[string]any{}, "meta": map[string]any{}},
	}
	cases := []struct {
		tmpl string
		want any
	}{
		{"plain text", "plain text"},
		{"List: {{ inputs.list != '' ? inputs.list : steps.usual.output }}", "List: milk, eggs"},
		{"{{ inputs.n }}", int64(3)},
		{"n={{ inputs.n }} flag={{ inputs.flag }}", "n=3 flag=true"},
		{"{{ steps.usual.result }}", map[string]any{"total": 130.5}},
		{"r={{ steps.usual.result }}", `r={"total":130.5}`},
		{"{{ steps.cart.output.total > 120 }}", false},
		{"{{ steps.usual.result.total > 120 }}", true},
		{"{{ [1, 2] }}", []any{float64(1), float64(2)}},
		{"{{ trigger.?payload.?missing.orValue('none') }}", "none"},
		{"{{ 'a,b'.split(',') }}", []any{"a", "b"}},
		{"x {{ toJson({'a': 1}) }}", `x {"a":1}`},
		{"Guidelines: {{ inputs.guidelines }}\nDone", "Guidelines: organic\nDone"},
	}
	for _, c := range cases {
		got, err := Render(c.tmpl, vars)
		if err != nil {
			t.Errorf("%q: %v", c.tmpl, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q = %#v, want %#v", c.tmpl, got, c.want)
		}
	}

	if _, err := Render("{{ steps.nope.output }}", vars); err == nil {
		t.Error("missing step: want error")
	}
	if _, err := EvalBool("inputs.n", vars); err == nil {
		t.Error("non-bool condition: want error")
	}
	ok, err := EvalBool("inputs.list == ''", vars)
	if err != nil || !ok {
		t.Errorf("EvalBool = %v, %v", ok, err)
	}
	v, err := RenderValue(map[string]any{"a": "{{ inputs.n }}", "b": []any{"{{ inputs.guidelines }}", 1}}, vars)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": int64(3), "b": []any{"organic", 1}}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("RenderValue = %#v", v)
	}
	if s, _ := RenderString("{{ inputs.missing_ok }}", Vars{"inputs": map[string]any{"missing_ok": nil}}); s != "" {
		t.Errorf("null renders as %q", s)
	}
}

func TestStripBraces(t *testing.T) {
	if got := stripBraces("{{ a > 1 }}"); got != "a > 1" {
		t.Error(got)
	}
	if got := stripBraces("a > 1"); got != "a > 1" {
		t.Error(got)
	}
}
