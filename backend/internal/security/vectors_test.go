package security

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/danielpina1/nexus/backend/internal/security/approval"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
)

// TestCrossLanguageVectors prints/records golden values that client
// implementations (iOS NexusCore, web) must reproduce byte for byte.
// NEXUS_WRITE_VECTORS=<path> writes them as JSON.
func TestCrossLanguageVectors(t *testing.T) {
	payloadJSON := `{"total":84.3,"currency":"EUR","items":[{"name":"Leite Mimosa","qty":6,"price":0.89},{"name":"Pão","qty":1,"price":1e-7}],"slot":"Sat 9–11","big":1e21,"neg":-0.5,"esc":"a\"b\\c\n\u0001/é"}`
	canon, err := approval.CanonicalJSON(json.RawMessage(payloadJSON))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := approval.ActionHash(json.RawMessage(payloadJSON))
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{7}, 32)
	auth := append([]byte{4}, bytes.Repeat([]byte{1}, 64)...)
	appr := append([]byte{4}, bytes.Repeat([]byte{2}, 64)...)
	enc := bytes.Repeat([]byte{3}, 32)
	hub := append([]byte{4}, bytes.Repeat([]byte{5}, 64)...)
	proof, err := pairing.Proof(secret, auth, appr, enc)
	if err != nil {
		t.Fatal(err)
	}
	v := map[string]string{
		"payload_json":       payloadJSON,
		"canonical_json":     string(canon),
		"action_sha256_hex":  hex.EncodeToString(hash),
		"pairing_secret_hex": hex.EncodeToString(secret),
		"auth_pub_hex":       hex.EncodeToString(auth),
		"approval_pub_hex":   hex.EncodeToString(appr),
		"encryption_pub_hex": hex.EncodeToString(enc),
		"hub_pub_hex":        hex.EncodeToString(hub),
		"pairing_proof_hex":  hex.EncodeToString(proof),
		"safety_code":        pairing.SafetyCode(hub, auth, appr, enc),
	}
	if p := os.Getenv("NEXUS_WRITE_VECTORS"); p != "" {
		b, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if v["safety_code"] == "" || len(hash) != 32 {
		t.Fatal("bad vectors")
	}
}
