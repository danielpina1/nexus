package anthrex

import (
	"bytes"
	"encoding/hex"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestMsgpackRoundTrip(t *testing.T) {
	cases := []any{
		nil, true, false,
		int64(0), int64(127), int64(128), int64(255), int64(256), int64(65535), int64(65536),
		int64(math.MaxUint32), int64(math.MaxUint32) + 1, int64(math.MaxInt64),
		uint64(math.MaxUint64),
		int64(-1), int64(-32), int64(-33), int64(-128), int64(-129), int64(-32768), int64(-32769),
		int64(math.MinInt32), int64(math.MinInt32) - 1, int64(math.MinInt64),
		1.5, -2.25,
		"", "a", strings.Repeat("x", 31), strings.Repeat("y", 32), strings.Repeat("z", 255), strings.Repeat("w", 256), strings.Repeat("v", 70000),
		[]byte{}, []byte{0, 0xff}, bytes.Repeat([]byte{1}, 300), bytes.Repeat([]byte{2}, 70000),
		[]any{}, []any{int64(1), "two", nil}, make([]any, 20),
		map[string]any{}, map[string]any{"a": int64(1), "b": []any{true}},
		Ext{Type: 5, Data: []byte{1, 2, 3}},
	}
	big := map[string]any{}
	for i := 0; i < 40; i++ {
		big[strings.Repeat("k", i+1)] = int64(i)
	}
	cases = append(cases, big)
	for i, c := range cases {
		b, err := Marshal(c)
		if err != nil {
			t.Fatalf("case %d: marshal: %v", i, err)
		}
		got, err := Unmarshal(b)
		if err != nil {
			t.Fatalf("case %d: unmarshal: %v", i, err)
		}
		want := c
		if s, ok := c.([]any); ok && len(s) == 20 {
			want = make([]any, 20) // nils
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("case %d: got %#v want %#v", i, got, want)
		}
	}
}

// Byte layouts rmp_serde produces (rmp writes the smallest marker).
func TestMsgpackKnownBytes(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{unit("ListWindows"), "ab4c69737457696e646f7773"},
		{int64(21), "15"},
		{uint32(200), "ccc8"},
		{int64(-1), "ff"},
		{[]byte{0, 0xff}, "c40200ff"},
		{nil, "c0"},
		// {"Kill": {"window_id": 3}}
		{MsgKill(3), "81a44b696c6c81a977696e646f775f696403"},
	}
	for _, c := range cases {
		b, err := Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(b); got != c.want {
			t.Errorf("%#v: got %s want %s", c.v, got, c.want)
		}
	}
}

func TestMsgpackRejectsGarbage(t *testing.T) {
	for _, b := range [][]byte{
		{0xc1},                   // never used
		{0xdc, 0xff, 0xff},       // array16 longer than the data
		{0xdf, 0xff, 0xff, 0xff}, // truncated length
		{0xa5, 'a'},              // short string
		{0x01, 0x02},             // trailing byte
	} {
		if _, err := Unmarshal(b); err == nil {
			t.Errorf("%x: want error", b)
		}
	}
	// Deep nesting is refused, not a stack overflow.
	deep := bytes.Repeat([]byte{0x91}, 10000)
	deep = append(deep, 0xc0)
	if _, err := Unmarshal(deep); err == nil {
		t.Error("deep nesting accepted")
	}
}

func TestFrameRoundTripAndLimits(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, MsgHello(ProtoVersion, ClientCLI)); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&buf, MsgUnsubscribe()); err != nil {
		t.Fatal(err)
	}
	hdr := buf.Bytes()[:4]
	if n := int(hdr[0])<<24 | int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3]); n <= 0 {
		t.Fatalf("bad header %x", hdr)
	}
	v, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	kind, body, _ := SplitVariant(v)
	if kind != "Hello" || body.(map[string]any)["proto_version"] != int64(21) || body.(map[string]any)["client"] != "cli" {
		t.Fatalf("hello = %#v", v)
	}
	v, err = ReadFrame(&buf)
	if err != nil || v != "Unsubscribe" {
		t.Fatalf("second frame %v %v", v, err)
	}
	if _, err := ReadFrame(&buf); err == nil {
		t.Fatal("want EOF")
	}
	// Oversized header.
	if _, err := ReadFrame(bytes.NewReader([]byte{0x01, 0x00, 0x00, 0x01})); err == nil {
		t.Fatal("oversized frame accepted")
	}
	// Truncated header.
	if _, err := ReadFrame(bytes.NewReader([]byte{0, 0})); err == nil {
		t.Fatal("truncated header accepted")
	}
}

