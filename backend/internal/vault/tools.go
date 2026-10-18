package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Tools returns the hub tools backed by the vault: memory.search, vault.read,
// vault.write, vault.link, vault.daily_append, facts.propose and the
// extended tools (vault.query, vault.history, vault.restore, vault.rename,
// vault.tasks, vault.complete_task, vault.related; see tools_ext.go).
func Tools(v *Vault) []kernel.Tool {
	return append(baseTools(v), extTools(v)...)
}

func baseTools(v *Vault) []kernel.Tool {
	read := nexusv1.ActionClass_ACTION_CLASS_READ
	write := nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL
	return []kernel.Tool{
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "memory.search",
				Description: "Search the user's knowledge vault (notes, people, projects, episodes, facts). Returns matching notes with snippets; cite them by [[title]].",
				InputSchema: schema(`{"type":"object","properties":{
					"query":{"type":"string","description":"Search text"},
					"types":{"type":"array","items":{"type":"string"},"description":"Note types to include, e.g. person, project, episode"},
					"limit":{"type":"integer","minimum":1,"maximum":50}},"required":["query"]}`),
				ActionClass: read,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct {
					Query string   `json:"query"`
					Types []string `json:"types"`
					Limit int      `json:"limit"`
				}
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				hits, err := v.Search(ctx, in.Query, in.Types, in.Limit)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				type hit struct {
					ID      string  `json:"id"`
					Path    string  `json:"path"`
					Title   string  `json:"title"`
					Snippet string  `json:"snippet"`
					Score   float32 `json:"score"`
				}
				out := struct {
					Hits []hit `json:"hits"`
				}{Hits: []hit{}}
				for _, h := range hits {
					out.Hits = append(out.Hits, hit{h.Note.Id, h.Note.Path, h.Note.Title, h.Snippet, h.Score})
				}
				return kernel.ToolOutput(out)
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "vault.read",
				Description: "Read a vault note by path (e.g. people/João.md) or id. Returns frontmatter, body, links and backlinks, and `commit` for safe updates.",
				InputSchema: schema(`{"type":"object","properties":{
					"path":{"type":"string"},"id":{"type":"string"}}}`),
				ActionClass: read,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct{ Path, ID string }
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				key := in.ID
				if key == "" {
					key = in.Path
				}
				if key == "" {
					return kernel.ToolResult{}, fmt.Errorf("%w: path or id is required", ErrInvalid)
				}
				n, err := v.Read(ctx, key)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				if err := checkAgentReadable(v, n.GetRef().GetPath()); err != nil {
					return kernel.ToolResult{}, err
				}
				return kernel.ToolOutput(noteJSON(n))
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "vault.write",
				Description: "Create or replace a Markdown note in the vault (YAML frontmatter + body), or append to it. Every write is a git commit. Pass base_commit from vault.read to avoid overwriting concurrent edits.",
				InputSchema: schema(`{"type":"object","properties":{
					"path":{"type":"string","description":"Vault-relative .md path, e.g. people/Maria.md"},
					"content":{"type":"string","description":"Full Markdown (replaces the note)"},
					"append":{"type":"string","description":"Markdown appended to the note instead of replacing it"},
					"message":{"type":"string","description":"Commit message"},
					"base_commit":{"type":"string"}},"required":["path"]}`),
				ActionClass: write,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct {
					Path, Content, Append, Message string
					BaseCommit                     string `json:"base_commit"`
				}
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				if err := checkAgentWritable(in.Path); err != nil {
					return kernel.ToolResult{}, err
				}
				ctx = WithRunID(ctx, c.RunID)
				msg := commitMsg(in.Message, "write "+in.Path, c)
				var n *nexusv1.Note
				var err error
				switch {
				case in.Append != "" && in.Content == "":
					n, err = v.Update(ctx, in.Path, msg, func(old []byte) ([]byte, error) {
						s := string(old)
						if s != "" && !strings.HasSuffix(s, "\n") {
							s += "\n"
						}
						return []byte(s + in.Append + "\n"), nil
					})
				case in.Content != "":
					n, err = v.WriteNote(ctx, in.Path, []byte(in.Content), msg, in.BaseCommit)
				default:
					return kernel.ToolResult{}, fmt.Errorf("%w: content or append is required", ErrInvalid)
				}
				if err != nil {
					return kernel.ToolResult{}, err
				}
				return kernel.ToolOutput(map[string]any{"id": n.Ref.Id, "path": n.Ref.Path, "title": n.Ref.Title, "commit": n.Commit})
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "vault.link",
				Description: "Link two notes. With a relation (e.g. works_at, prefers) adds a typed `relation:: [[to]]` field, else a plain [[to]] link, to the `from` note.",
				InputSchema: schema(`{"type":"object","properties":{
					"from":{"type":"string","description":"Note path or id"},
					"to":{"type":"string","description":"Note path, id or name"},
					"relation":{"type":"string"}},"required":["from","to"]}`),
				ActionClass: write,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct{ From, To, Relation string }
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				if err := checkAgentWritable(in.From); err != nil {
					return kernel.ToolResult{}, err
				}
				if cur, err := v.Read(ctx, in.From); err == nil && cur.GetRef() != nil {
					if err := checkAgentWritable(cur.Ref.Path); err != nil {
						return kernel.ToolResult{}, err
					}
				}
				n, err := v.Link(WithRunID(ctx, c.RunID), in.From, in.To, in.Relation)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				return kernel.ToolOutput(map[string]any{"id": n.Ref.Id, "path": n.Ref.Path, "commit": n.Commit})
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "vault.daily_append",
				Description: "Append text to today's daily note (daily/YYYY-MM-DD.md).",
				InputSchema: schema(`{"type":"object","properties":{
					"text":{"type":"string"},
					"date":{"type":"string","description":"YYYY-MM-DD; default today"}},"required":["text"]}`),
				ActionClass: write,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct{ Text, Date string }
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				var day time.Time
				if in.Date != "" {
					d, err := time.ParseInLocation("2006-01-02", in.Date, time.Local)
					if err != nil {
						return kernel.ToolResult{}, fmt.Errorf("%w: date must be YYYY-MM-DD", ErrInvalid)
					}
					day = d
				}
				n, err := v.DailyAppend(WithRunID(ctx, c.RunID), in.Text, day)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				return kernel.ToolOutput(map[string]any{"id": n.Ref.Id, "path": n.Ref.Path})
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name:        "facts.propose",
				Description: "Propose a fact (subject–predicate–object) for the user's review queue, e.g. subject \"João Silva\", predicate \"works_at\", object \"[[ACME]]\".",
				InputSchema: schema(`{"type":"object","properties":{
					"subject":{"type":"string","description":"Entity note name, path or id"},
					"predicate":{"type":"string"},
					"object":{"type":"string"},
					"confidence":{"type":"number","minimum":0,"maximum":1},
					"valid_from":{"type":"string","description":"YYYY-MM-DD"},
					"source":{"type":"string","description":"Where it came from (note link, URL)"}},
					"required":["subject","predicate","object","confidence"]}`),
				ActionClass: write,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in FactInput
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				if in.Source == "" && c.AgentID != "" {
					in.Source = "agent:" + c.AgentID
				}
				f, err := v.ProposeFact(WithRunID(ctx, c.RunID), in)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				return kernel.ToolOutput(map[string]any{"id": f.Id, "path": f.Source.GetPath(), "status": "proposed"})
			},
		},
	}
}

