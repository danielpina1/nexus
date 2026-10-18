package anthrex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The fixtures are serde_json encodings of proto::RunInfo copied from the
// Anthrex repository (see testdata/README.md). serde's data model is shared
// by serde_json and rmp_serde, so the JSON structure is the MessagePack
// structure: these tests push each fixture through our encoder, back
// through our decoder, and into the typed structs.

func loadFixtures(t *testing.T) map[string][]byte {
	t.Helper()
	files, err := filepath.Glob("testdata/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = b
	}
	return out
}

func TestFixturesMsgpackRoundTrip(t *testing.T) {
	for name, b := range loadFixtures(t) {
		g, err := DecodeJSON(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		mp, err := Marshal(g)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		back, err := Unmarshal(mp)
		if err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if !reflect.DeepEqual(g, back) {
			t.Fatalf("%s: round trip differs", name)
		}
	}
}

func TestFixturesDecodeRunInfo(t *testing.T) {
	for name, b := range loadFixtures(t) {
		if name == "orch_tools.json" {
			continue
		}
		g, err := DecodeJSON(b)
		if err != nil {
			t.Fatal(err)
		}
		// Wrap the RunInfo as the daemon sends it:
		// DaemonMsg::Run(RunReply::Snapshot(RunsSnapshot{…})).
		msg := map[string]any{"Run": map[string]any{"Snapshot": map[string]any{
			"revision": int64(7), "runs": []any{g}, "now": int64(1_700_000_100), "proposals": []any{},
		}}}
		var buf bytes.Buffer
		if err := WriteFrame(&buf, msg); err != nil {
			t.Fatal(err)
		}
		v, err := ReadFrame(&buf)
		if err != nil {
			t.Fatal(err)
		}
		m, err := DecodeDaemonMsg(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m.Kind != "Run" || m.Run.Kind != "Snapshot" || m.Run.Snapshot == nil || m.Run.Snapshot.Revision != 7 {
			t.Fatalf("%s: %+v", name, m)
		}
		r := m.Run.Snapshot.Runs[0]
		var direct RunInfo
		if err := json.Unmarshal(b, &direct); err != nil {
			t.Fatalf("%s: direct json: %v", name, err)
		}
		ja, _ := json.Marshal(r)
		jb, _ := json.Marshal(direct)
		if !bytes.Equal(ja, jb) {
			t.Fatalf("%s: msgpack path and json path disagree", name)
		}
		if r.RunID == "" || r.Goal == "" || r.State == "" || r.BaseBranch == "" || len(r.Tasks) == 0 {
			t.Fatalf("%s: essentials missing: %+v", name, r)
		}
		for _, task := range r.Tasks {
			if task.ID == "" || task.State == "" || task.Title == "" || task.Route.Runtime == "" {
				t.Fatalf("%s: task essentials missing: %+v", name, task)
			}
		}
		raw := g.(map[string]any)
		// Spot-check fields that have a non-trivial wire shape when present.
		if o, ok := raw["orchestrator"].(map[string]any); ok && o != nil {
			if r.Orchestrator == nil || r.Orchestrator.Route.Model != o["route"].(map[string]any)["model"] {
				t.Fatalf("%s: orchestrator not decoded", name)
			}
		}
		if hs, ok := raw["holds"].([]any); ok && len(hs) > 0 {
			h := hs[0].(map[string]any)
			if r.Holds[0].ID != h["id"] || r.Holds[0].Kind.Kind != h["kind"].(map[string]any)["kind"] {
				t.Fatalf("%s: hold kind (internally tagged) not decoded: %+v", name, r.Holds[0])
			}
		}
		if as, ok := raw["actions"].([]any); ok && len(as) > 0 {
			if r.Actions[0].Kind.Kind == "" {
				t.Fatalf("%s: action kind not decoded", name)
			}
		}
		if u, ok := raw["usage"].(map[string]any); ok && u != nil {
			if r.Usage == nil || int64(r.Usage.Total.Input) != u["total"].(map[string]any)["input"] {
				t.Fatalf("%s: usage not decoded", name)
			}
		}
	}
}

// Every fixture key we model must exist in at least one fixture, so a
// renamed wire field shows up here rather than as a silent zero value.
func TestTypedFieldsExistInFixtures(t *testing.T) {
	seenRun, seenTask := map[string]bool{}, map[string]bool{}
	for name, b := range loadFixtures(t) {
		if name == "orch_tools.json" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		for k := range raw {
			seenRun[k] = true
		}
		var tasks []map[string]json.RawMessage
		_ = json.Unmarshal(raw["tasks"], &tasks)
		for _, tk := range tasks {
			for k := range tk {
				seenTask[k] = true
			}
		}
	}
	check := func(typ reflect.Type, seen map[string]bool, optional map[string]bool) {
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			if c := bytes.IndexByte([]byte(tag), ','); c >= 0 {
				tag = tag[:c]
			}
			if tag == "" || optional[tag] {
				continue
			}
			if !seen[tag] {
				t.Errorf("%s.%s: no fixture has %q", typ.Name(), typ.Field(i).Name, tag)
			}
		}
	}
	// Fields left out of every fixture by skip_serializing_if (empty/off).
	check(reflect.TypeOf(RunInfo{}), seenRun, map[string]bool{
		"title": true, "halt_user_only": true, "halted_phase": true, "delivery": true, "chain": true,
		"design": true, "doc_gate": true, "docs": true,
	})
	check(reflect.TypeOf(TaskInfo{}), seenTask, nil)
}