func TestDocGateActionEncoding(t *testing.T) {
	v1 := uint32(2)
	cases := []struct {
		a    DocGateAction
		want any
	}{
		{DocGateAction{Kind: "approve"}, "approve"},
		{DocGateAction{Kind: "approve", Version: &v1}, map[string]any{"approve": map[string]any{"version": v1}}},
		{DocGateAction{Kind: "changes", Note: "n", Review: true}, map[string]any{"changes": map[string]any{"note": "n", "review": true}}},
		{DocGateAction{Kind: "back", Note: "b"}, map[string]any{"back": map[string]any{"note": "b"}}},
		{DocGateAction{Kind: "reject"}, "reject"},
	}
	for _, c := range cases {
		got, err := c.a.Value()
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: got %#v (%v)", c.a, got, err)
		}
	}
	if _, err := (DocGateAction{Kind: "nope"}).Value(); err == nil {
		t.Error("unknown action accepted")
	}
}

func TestMessageTarget(t *testing.T) {
	cases := map[string]any{
		"running": "running", "stage:2": "stage:2",
		"t1": []any{"t1"}, "t1,t2": []any{"t1", "t2"},
	}
	for in, want := range cases {
		got, err := MessageTarget(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %#v %v", in, got, err)
		}
	}
	if _, err := MessageTarget(""); err == nil {
		t.Error("empty target accepted")
	}
}

func TestStartGoalEncoding(t *testing.T) {
	req, err := ReqStartGoal(StartGoalRequest{Goal: "g", Dir: "/r", Delivery: "local", Design: "full", Orchestrator: "codex:gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Marshal(MsgRunTagged(9, req))
	v, _ := Unmarshal(b)
	kind, body, _ := SplitVariant(v)
	f := body.(map[string]any)
	if kind != "RunTagged" || f["id"] != int64(9) {
		t.Fatalf("%#v", v)
	}
	rk, rb, _ := SplitVariant(f["request"])
	r := rb.(map[string]any)
	if rk != "StartGoal" || r["goal"] != "g" || r["dir"] != "/r" || r["yes"] != false || r["trust_project"] != false ||
		r["unconfined_checks"] != false || r["delivery"] != "local" || r["design"] != "full" || r["continue_from"] != nil {
		t.Fatalf("%#v", r)
	}
	if o := r["orchestrator"].(map[string]any); o["runtime"] != "codex" || o["model"] != "gpt-5" {
		t.Fatalf("orchestrator %#v", o)
	}
	for _, bad := range []StartGoalRequest{{Dir: "/r"}, {Goal: "g"}, {Goal: "g", Dir: "/r", Delivery: "x"}, {Goal: "g", Dir: "/r", Orchestrator: "gemini"}} {
		if _, err := ReqStartGoal(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestSocketPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		override string
		env      map[string]string
		goos     string
		want     string
	}{
		{"/x.sock", map[string]string{"ANTHREX_SOCKET": "/y"}, "linux", "/x.sock"},
		{"", map[string]string{"ANTHREX_SOCKET": "/y"}, "linux", "/y"},
		{"", map[string]string{"XDG_RUNTIME_DIR": "/run/user/501"}, "linux", "/run/user/501/anthrex/daemon.sock"},
		{"", map[string]string{"XDG_RUNTIME_DIR": "relative"}, "linux", "/tmp/anthrex-501/daemon.sock"},
		{"", map[string]string{}, "linux", "/tmp/anthrex-501/daemon.sock"},
		{"", map[string]string{"TMPDIR": "/var/folders/ab/T/", "XDG_RUNTIME_DIR": "/run"}, "darwin", "/var/folders/ab/T/anthrex-501/daemon.sock"},
		{"", map[string]string{}, "darwin", "/tmp/anthrex-501/daemon.sock"},
	}
	for _, c := range cases {
		if got := socketPath(c.override, env(c.env), c.goos, 501); got != c.want {
			t.Errorf("%+v: got %s want %s", c, got, c.want)
		}
	}
}
