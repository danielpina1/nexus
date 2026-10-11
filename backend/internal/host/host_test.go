package host

import (
	"context"
	"testing"

	"github.com/danielpina1/nexus/backend/kernel"
)

type testMod struct {
	kernel.BaseModule
	info  kernel.ModuleInfo
	order *[]string
}

func (m *testMod) Info() kernel.ModuleInfo { return m.info }
func (m *testMod) Init(_ context.Context, h kernel.Host) error {
	*m.order = append(*m.order, m.info.Name)
	h.Provide("svc."+m.info.Name, m.info.Name)
	return nil
}

var order []string

func init() {
	kernel.RegisterModule(func() kernel.Module {
		return &testMod{info: kernel.ModuleInfo{Name: "t.b", Side: "test", Requires: []string{"t.a"}, DefaultEnabled: true}, order: &order}
	})
	kernel.RegisterModule(func() kernel.Module {
		return &testMod{info: kernel.ModuleInfo{Name: "t.a", Side: "test", DefaultEnabled: true}, order: &order}
	})
	kernel.RegisterModule(func() kernel.Module {
		return &testMod{info: kernel.ModuleInfo{Name: "t.c", Side: "test"}, order: &order}
	})
}

func TestDependencyOrderAndConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte("data_dir: " + t.TempDir() + "\nmodules:\n  enable: [t.c]\n"))
	if err != nil {
		t.Fatal(err)
	}
	order = nil
	p := New(Options{Side: "test", Config: cfg})
	if err := p.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := p.ModuleNames()
	want := []string{"t.a", "t.b", "t.c"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if v, ok := p.Lookup("svc.t.b"); !ok || v != "t.b" {
		t.Fatalf("service not provided: %v", v)
	}
}

func TestMissingDependency(t *testing.T) {
	cfg, _ := ParseConfig([]byte("data_dir: " + t.TempDir() + "\nmodules:\n  disable: [t.a]\n"))
	p := New(Options{Side: "test", Config: cfg})
	if err := p.Init(context.Background()); err == nil {
		t.Fatal("expected error for missing dependency")
	}
}
