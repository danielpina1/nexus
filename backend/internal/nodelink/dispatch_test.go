package nodelink

import (
	"errors"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/nodelink/wire"
	"github.com/danielpina1/nexus/backend/kernel"
)

func TestSelectNode(t *testing.T) {
	rt := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	cands := []candidate{
		{id: "mac", online: true, tags: []string{"mac", "browser"}, runtimes: rt("claude-code", "shell"), active: 2},
		{id: "vps", online: true, tags: []string{"vps"}, runtimes: rt("shell"), active: 0},
		{id: "gpu", online: false, tags: []string{"gpu"}, runtimes: rt("ollama", "shell"), active: 0},
		{id: "box", online: true, tags: []string{"vps", "big"}, runtimes: rt("shell"), active: 0},
	}
	tests := []struct {
		name string
		sel  kernel.NodeSelector
		want string
		err  error
	}{
		{"least active, tie by id", kernel.NodeSelector{Runtime: "shell"}, "box", nil},
		{"any tag", kernel.NodeSelector{Tags: []string{"any"}, Runtime: "shell"}, "box", nil},
		{"tag subset", kernel.NodeSelector{Tags: []string{"browser"}}, "mac", nil},
		{"tags must all match", kernel.NodeSelector{Tags: []string{"vps", "big"}}, "box", nil},
		{"tag mismatch", kernel.NodeSelector{Tags: []string{"mac", "vps"}}, "", ErrNoNode},
		{"runtime missing", kernel.NodeSelector{Runtime: "codex"}, "", ErrNoNode},
		{"explicit id", kernel.NodeSelector{NodeID: "mac", Runtime: "shell"}, "mac", nil},
		{"explicit id offline", kernel.NodeSelector{NodeID: "gpu"}, "", ErrNoNode},
		{"explicit id wrong runtime", kernel.NodeSelector{NodeID: "vps", Runtime: "claude-code"}, "", ErrNoNode},
		{"offline excluded", kernel.NodeSelector{Runtime: "ollama"}, "", ErrNoNode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectNode(cands, tc.sel)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestReliable(t *testing.T) {
	tests := []struct {
		msg  *nexusv1.ConnectResponse
		want bool
	}{
		{&nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Assign{Assign: &nexusv1.RunAssignment{}}}, true},
		{&nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Input{Input: &nexusv1.RunInput{RunId: "r"}}}, true},
		{&nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Input{Input: &nexusv1.RunInput{QuestionId: wire.ControlCallPrefix + "x"}}}, false},
		{&nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Cancel{Cancel: &nexusv1.RunCancel{}}}, true},
		{&nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Resolution{Resolution: &nexusv1.ApprovalResolution{}}}, true},
		{&nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Ack{Ack: &nexusv1.Ack{}}}, false},
		{&nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_ToolResponse{ToolResponse: &nexusv1.ToolResponse{}}}, false},
	}
	for i, tc := range tests {
		if got := wire.Reliable(tc.msg); got != tc.want {
			t.Errorf("%d: got %v want %v", i, got, tc.want)
		}
	}
}

func TestStructRoundTrip(t *testing.T) {
	for _, in := range []string{`{"a":1}`, `[1,2]`, `"s"`, `3`} {
		s, err := wire.JSONToStruct([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		out, err := wire.StructToJSON(s)
		if err != nil || string(out) != in {
			t.Fatalf("%s -> %s (%v)", in, out, err)
		}
	}
	if s, err := wire.JSONToStruct(nil); s != nil || err != nil {
		t.Fatal("empty input should be nil")
	}
}
