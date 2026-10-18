package index

import (
	"context"
	"encoding/json"
	"strings"
)

// Task is an indexed checkbox item (see note.Task).
type Task struct {
	Path      string
	Line      int
	Status    string
	Done      bool
	Text      string
	Due       string
	Scheduled string
	Start     string
	DoneDate  string
	Priority  string
	Tags      []string
	Raw       string
}

// TaskFilter narrows Tasks. Empty fields do not filter.
type TaskFilter struct {
	// Done: nil = all, true = done/cancelled, false = open.
	Done      *bool
	DueBefore string // inclusive YYYY-MM-DD
	DueAfter  string // inclusive YYYY-MM-DD
	Tags      []string
	Prefix    string
	Path      string
	Text      string
	Limit     int
}

// Tasks returns tasks ordered by due date (undated last), path and line.
func (ix *Index) Tasks(ctx context.Context, f TaskFilter) ([]Task, error) {
	q := `SELECT path, line, status, done, text, due, scheduled, start, done_date, priority, tags, raw FROM tasks WHERE 1=1`
	var args []any
	if f.Done != nil {
		q += ` AND done = ?`
		if *f.Done {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if f.DueBefore != "" {
		q += ` AND due != '' AND due <= ?`
		args = append(args, f.DueBefore)
	}
	if f.DueAfter != "" {
		q += ` AND due >= ?`
		args = append(args, f.DueAfter)
	}
	if f.Prefix != "" {
		q += ` AND substr(path, 1, ?) = ?`
		args = append(args, len(f.Prefix), f.Prefix)
	}
	if f.Path != "" {
		q += ` AND path = ?`
		args = append(args, f.Path)
	}
	if f.Text != "" {
		q += ` AND instr(lower(text), ?) > 0`
		args = append(args, strings.ToLower(f.Text))
	}
	q += ` ORDER BY due = '', due, path, line`
	rows, err := ix.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var done int
		var tags string
		if err := rows.Scan(&t.Path, &t.Line, &t.Status, &done, &t.Text, &t.Due, &t.Scheduled, &t.Start, &t.DoneDate, &t.Priority, &tags, &t.Raw); err != nil {
			return nil, err
		}
		t.Done = done == 1
		_ = json.Unmarshal([]byte(tags), &t.Tags)
		if !hasAnyTag(t.Tags, f.Tags) {
			continue
		}
		out = append(out, t)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, rows.Err()
}

// hasAnyTag matches tags case-insensitively, including nested tags
// ("work" matches "work/acme").
func hasAnyTag(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		w = strings.ToLower(strings.TrimPrefix(w, "#"))
		for _, h := range have {
			h = strings.ToLower(h)
			if h == w || strings.HasPrefix(h, w+"/") {
				return true
			}
		}
	}
	return false
}

// HasAnyTag is hasAnyTag for other packages.
func HasAnyTag(have, want []string) bool { return hasAnyTag(have, want) }
