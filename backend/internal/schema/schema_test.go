package schema

import (
	"strings"
	"testing"
)

func TestCompileAndValidate(t *testing.T) {
	s, err := Compile(map[string]any{
		"type":     "object",
		"required": []any{"approved", "feedback"},
		"properties": map[string]any{
			"approved": map[string]any{"type": "boolean"},
			"feedback": map[string]any{"type": "string"},
			"score":    map[string]any{"type": "integer", "minimum": 0, "maximum": 10},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(map[string]any{"approved": true, "feedback": "ok", "score": 7}); err != nil {
		t.Fatalf("valid value rejected: %v", err)
	}
	err = s.Validate(map[string]any{"approved": "yes", "score": 11})
	if err == nil {
		t.Fatal("invalid value accepted")
	}
	for _, want := range []string{"/approved", "feedback"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestCompileRefusesExternalRefs(t *testing.T) {
	_, err := Compile(map[string]any{"$ref": "file:///etc/passwd"})
	if err == nil {
		t.Fatal("external $ref compiled")
	}
	if _, err := Compile(map[string]any{"type": 7}); err == nil {
		t.Fatal("invalid schema compiled")
	}
}

func TestNormalizeYAMLValues(t *testing.T) {
	v, err := Normalize(map[any]any{"n": 3, "list": []any{map[any]any{"a": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["n"].(float64) != 3 {
		t.Fatalf("n = %v", m["n"])
	}
}

func TestParseAnswer(t *testing.T) {
	cases := map[string]bool{
		`{"a":1}`:                               true,
		"Here you go:\n```json\n{\"a\":1}\n```": true,
		`Result: {"a": {"b": 2}} done`:          true,
		`[1,2]`:                                 true,
		`no json here`:                          false,
		``:                                      false,
	}
	for in, ok := range cases {
		v, err := ParseAnswer(in)
		if (err == nil) != ok {
			t.Errorf("ParseAnswer(%q) = %v, %v", in, v, err)
		}
	}
}
