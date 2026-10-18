package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielpina1/nexus/backend/internal/router"
	"github.com/danielpina1/nexus/backend/internal/router/learn"
	"github.com/danielpina1/nexus/backend/internal/store"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func TestRouterEvalCommand(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nexus.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	h := kerneltest.New(t)
	if _, err := router.New(h, st, router.Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := learn.New(h, st, learn.Config{EmbedModel: "hash"}); err != nil {
		t.Fatal(err)
	}
	inputs := map[string][]string{
		"researcher":         {"find out the state of solid state batteries", "research creatine evidence", "look into heat pumps efficiency"},
		"grocery-continente": {"buy milk eggs and bread", "order the weekly groceries", "we are out of coffee"},
	}
	i := 0
	for agent, list := range inputs {
		for _, in := range list {
			id := fmt.Sprintf("rtd_%d", i)
			i++
			if _, err := st.DB().Exec(`INSERT INTO router_decisions (id, created_at, input, stage, kind, agent_id, confidence, task_id) VALUES (?, ?, ?, 'keyword', 3, ?, 0.5, ?)`,
				id, i, in, agent, "tsk_"+id); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB().Exec(`INSERT INTO router_learn_outcomes (decision_id, created_at, state) VALUES (?, ?, 'succeeded')`, id, i); err != nil {
				t.Fatal(err)
			}
		}
	}
	st.Close()
	vault, _ := filepath.Abs("../../../vault-template")
	cfg := filepath.Join(dir, "nexus.yaml")
	if err := os.WriteFile(cfg, []byte("store:\n  path: "+dbPath+"\nvault:\n  path: "+vault+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	err = cmdRouter([]string{"eval", "-config", cfg, "-hash"})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "tested 6 (leave-one-out)") || !strings.Contains(string(out), "accuracy") {
		t.Fatalf("output:\n%s", out)
	}
}
