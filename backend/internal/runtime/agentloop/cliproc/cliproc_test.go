package cliproc

import (
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func TestWorkdir(t *testing.T) {
	root := t.TempDir()
	ctxWith := func(dir string) *structpb.Struct {
		s, _ := structpb.NewStruct(map[string]any{"repo_dir": dir})
		return s
	}
	opts := WorkdirOptions{PerAgent: map[string]string{"coder": "/srv/repo"}, AllowedRoots: []string{root}}
	tests := []struct {
		name    string
		a       *nexusv1.RunAssignment
		want    string
		wantErr bool
	}{
		{"default", &nexusv1.RunAssignment{Agent: &nexusv1.Agent{Id: "x"}}, "/run", false},
		{"per agent", &nexusv1.RunAssignment{Agent: &nexusv1.Agent{Id: "coder"}}, "/srv/repo", false},
		{"allowed repo_dir", &nexusv1.RunAssignment{Context: ctxWith(filepath.Join(root, "proj"))}, filepath.Join(root, "proj"), false},
		{"escape", &nexusv1.RunAssignment{Context: ctxWith(filepath.Join(root, "..", "etc"))}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Workdir(tt.a, "/run", opts)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestTailAndReadLines(t *testing.T) {
	tl := &Tail{N: 5}
	tl.Write([]byte("hello world"))
	if tl.String() != "world" {
		t.Fatalf("tail %q", tl.String())
	}
	var lines []string
	_ = ReadLines(strings.NewReader("a\n\n b \nc"), func(l []byte) { lines = append(lines, string(l)) })
	if strings.Join(lines, ",") != "a,b,c" {
		t.Fatalf("lines %v", lines)
	}
}
