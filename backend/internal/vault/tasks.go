package vault

import (
	"context"
	"fmt"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// TaskFilter narrows ListTasks.
type TaskFilter struct {
	Status    string // open (default) | done | all
	DueBefore string
	DueAfter  string
	Tags      []string
	Prefix    string
	Note      string // id or path
	Text      string
	Limit     int
}

// ListTasks returns checkbox tasks from notes (Obsidian Tasks syntax).
func (v *Vault) ListTasks(ctx context.Context, f TaskFilter) ([]index.Task, error) {
	ix := index.TaskFilter{DueBefore: f.DueBefore, DueAfter: f.DueAfter, Tags: f.Tags, Prefix: f.Prefix, Text: f.Text, Limit: f.Limit}
	switch strings.ToLower(f.Status) {
	case "", "open", "todo":
		no := false
		ix.Done = &no
	case "done", "completed":
		yes := true
		ix.Done = &yes
	case "all", "any":
	default:
		return nil, fmt.Errorf("%w: status must be open, done or all", ErrInvalid)
	}
	if f.Note != "" {
		rel, err := v.locate(ctx, f.Note)
		if err != nil {
			return nil, err
		}
		ix.Path = rel
	}
	if ix.Limit <= 0 {
		ix.Limit = 200
	}
	return v.ix.Tasks(ctx, ix)
}

// ToggleTask flips (or sets) the checkbox on a line. raw, when given, must
// equal the current line (optimistic concurrency); completing a task adds
// "✅ <today>" like Obsidian Tasks.
func (v *Vault) ToggleTask(ctx context.Context, idOrPath string, line int, raw string, done *bool) (*index.Task, *nexusv1.Note, error) {
	rel, err := v.locate(ctx, idOrPath)
	if err != nil {
		return nil, nil, err
	}
	if line <= 0 {
		return nil, nil, fmt.Errorf("%w: line is required", ErrInvalid)
	}
	var task *index.Task
	today := v.now().Format("2006-01-02")
	n, err := v.Update(ctx, rel, "", func(old []byte) ([]byte, error) {
		if old == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, rel)
		}
		lines := strings.Split(string(old), "\n")
		if line > len(lines) {
			return nil, fmt.Errorf("%w: %s has no line %d", ErrInvalid, rel, line)
		}
		cur := strings.TrimSuffix(lines[line-1], "\r")
		if raw != "" && strings.TrimRight(cur, " ") != strings.TrimRight(raw, " ") {
			return nil, fmt.Errorf("%w: line %d of %s changed", ErrConflict, line, rel)
		}
		nl, ok := note.ToggleTaskLine(cur, done, today)
		if !ok {
			return nil, fmt.Errorf("%w: line %d of %s is not a task", ErrInvalid, line, rel)
		}
		if strings.HasSuffix(lines[line-1], "\r") {
			nl += "\r"
		}
		lines[line-1] = nl
		t, _ := note.ParseTaskLine(strings.TrimSuffix(nl, "\r"))
		task = &index.Task{Path: rel, Line: line, Status: t.Status, Done: t.Done, Text: t.Text, Due: t.Due, Scheduled: t.Scheduled,
			Start: t.Start, DoneDate: t.DoneDate, Priority: t.Priority, Tags: t.Tags, Raw: strings.TrimSuffix(nl, "\r")}
		return []byte(strings.Join(lines, "\n")), nil
	})
	if err != nil {
		return nil, nil, err
	}
	return task, n, nil
}

func (v *Vault) taskProto(ctx context.Context, t index.Task) *nexusv1.NoteTask {
	out := &nexusv1.NoteTask{
		Line: int32(t.Line), Text: t.Text, Done: t.Done, Status: t.Status, Due: t.Due, Scheduled: t.Scheduled,
		Start: t.Start, DoneDate: t.DoneDate, Priority: t.Priority, Tags: t.Tags, Raw: t.Raw,
	}
	if e, ok := v.ix.Get(ctx, t.Path); ok {
		out.Note = entryRef(e)
	} else {
		out.Note = &nexusv1.NoteRef{Path: t.Path, Title: t.Path}
	}
	return out
}
