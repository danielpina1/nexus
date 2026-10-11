package policy

import (
	"context"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

const (
	auto      = nexusv1.TrustLevel_TRUST_LEVEL_AUTO
	notify    = nexusv1.TrustLevel_TRUST_LEVEL_NOTIFY
	approve   = nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
	biometric = nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC
)

func TestDefaults(t *testing.T) {
	e, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		class nexusv1.ActionClass
		want  nexusv1.TrustLevel
	}{
		{nexusv1.ActionClass_ACTION_CLASS_READ, auto},
		{nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL, notify},
		{nexusv1.ActionClass_ACTION_CLASS_COMPUTE, auto},
		{nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL, approve},
		{nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH, approve},
		{nexusv1.ActionClass_ACTION_CLASS_SPEND, biometric},
		{nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE, biometric},
		{nexusv1.ActionClass_ACTION_CLASS_DEVICE_CONTROL, approve},
		{nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED, approve},
		{nexusv1.ActionClass(99), approve},
	}
	for _, c := range cases {
		got, err := e.Level(context.Background(), nil, c.class)
		if err != nil || got != c.want {
			t.Errorf("%s = %s (%v), want %s", c.class, got, err, c.want)
		}
	}
}

func TestAgentCanOnlyTighten(t *testing.T) {
	e, _ := New(Config{})
	agent := &nexusv1.Agent{Policy: map[string]nexusv1.TrustLevel{
		"read":           approve,   // tighten
		"spend":          auto,      // attempt to loosen: ignored
		"WRITE_EXTERNAL": biometric, // tighten
		"substitutions":  approve,   // not an action class: ignored
	}}
	cases := []struct {
		class nexusv1.ActionClass
		want  nexusv1.TrustLevel
	}{
		{nexusv1.ActionClass_ACTION_CLASS_READ, approve},
		{nexusv1.ActionClass_ACTION_CLASS_SPEND, biometric},
		{nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL, biometric},
		{nexusv1.ActionClass_ACTION_CLASS_COMPUTE, auto},
	}
	for _, c := range cases {
		if got, _ := e.Level(context.Background(), agent, c.class); got != c.want {
			t.Errorf("%s = %s, want %s", c.class, got, c.want)
		}
	}
}

func TestConfigOverrides(t *testing.T) {
	h := kerneltest.New(t)
	h.ConfigYAML = "levels:\n  write_internal: auto\n  device-control: notify\n  read: TRUST_LEVEL_NOTIFY\n"
	m := &Module{}
	if err := m.Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	pe, err := kernel.Resolve[kernel.PolicyEngine](h, kernel.SvcPolicy)
	if err != nil {
		t.Fatal(err)
	}
	for class, want := range map[nexusv1.ActionClass]nexusv1.TrustLevel{
		nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL: auto,
		nexusv1.ActionClass_ACTION_CLASS_DEVICE_CONTROL: notify,
		nexusv1.ActionClass_ACTION_CLASS_READ:           notify,
		nexusv1.ActionClass_ACTION_CLASS_SPEND:          biometric,
	} {
		if got, _ := pe.Level(context.Background(), nil, class); got != want {
			t.Errorf("%s = %s, want %s", class, got, want)
		}
	}
}

func TestConfigErrors(t *testing.T) {
	cases := []map[string]string{
		{"teleport": "auto"},
		{"read": "sometimes"},
		{"unspecified": "auto"},
	}
	for _, levels := range cases {
		if _, err := New(Config{Levels: levels}); err == nil {
			t.Errorf("New(%v) succeeded, want error", levels)
		}
	}
}
