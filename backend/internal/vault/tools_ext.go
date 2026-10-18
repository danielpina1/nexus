package vault

import (
	"context"
	"fmt"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// extTools are the agent tools for queries, history and refactoring.
//
// vault.restore and vault.rename are internal writes (WRITE_INTERNAL), but
// unlike vault.write they roll a note back or rewrite links across many
// notes, so they carry an explicit minimum level of APPROVE
// (kernel.ToolSpec.MinLevel): the user confirms each call with one tap
// (not biometric). Owner policy can tighten it, never loosen it.
func extTools(v *Vault) []kernel.Tool {
	read := nexusv1.ActionClass_ACTION_CLASS_READ
	write := nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL
	approve := nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
	return []kernel.Tool{
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name: "vault.query",
				Description: "Query the vault with filters, e.g. `type:person tag:work updated:>2026-09-01 links:[[ACME]] sort:-updated limit:20`. " +
					"Keys: type, tag, path, title, links:[[X]] (notes linking to X), from:[[X]] (notes X links to), created/updated (> < >= <=, YYYY-MM-DD, -7d), " +
					"has:field, orphan:true, any frontmatter field (status:active, rating:>3), sort:field / sort:-field, limit:N. Free words are full-text search; '-' negates.",
				InputSchema: schema(`{"type":"object","properties":{
					"query":{"type":"string"},
					"limit":{"type":"integer","minimum":1,"maximum":200}},"required":["query"]}`),
				ActionClass: read,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				res, err := v.Query(ctx, in.Query, in.Limit)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				type hit struct {
					ID      string         `json:"id"`
					Path    string         `json:"path"`
					Title   string         `json:"title"`
					Type    string         `json:"type,omitempty"`
					Tags    []string       `json:"tags,omitempty"`
					Updated string         `json:"updated"`
					Snippet string         `json:"snippet,omitempty"`
					Fields  map[string]any `json:"frontmatter,omitempty"`
				}
				out := struct {
					Total    int      `json:"total"`
					Hits     []hit    `json:"hits"`
					Warnings []string `json:"warnings,omitempty"`
				}{Total: res.Total, Hits: []hit{}, Warnings: res.Warnings}
				for _, h := range res.Hits {
					e := h.Entry
					x := hit{ID: e.Key(), Path: e.Path, Title: e.Title, Type: e.Type, Tags: e.Tags, Updated: e.Updated.Format("2006-01-02T15:04")}
					if v.AgentMayRead(e.Path) {
						x.Snippet, x.Fields = h.Snippet, e.Frontmatter
					}
					out.Hits = append(out.Hits, x)
				}
				return kernel.ToolOutput(out)
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "vault.history",
				Description: "List the versions of a vault note (newest first) with commit ids, messages, the agent run that made them and optional diffs. Use with vault.restore to undo a change.",
				InputSchema: schema(`{"type":"object","properties":{
					"path":{"type":"string"},"id":{"type":"string"},
					"limit":{"type":"integer","minimum":1,"maximum":50},
					"diff":{"type":"boolean","description":"Include unified diffs"}}}`),
				ActionClass: read,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct {
					Path, ID string
					Limit    int
					Diff     bool
				}
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				key, err := toolKey(in.ID, in.Path)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				rel, err := v.locate(ctx, key)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				if err := checkAgentReadable(v, rel); err != nil {
					return kernel.ToolResult{}, err
				}
				if in.Limit <= 0 {
					in.Limit = 10
				}
				vs, err := v.History(ctx, rel, in.Limit, in.Diff)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				type ver struct {
					Commit  string `json:"commit"`
					Path    string `json:"path"`
					At      string `json:"at"`
					Message string `json:"message"`
					RunID   string `json:"run_id,omitempty"`
					Deleted bool   `json:"deleted,omitempty"`
					Diff    string `json:"diff,omitempty"`
				}
				out := struct {
					Versions []ver `json:"versions"`
				}{Versions: []ver{}}
				for _, x := range vs {
					out.Versions = append(out.Versions, ver{x.Commit, x.Path, x.At.Format("2006-01-02T15:04:05Z07:00"), x.Message, x.RunID, x.Deleted, truncate(x.Diff, 4000)})
				}
				return kernel.ToolOutput(out)
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "vault.restore",
				Description: "Restore a vault note to an earlier version (commit from vault.history). The restore is itself a new version, so it can be undone. Requires the user's approval.",
				InputSchema: schema(`{"type":"object","properties":{
					"path":{"type":"string"},"id":{"type":"string"},
					"commit":{"type":"string"},"message":{"type":"string"}},"required":["commit"]}`),
				ActionClass: write,
				MinLevel:    approve,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct{ Path, ID, Commit, Message string }
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				key, err := toolKey(in.ID, in.Path)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				rel, err := v.locate(ctx, key)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				if err := checkAgentWritable(rel); err != nil {
					return kernel.ToolResult{}, err
				}
				if err := checkAgentReadable(v, rel); err != nil {
					return kernel.ToolResult{}, err
				}
				n, err := v.RestoreVersion(WithRunID(ctx, c.RunID), rel, in.Commit, commitMsg(in.Message, "restore "+rel+" to "+shortRev(in.Commit), c))
				if err != nil {
					return kernel.ToolResult{}, err
				}
				return kernel.ToolOutput(map[string]any{"id": n.Ref.Id, "path": n.Ref.Path, "commit": n.Commit})
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "vault.rename",
				Description: "Rename or move a vault note and update every link to it across the vault (wikilinks, embeds, Markdown links, frontmatter fields). Requires the user's approval.",
				InputSchema: schema(`{"type":"object","properties":{
					"from":{"type":"string","description":"Current path or id"},
					"to":{"type":"string","description":"New vault path, e.g. people/João Silva.md"},
					"message":{"type":"string"}},"required":["from","to"]}`),
				ActionClass: write,
				MinLevel:    approve,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct{ From, To, Message string }
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				from, err := v.locate(ctx, in.From)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				for _, p := range []string{from, in.To} {
					if err := checkAgentWritable(p); err != nil {
						return kernel.ToolResult{}, err
					}
				}
				res, err := v.Rename(WithRunID(ctx, c.RunID), from, in.To, false, commitMsg(in.Message, "rename "+from+" → "+in.To, c))
				if err != nil {
					return kernel.ToolResult{}, err
				}
				return kernel.ToolOutput(map[string]any{"id": res.Note.Ref.Id, "path": res.Note.Ref.Path, "updated_notes": res.Updated})
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "vault.tasks",
				Description: "List checkbox tasks from vault notes (`- [ ] text 📅 YYYY-MM-DD #tag`). Filter by status (open|done|all), due dates, tags, folder or note.",
				InputSchema: schema(`{"type":"object","properties":{
					"status":{"type":"string","enum":["open","done","all"]},
					"due_before":{"type":"string","description":"YYYY-MM-DD inclusive"},
					"due_after":{"type":"string"},
					"tags":{"type":"array","items":{"type":"string"}},
					"path_prefix":{"type":"string"},
					"note":{"type":"string","description":"Path or id of one note"},
					"text":{"type":"string"},
					"limit":{"type":"integer","minimum":1,"maximum":500}}}`),
				ActionClass: read,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct {
					Status     string   `json:"status"`
					DueBefore  string   `json:"due_before"`
					DueAfter   string   `json:"due_after"`
					Tags       []string `json:"tags"`
					PathPrefix string   `json:"path_prefix"`
					Note       string   `json:"note"`
					Text       string   `json:"text"`
					Limit      int      `json:"limit"`
				}
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				ts, err := v.ListTasks(ctx, TaskFilter{Status: in.Status, DueBefore: in.DueBefore, DueAfter: in.DueAfter, Tags: in.Tags,
					Prefix: in.PathPrefix, Note: in.Note, Text: in.Text, Limit: in.Limit})
				if err != nil {
					return kernel.ToolResult{}, err
				}
				type task struct {
					Path     string   `json:"path"`
					Line     int      `json:"line"`
					Text     string   `json:"text"`
					Done     bool     `json:"done"`
					Due      string   `json:"due,omitempty"`
					Priority string   `json:"priority,omitempty"`
					Tags     []string `json:"tags,omitempty"`
					Raw      string   `json:"raw"`
				}
				out := struct {
					Tasks []task `json:"tasks"`
				}{Tasks: []task{}}
				for _, t := range ts {
					out.Tasks = append(out.Tasks, task{t.Path, t.Line, t.Text, t.Done, t.Due, t.Priority, t.Tags, t.Raw})
				}
				return kernel.ToolOutput(out)
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "vault.complete_task",
				Description: "Check (or uncheck with done=false) a task found with vault.tasks. Pass raw from vault.tasks so a changed line is not toggled by mistake.",
				InputSchema: schema(`{"type":"object","properties":{
					"path":{"type":"string"},"line":{"type":"integer","minimum":1},
					"raw":{"type":"string"},"done":{"type":"boolean"}},"required":["path","line"]}`),
				ActionClass: write,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct {
					Path string `json:"path"`
					Line int    `json:"line"`
					Raw  string `json:"raw"`
					Done *bool  `json:"done"`
				}
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				if err := checkAgentWritable(in.Path); err != nil {
					return kernel.ToolResult{}, err
				}
				done := in.Done
				if done == nil {
					t := true
					done = &t
				}
				t, _, err := v.ToggleTask(WithRunID(ctx, c.RunID), in.Path, in.Line, in.Raw, done)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				return kernel.ToolOutput(map[string]any{"path": t.Path, "line": t.Line, "done": t.Done, "raw": t.Raw})
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "vault.related",
				Description: "Find notes related to a note: direct links, shared links, shared tags and similar text. Use it to gather context before writing about a person, project or topic.",
				InputSchema: schema(`{"type":"object","properties":{
					"path":{"type":"string"},"id":{"type":"string"},
					"limit":{"type":"integer","minimum":1,"maximum":50}}}`),
				ActionClass: read,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct {
					Path, ID string
					Limit    int
				}
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				key, err := toolKey(in.ID, in.Path)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				rel, err := v.Related(ctx, key, in.Limit)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				type r struct {
					ID      string   `json:"id"`
					Path    string   `json:"path"`
					Title   string   `json:"title"`
					Score   float64  `json:"score"`
					Reasons []string `json:"reasons"`
				}
				out := struct {
					Notes []r `json:"notes"`
				}{Notes: []r{}}
				for _, x := range rel {
					out.Notes = append(out.Notes, r{x.Entry.Key(), x.Entry.Path, x.Entry.Title, x.Score, x.Reasons})
				}
				return kernel.ToolOutput(out)
			},
		},
	}
}

func toolKey(id, path string) (string, error) {
	if id != "" {
		return id, nil
	}
	if path != "" {
		return path, nil
	}
	return "", fmt.Errorf("%w: path or id is required", ErrInvalid)
}

func shortRev(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