func schema(s string) json.RawMessage {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("vault: bad tool schema: " + err.Error())
	}
	b, _ := json.Marshal(v)
	return b
}

func commitMsg(msg, def string, c kernel.ToolCall) string {
	if strings.TrimSpace(msg) == "" {
		msg = def
	}
	if c.AgentID != "" {
		msg += " (agent " + c.AgentID + ")"
	}
	return msg
}

type noteOut struct {
	ID          string         `json:"id"`
	Path        string         `json:"path"`
	Title       string         `json:"title"`
	Type        string         `json:"type,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
	Body        string         `json:"body"`
	Links       []refOut       `json:"links,omitempty"`
	Backlinks   []refOut       `json:"backlinks,omitempty"`
	Commit      string         `json:"commit"`
}

type refOut struct {
	ID    string `json:"id,omitempty"`
	Path  string `json:"path,omitempty"`
	Title string `json:"title"`
}

func refsOut(rs []*nexusv1.NoteRef) []refOut {
	var out []refOut
	for _, r := range rs {
		out = append(out, refOut{r.Id, r.Path, r.Title})
	}
	return out
}

func noteJSON(n *nexusv1.Note) noteOut {
	return noteOut{
		ID: n.Ref.Id, Path: n.Ref.Path, Title: n.Ref.Title, Type: n.Type, Tags: n.Tags,
		Frontmatter: n.Frontmatter.AsMap(), Body: n.Body,
		Links: refsOut(n.Links), Backlinks: refsOut(n.Backlinks), Commit: n.Commit,
	}
}

// protectedDirs hold configuration (agents, their permissions, automations,
// trusted shops). Agents must never change them: an agent that could write
// agents/ or shops/ could widen its own tools, policies or allowed domains.
// The owner edits them from the dashboard, the app or Obsidian, which use
// VaultService, not these tools.
var protectedDirs = []string{"agents/", "policies/", "workflows/", "shops/", "teams/", "skills/"}

// checkAgentWritable rejects agent writes to configuration folders.
func checkAgentWritable(path string) error {
	p := strings.ToLower(strings.TrimLeft(filepath.ToSlash(filepath.Clean(path)), "/"))
	for _, d := range protectedDirs {
		if strings.HasPrefix(p, d) {
			return fmt.Errorf("%w: %s is configuration; only the owner can change it (from the dashboard, the app or Obsidian)", ErrInvalid, d)
		}
	}
	return nil
}

// checkAgentReadable rejects agent reads of encrypted notes unless
// vault.encryption.agent_access is set.
func checkAgentReadable(v *Vault, path string) error {
	if !v.AgentMayRead(path) {
		return fmt.Errorf("%w: %s is in an encrypted folder; agents may not read it (vault.encryption.agent_access)", ErrInvalid, path)
	}
	return nil
}
