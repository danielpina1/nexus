package approvalgate

import (
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func TestWithMin(t *testing.T) {
	cases := []struct{ level, min, want nexusv1.TrustLevel }{
		{notify, 0, notify},
		{notify, approve, approve},
		{biometric, approve, biometric}, // never loosens
		{auto, nexusv1.TrustLevel(42), auto},
	}
	for _, c := range cases {
		if got := WithMin(c.level, c.min); got != c.want {
			t.Errorf("WithMin(%s, %s) = %s, want %s", c.level, c.min, got, c.want)
		}
	}
}

// A WRITE_INTERNAL tool (NOTIFY by class) with MinLevel APPROVE asks for a
// one-tap approval, not biometric.
func TestCheckHonoursMinLevel(t *testing.T) {
	s := newSink()
	g := newGate(s, nil)
	r := req()
	r.Class, r.MinLevel = nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL, approve
	outc, _ := startCheck(g, r)
	a := expectApproval(t, s)
	if a.Level != approve || a.ActionClass != nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL {
		t.Fatalf("approval level %s class %s", a.Level, a.ActionClass)
	}
	g.Close()
	<-outc
	// An agent policy can still tighten it.
	g = newGate(newSink(), &nexusv1.Agent{Policy: map[string]nexusv1.TrustLevel{"write_internal": biometric}})
	if lvl := WithMin(g.Level(r.Class), r.MinLevel); lvl != biometric {
		t.Fatalf("tightened level = %s", lvl)
	}
}
