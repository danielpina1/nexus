package note

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnsureID returns src with `id: <id>` in its frontmatter when the note has
// no id yet. The rest of the file is preserved byte for byte. existing is the
// id already present (src returned unchanged).
func EnsureID(src []byte, newID func() string) (out []byte, id string, changed bool) {
	n := Parse("", src)
	if n.ID != "" {
		return src, n.ID, false
	}
	id = newID()
	s := string(bytes.TrimPrefix(src, []byte("\uFEFF")))
	nl := "\n"
	if strings.Contains(s, "\r\n") {
		nl = "\r\n"
	}
	if n.HasFrontmatter && n.FrontmatterErr == nil {
		// Insert right after the opening delimiter.
		i := strings.Index(s, "\n")
		return []byte(s[:i+1] + "id: " + id + nl + s[i+1:]), id, true
	}
	if n.HasFrontmatter {
		// Invalid YAML: do not touch it (we would make it worse).
		return src, "", false
	}
	return []byte("---" + nl + "id: " + id + nl + "---" + nl + s), id, true
}

// SetFields sets (or with a nil value, deletes) top-level frontmatter keys,
// keeping the order, comments and formatting of the other keys as far as
// yaml.v3 allows. New keys are appended. A note without frontmatter gets one.
func SetFields(src []byte, fields map[string]any, order ...string) ([]byte, error) {
	fm, body, _, ok := SplitFrontmatter(src)
	var doc yaml.Node
	if ok && strings.TrimSpace(fm) != "" {
		if err := yaml.Unmarshal([]byte(fm), &doc); err != nil {
			return nil, fmt.Errorf("frontmatter: %w", err)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("frontmatter is not a mapping")
	}
	keys := order
	if len(keys) == 0 {
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	for _, k := range keys {
		v, present := fields[k]
		if !present {
			continue
		}
		idx := -1
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == k {
				idx = i
				break
			}
		}
		if v == nil {
			if idx >= 0 {
				m.Content = append(m.Content[:idx], m.Content[idx+2:]...)
			}
			continue
		}
		var vn yaml.Node
		if err := vn.Encode(v); err != nil {
			return nil, err
		}
		if idx >= 0 {
			m.Content[idx+1] = &vn
		} else {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, &vn)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	_ = enc.Close()
	out := "---\n" + buf.String() + "---\n" + body
	return []byte(out), nil
}

// Render builds a note from ordered frontmatter key/values and a body.
func Render(keys []string, fields map[string]any, body string) ([]byte, error) {
	b, err := SetFields(nil, fields, keys...)
	if err != nil {
		return nil, err
	}
	return append(b, []byte(body)...), nil
}
