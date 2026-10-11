package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "nexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrate(t *testing.T) {
	tests := []struct {
		name    string
		runs    [][]string // successive Migrate calls for module "m"
		applied int
		wantErr bool
	}{
		{
			name:    "single",
			runs:    [][]string{{`CREATE TABLE a (x INTEGER)`}},
			applied: 1,
		},
		{
			name: "idempotent and appendable",
			runs: [][]string{
				{`CREATE TABLE a (x INTEGER)`},
				{`CREATE TABLE a (x INTEGER)`, `ALTER TABLE a ADD COLUMN y TEXT`},
				{`CREATE TABLE a (x INTEGER)`, `ALTER TABLE a ADD COLUMN y TEXT`},
			},
			applied: 2,
		},
		{
			name:    "failure rolls back the failing statement only",
			runs:    [][]string{{`CREATE TABLE a (x INTEGER)`, `NOT SQL`}},
			applied: 1,
			wantErr: true,
		},
		{
			name:    "empty",
			runs:    [][]string{nil},
			applied: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTemp(t)
			var err error
			for _, stmts := range tt.runs {
				err = s.Migrate("m", stmts)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			n, err := s.Applied("m")
			if err != nil {
				t.Fatal(err)
			}
			if n != tt.applied {
				t.Fatalf("applied = %d, want %d", n, tt.applied)
			}
		})
	}
}

func TestMigrateModulesAreIndependent(t *testing.T) {
	s := openTemp(t)
	if err := s.Migrate("a", []string{`CREATE TABLE ta (x)`}); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate("b", []string{`CREATE TABLE tb (x)`, `CREATE TABLE tb2 (x)`}); err != nil {
		t.Fatal(err)
	}
	for mod, want := range map[string]int{"a": 1, "b": 2, "c": 0} {
		if n, _ := s.Applied(mod); n != want {
			t.Errorf("%s applied = %d, want %d", mod, n, want)
		}
	}
	if err := s.Migrate("", nil); err == nil {
		t.Error("empty module name accepted")
	}
}

func TestMigrationsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nexus.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate("m", []string{`CREATE TABLE a (x)`}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Re-running would fail ("table a already exists") if not tracked.
	if err := s.Migrate("m", []string{`CREATE TABLE a (x)`}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMemory(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate("m", []string{`CREATE TABLE a (x)`}); err != nil {
		t.Fatal(err)
	}
}

func TestModule(t *testing.T) {
	h := kerneltest.New(t)
	m := &Module{}
	if info := m.Info(); info.Name != "store" || info.Side != kernel.SideHub || !info.DefaultEnabled {
		t.Fatalf("bad info %+v", info)
	}
	ctx := context.Background()
	if err := m.Init(ctx, h); err != nil {
		t.Fatal(err)
	}
	st, err := kernel.Resolve[kernel.Store](h, kernel.SvcStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Ping(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.Dir, "nexus.db")); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Ping(); err == nil {
		t.Fatal("db still open after Stop")
	}
}

func TestModuleConfigPath(t *testing.T) {
	h := kerneltest.New(t)
	path := filepath.Join(t.TempDir(), "custom", "x.db")
	h.ConfigYAML = "path: " + path
	m := &Module{}
	if err := m.Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
