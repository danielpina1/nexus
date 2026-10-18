package note

import (
	"regexp"
	"strings"
)

// Task is a Markdown checkbox item, Obsidian Tasks / Dataview compatible:
//
//   - [ ] Call João 📅 2026-10-20 ⏳ 2026-10-18 🔼 #work
//   - [x] Pay rent ✅ 2026-10-01
//   - [ ] Renew passport [due:: 2026-11-01] [priority:: high]
type Task struct {
	Line      int    // 1-based line in the file
	Status    string // checkbox character: " ", "x", "/", "-", ">"…
	Done      bool   // x/X (done) or - (cancelled)
	Text      string // description without checkbox, dates, priority and fields
	Due       string // YYYY-MM-DD
	Scheduled string
	Start     string
	DoneDate  string
	Created   string
	Priority  string // highest | high | medium | low | lowest
	Tags      []string
	Raw       string
}

var (
	reTask      = regexp.MustCompile(`^(\s*(?:>\s*)*(?:[-*+]|\d+[.)])\s+)\[(.)\]\s?(.*)$`)
	reTaskDate  = regexp.MustCompile(`(📅|⏳|🛫|✅|➕)️?\s*(\d{4}-\d{2}-\d{2})`)
	reTaskField = regexp.MustCompile(`\[(due|scheduled|start|completion|created|priority)::\s*([^\]]*)\]`)
	reTaskPrio  = regexp.MustCompile(`\s*(🔺|⏫|🔼|🔽|⏬)️?`)
	reTaskTag   = regexp.MustCompile(`(?:^|\s)#([\p{L}\p{N}_/\-]+)`)
)

var prioNames = map[string]string{"🔺": "highest", "⏫": "high", "🔼": "medium", "🔽": "low", "⏬": "lowest"}

// ParseTaskLine parses one line; ok is false when it is not a checkbox.
func ParseTaskLine(line string) (Task, bool) {
	m := reTask.FindStringSubmatch(line)
	if m == nil {
		return Task{}, false
	}
	t := Task{Status: m[2], Raw: line}
	t.Done = m[2] == "x" || m[2] == "X" || m[2] == "-"
	rest := m[3]
	for _, d := range reTaskDate.FindAllStringSubmatch(rest, -1) {
		switch d[1] {
		case "📅":
			t.Due = d[2]
		case "⏳":
			t.Scheduled = d[2]
		case "🛫":
			t.Start = d[2]
		case "✅":
			t.DoneDate = d[2]
		case "➕":
			t.Created = d[2]
		}
	}
	rest = reTaskDate.ReplaceAllString(rest, "")
	for _, f := range reTaskField.FindAllStringSubmatch(rest, -1) {
		v := strings.TrimSpace(f[2])
		switch f[1] {
		case "due":
			t.Due = v
		case "scheduled":
			t.Scheduled = v
		case "start":
			t.Start = v
		case "completion":
			t.DoneDate = v
		case "created":
			t.Created = v
		case "priority":
			t.Priority = strings.ToLower(v)
		}
	}
	rest = reTaskField.ReplaceAllString(rest, "")
	if p := reTaskPrio.FindStringSubmatch(rest); p != nil {
		t.Priority = prioNames[p[1]]
	}
	rest = reTaskPrio.ReplaceAllString(rest, "")
	for _, tg := range reTaskTag.FindAllStringSubmatch(rest, -1) {
		t.Tags = append(t.Tags, tg[1])
	}
	t.Text = strings.Join(strings.Fields(rest), " ")
	return t, true
}

// ToggleTaskLine flips (or sets, when done is non-nil) a checkbox line.
// Completing adds "✅ <today>" and reopening removes it.
func ToggleTaskLine(line string, done *bool, today string) (string, bool) {
	m := reTask.FindStringSubmatchIndex(line)
	if m == nil {
		return line, false
	}
	cur := line[m[4]:m[5]]
	isDone := cur == "x" || cur == "X"
	want := !isDone
	if done != nil {
		want = *done
	}
	if want == isDone {
		return line, true
	}
	box := " "
	if want {
		box = "x"
	}
	out := line[:m[4]] + box + line[m[5]:]
	if want {
		if !strings.Contains(out, "✅") && today != "" {
			out = strings.TrimRight(out, " ") + " ✅ " + today
		}
	} else {
		out = regexp.MustCompile(`\s*✅️?\s*\d{4}-\d{2}-\d{2}`).ReplaceAllString(out, "")
	}
	return out, true
}
