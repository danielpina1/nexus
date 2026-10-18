package note

import "testing"

func TestParseTaskLine(t *testing.T) {
	tk, ok := ParseTaskLine("- [ ] Call João 📅 2026-10-20 ⏳ 2026-10-18 🔼 #work #calls")
	if !ok || tk.Done || tk.Due != "2026-10-20" || tk.Scheduled != "2026-10-18" || tk.Priority != "medium" ||
		len(tk.Tags) != 2 || tk.Text != "Call João #work #calls" {
		t.Fatalf("task = %+v", tk)
	}
	tk, ok = ParseTaskLine("  * [x] Pay rent ✅ 2026-10-01 [priority:: high]")
	if !ok || !tk.Done || tk.DoneDate != "2026-10-01" || tk.Priority != "high" || tk.Text != "Pay rent" {
		t.Fatalf("done task = %+v", tk)
	}
	tk, ok = ParseTaskLine("1. [ ] Renew [due:: 2026-11-01]")
	if !ok || tk.Due != "2026-11-01" || tk.Text != "Renew" {
		t.Fatalf("numbered = %+v", tk)
	}
	if _, ok := ParseTaskLine("- not a task"); ok {
		t.Fatal("plain bullet parsed as task")
	}
	n := Parse("x.md", []byte("# T\n```\n- [ ] in code\n```\n- [ ] real\n"))
	if len(n.Tasks) != 1 || n.Tasks[0].Line != 5 {
		t.Fatalf("tasks = %+v", n.Tasks)
	}
}

func TestToggleTaskLine(t *testing.T) {
	out, ok := ToggleTaskLine("- [ ] Pay 📅 2026-10-20", nil, "2026-10-18")
	if !ok || out != "- [x] Pay 📅 2026-10-20 ✅ 2026-10-18" {
		t.Fatalf("toggle = %q", out)
	}
	back, _ := ToggleTaskLine(out, nil, "2026-10-18")
	if back != "- [ ] Pay 📅 2026-10-20" {
		t.Fatalf("untoggle = %q", back)
	}
	done := true
	if same, _ := ToggleTaskLine(out, &done, "x"); same != out {
		t.Fatalf("set done on done changed: %q", same)
	}
}
