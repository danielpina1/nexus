package wire

import (
	"encoding/json"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Tool specs with an explicit MinLevel round-trip; nodes that predate
// min_level see action_class 0 (gated at APPROVE), never the looser class.
func TestToolSpecMinLevel(t *testing.T) {
	specs := []kernel.ToolSpec{
		{Name: "vault.read", ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ},
		{Name: "vault.rename", ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL, MinLevel: nexusv1.TrustLevel_TRUST_LEVEL_APPROVE},
	}
	b, err := EncodeToolSpecs(specs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeToolSpecs(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := range specs {
		if got[i].Name != specs[i].Name || got[i].ActionClass != specs[i].ActionClass || got[i].MinLevel != specs[i].MinLevel {
			t.Fatalf("round trip %d: %+v", i, got[i])
		}
	}
	var old struct {
		Tools []struct {
			Name        string `json:"name"`
			ActionClass int32  `json:"action_class"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	if old.Tools[0].ActionClass != int32(nexusv1.ActionClass_ACTION_CLASS_READ) || old.Tools[1].ActionClass != 0 {
		t.Fatalf("old-node view %+v", old.Tools)
	}
}
