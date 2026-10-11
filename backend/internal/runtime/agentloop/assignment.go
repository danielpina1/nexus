package agentloop

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

const maxInlineAttachment = 32 << 10

// BuildPrompt renders the user prompt: the task prompt, the assignment
// context (memory snippets, params) as JSON, and attachments (small text
// attachments inline, others by name).
func BuildPrompt(a *nexusv1.RunAssignment) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(a.GetPrompt()))
	if c := a.GetContext(); c != nil && len(c.GetFields()) > 0 {
		js, err := json.MarshalIndent(c.AsMap(), "", "  ")
		if err == nil {
			b.WriteString("\n\n## Context\n```json\n")
			b.Write(js)
			b.WriteString("\n```")
		}
	}
	if atts := a.GetAttachments(); len(atts) > 0 {
		b.WriteString("\n\n## Attachments\n")
		for _, at := range atts {
			name := at.GetName()
			if name == "" {
				name = at.GetId()
			}
			fmt.Fprintf(&b, "- %s (%s)", name, at.GetMimeType())
			if at.GetUrl() != "" {
				fmt.Fprintf(&b, " %s", at.GetUrl())
			}
			b.WriteByte('\n')
			if isText(at) {
				fmt.Fprintf(&b, "<attachment name=%q>\n%s\n</attachment>\n", name, string(at.GetData()))
			}
		}
	}
	return b.String()
}

func isText(at *nexusv1.Attachment) bool {
	d := at.GetData()
	if len(d) == 0 || len(d) > maxInlineAttachment || !utf8.Valid(d) {
		return false
	}
	mt := at.GetMimeType()
	return strings.HasPrefix(mt, "text/") || mt == "application/json" || mt == "application/x-yaml" || mt == "application/yaml"
}
