package vault

import (
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	a := "one\ntwo\nthree\nfour\n"
	b := "one\n2\nthree\nfour\nfive\n"
	d := UnifiedDiff("a", "b", a, b)
	for _, want := range []string{"--- a\n+++ b\n", "-two\n", "+2\n", "+five\n", " three\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
	if UnifiedDiff("a", "b", a, a) != "" {
		t.Error("equal texts produced a diff")
	}
	if d := UnifiedDiff("a", "b", "", "x\n"); !strings.Contains(d, "@@ -0,0 +1 @@\n+x\n") {
		t.Errorf("create diff:\n%s", d)
	}
	if d := UnifiedDiff("a", "b", "x", "y"); !strings.Contains(d, "No newline") {
		t.Errorf("no-newline diff:\n%s", d)
	}
}

func TestDiffLinesRoundTrip(t *testing.T) {
	cases := [][2]string{
		{"", "a\nb\n"}, {"a\nb\n", ""}, {"a\nb\nc\n", "c\nb\na\n"},
		{"x\ny\nz\n", "x\nq\nz\nw\n"}, {"1\n2\n3\n4\n5\n6\n", "0\n1\n3\n4\n6\n7\n"},
	}
	for _, c := range cases {
		a, b := splitLines(c[0]), splitLines(c[1])
		var ra, rb []string
		for _, e := range diffLines(a, b) {
			switch e.kind {
			case editEqual:
				ra = append(ra, a[e.a])
				rb = append(rb, b[e.b])
			case editDelete:
				ra = append(ra, a[e.a])
			case editInsert:
				rb = append(rb, b[e.b])
			}
		}
		if strings.Join(ra, "") != c[0] || strings.Join(rb, "") != c[1] {
			t.Errorf("diff %q→%q does not reproduce inputs: %q %q", c[0], c[1], ra, rb)
		}
	}
}

func TestMerge3(t *testing.T) {
	base := "title\n\nline a\nline b\nline c\n"
	ours := "title\n\nline A\nline b\nline c\n"
	theirs := "title\n\nline a\nline b\nline C\nline d\n"
	got, ok := Merge3(base, ours, theirs)
	if !ok || got != "title\n\nline A\nline b\nline C\nline d\n" {
		t.Fatalf("clean merge = %v %q", ok, got)
	}
	got, ok = Merge3(base, "title\n\nX\nline b\nline c\n", "title\n\nY\nline b\nline c\n")
	if ok || !strings.Contains(got, "<<<<<<< current\nX\n=======\nY\n>>>>>>> incoming\n") {
		t.Fatalf("conflict merge = %v %q", ok, got)
	}
	if got, ok := Merge3("a\n", "a\nb\n", "a\nb\n"); !ok || got != "a\nb\n" {
		t.Fatalf("same change = %v %q", ok, got)
	}
}
