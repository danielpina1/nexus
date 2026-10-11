package main

import (
	"flag"
	"slices"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		args   []string
		pos    []string
		agent  string
		follow bool
	}{
		{[]string{"hello world"}, []string{"hello world"}, "", false},
		{[]string{"hi", "-follow", "-agent", "a"}, []string{"hi"}, "a", true},
		{[]string{"-agent=a", "hi", "there"}, []string{"hi", "there"}, "a", false},
		{nil, nil, "", false},
	}
	for _, tt := range tests {
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		agent := fs.String("agent", "", "")
		follow := fs.Bool("follow", false, "")
		pos, err := parseArgs(fs, tt.args)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(pos, tt.pos) || *agent != tt.agent || *follow != tt.follow {
			t.Errorf("%v: pos=%v agent=%q follow=%v", tt.args, pos, *agent, *follow)
		}
	}
}

func TestFormat(t *testing.T) {
	if got := stateName(nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL); got != "waiting-approval" {
		t.Errorf("stateName = %q", got)
	}
	if got := tierName(nexusv1.Tier_TIER_UNSPECIFIED); got != "-" {
		t.Errorf("tierName = %q", got)
	}
	if got := truncate("a  b\nc", 10); got != "a b c" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("abcdefghij", 5); got != "abcd…" {
		t.Errorf("truncate = %q", got)
	}
	ev := &nexusv1.Event{Seq: 7, TaskId: "tsk_1", Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{Text: "hi"}}}
	if kind, text := summarize(ev); kind != "output" || text != "hi" {
		t.Errorf("summarize = %q %q", kind, text)
	}
	if line := eventLine(ev); line == "" {
		t.Error("empty line")
	}
}
