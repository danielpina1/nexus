// Package schema validates structured agent inputs and outputs against JSON
// Schema (drafts 4–2020-12, via github.com/santhosh-tekuri/jsonschema/v6).
//
// Schemas come from vault notes (agent `inputs:` / `output:`, team
// `output:`), so they are compiled with every external reference refused:
// a schema can never make the hub read a file or fetch a URL.
package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Schema is a compiled JSON Schema.
type Schema struct {
	s   *jsonschema.Schema
	doc map[string]any
}

type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema references are not allowed (%s)", url)
}

// Compile compiles a schema document (a JSON object, e.g. decoded from YAML
// with Normalize).
func Compile(doc map[string]any) (*Schema, error) {
	if doc == nil {
		return nil, errors.New("schema: empty schema")
	}
	norm, err := Normalize(doc)
	if err != nil {
		return nil, err
	}
	m, ok := norm.(map[string]any)
	if !ok {
		return nil, errors.New("schema: a schema must be an object")
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(noLoader{})
	const loc = "nexus:///schema.json"
	if err := c.AddResource(loc, m); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	s, err := c.Compile(loc)
	if err != nil {
		return nil, fmt.Errorf("schema: %s", oneLine(err.Error()))
	}
	return &Schema{s: s, doc: m}, nil
}

// Doc returns the normalised schema document.
func (s *Schema) Doc() map[string]any { return s.doc }

// Validate checks a JSON value (as decoded by encoding/json or Normalize).
// The error lists the violations in a compact, model-readable form.
func (s *Schema) Validate(v any) error {
	nv, err := Normalize(v)
	if err != nil {
		return err
	}
	if err := s.s.Validate(nv); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return errors.New(describe(ve))
		}
		return err
	}
	return nil
}

// describe flattens a validation error into "at /path: message; …".
func describe(ve *jsonschema.ValidationError) string {
	out := ve.BasicOutput()
	var parts []string
	for _, u := range out.Errors {
		if u.Error == nil {
			continue
		}
		msg := u.Error.String()
		if msg == "" || strings.HasPrefix(msg, "validation failed") {
			continue
		}
		loc := u.InstanceLocation
		if loc == "" {
			loc = "/"
		}
		parts = append(parts, "at "+loc+": "+msg)
	}
	if len(parts) == 0 {
		return oneLine(ve.Error())
	}
	if len(parts) > 8 {
		parts = append(parts[:8], fmt.Sprintf("(%d more)", len(parts)-8))
	}
	return strings.Join(parts, "; ")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Normalize converts YAML/JSON-decoded values (map[any]any, ints…) into
// plain JSON values (map[string]any, []any, float64, string, bool, nil).
func Normalize(v any) (any, error) {
	b, err := json.Marshal(jsonSafe(v))
	if err != nil {
		return nil, fmt.Errorf("schema: not JSON-compatible: %w", err)
	}
	var out any
	d := json.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

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
	}
	return v
}

// ErrNoJSON is returned by ParseAnswer when the text holds no JSON value.
var ErrNoJSON = errors.New("the answer contains no JSON value")

// ParseAnswer extracts the JSON value from a model's final answer: the
// whole text, a ```json fenced block, or the outermost {...} / [...].
func ParseAnswer(text string) (any, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return nil, ErrNoJSON
	}
	try := func(c string) (any, bool) {
		var v any
		if err := json.Unmarshal([]byte(strings.TrimSpace(c)), &v); err != nil {
			return nil, false
		}
		return v, true
	}
	if v, ok := try(s); ok {
		return v, nil
	}
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			if v, ok := try(rest[:j]); ok {
				return v, nil
			}
		}
	}
	for _, p := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		i, j := strings.IndexByte(s, p[0]), strings.LastIndexByte(s, p[1])
		if i >= 0 && j > i {
			if v, ok := try(s[i : j+1]); ok {
				return v, nil
			}
		}
	}
	return nil, ErrNoJSON
}
