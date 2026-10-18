package node

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func namedTool(name string) kernel.Tool {
	return kernel.ToolFunc{S: kernel.ToolSpec{Name: name}, F: func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
		return kernel.ToolResult{}, nil
	}}
}

func toolNames(ts []kernel.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Spec().Name)
	}
	return out
}

func TestCodeOnlyTools(t *testing.T) {
	tools := []kernel.Tool{namedTool("memory.search"), namedTool("anthrex.status"), namedTool("anthrex.accept")}
	ctx := func(m map[string]any) *structpb.Struct { s, _ := structpb.NewStruct(m); return s }
	cases := []struct {
		name string
		a    *nexusv1.RunAssignment
		want int
	}{
		{"assistant gets no anthrex tools", &nexusv1.RunAssignment{Agent: &nexusv1.Agent{Id: "assistant", Runtime: "auto"}}, 1},
		{"shop agent even if listed", &nexusv1.RunAssignment{Agent: &nexusv1.Agent{Id: "shop", Runtime: "auto", Tools: []string{"anthrex.status"}}}, 1},
		{"claude coding:false", &nexusv1.RunAssignment{Agent: &nexusv1.Agent{Runtime: "claude-code"}, Context: ctx(map[string]any{"claude": map[string]any{"coding": false}})}, 1},
		{"anthrex runtime", &nexusv1.RunAssignment{Agent: &nexusv1.Agent{Runtime: "anthrex"}}, 3},
		{"claude coding:true", &nexusv1.RunAssignment{Agent: &nexusv1.Agent{Runtime: "claude-code"}, Context: ctx(map[string]any{"claude": map[string]any{"coding": true}})}, 3},
		{"codex coding:true", &nexusv1.RunAssignment{Agent: &nexusv1.Agent{Runtime: "auto"}, Context: ctx(map[string]any{"codex": map[string]any{"coding": true}})}, 3},
	}
	for _, c := range cases {
		got := codeOnlyTools(c.a, append([]kernel.Tool(nil), tools...))
		if len(got) != c.want {
			t.Errorf("%s: tools = %v, want %d", c.name, toolNames(got), c.want)
		}
	}
}
