package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// TestMultiStoreRun drives a shopping run across two "stores": loja-a
// (127.0.0.1, learned through shops.get, own profile, custom checkout
// label) and localhost (unlocked with browser.allow_domain after approval).
func TestMultiStoreRun(t *testing.T) {
	chrome := needChrome(t)
	var mu sync.Mutex
	ordered := false
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>Home %s</title><h1>Loja %s</h1>`, r.Host, r.Host)
	})
	mux.HandleFunc("/cart", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Carrinho</title><h1>Carrinho</h1>
<ul><li>Livro O Principezinho</li><li>7,99 €</li><li>Marcador de livros — 2,99 €</li></ul>
<p>Portes 0,00 €</p><p>Total a pagar 10,98 €</p>
<form method="post" action="/done"><button>Fechar pedido</button></form>`)
	})
	mux.HandleFunc("/done", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ordered = r.Method == http.MethodPost
		mu.Unlock()
		fmt.Fprint(w, `<!doctype html><title>Obrigado</title><h1>Encomenda feita</h1>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	a := "http://127.0.0.1:" + port
	b := "http://localhost:" + port

	shopsGet := kernel.ToolFunc{S: kernel.ToolSpec{Name: HubToolShopsGet, ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ},
		F: func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
			return kernel.ToolOutput(map[string]any{"id": "loja-a", "name": "Loja A", "domains": []string{"127.0.0.1"},
				"checkout_labels": []string{"Fechar pedido"}, "playbook": "# Loja A"})
		}}

	var cartRef int
	llm := &scriptLLM{t: t}
	expect := func(last, want string) {
		t.Helper()
		if !strings.Contains(last, want) {
			t.Errorf("want %q in: %.400s", want, last)
		}
	}
	llm.steps = []func(string) kernel.ChatMessage{
		func(string) kernel.ChatMessage { return call(ToolNavigate, map[string]string{"url": a + "/cart"}) },
		func(last string) kernel.ChatMessage {
			expect(last, "not allowed")
			return call(HubToolShopsGet, map[string]string{"id": "loja-a"})
		},
		func(last string) kernel.ChatMessage {
			expect(last, "newly_allowed")
			return call(ToolNavigate, map[string]string{"url": a + "/"})
		},
		func(last string) kernel.ChatMessage {
			expect(last, `"ok":true`)
			return call(ToolNavigate, map[string]string{"url": b + "/"})
		},
		func(last string) kernel.ChatMessage {
			expect(last, "not allowed")
			return call(ToolAllowDomain, map[string]string{"domain": "localhost", "reason": "second store"})
		},
		func(last string) kernel.ChatMessage {
			expect(last, `"added":["localhost"]`)
			return call(ToolNavigate, map[string]string{"url": b + "/"})
		},
		func(last string) kernel.ChatMessage {
			expect(last, "Home localhost")
			return call(ToolNavigate, map[string]string{"url": a + "/cart"})
		},
		func(last string) kernel.ChatMessage { return call(ToolSnapshot, nil) },
		func(last string) kernel.ChatMessage {
			cartRef = refOf(t, last, "button", "Fechar pedido")
			return call(ToolClick, map[string]any{"ref": cartRef})
		},
		func(last string) kernel.ChatMessage {
			expect(last, `"checkout_click":true`)
			return kernel.ChatMessage{Content: "Encomenda feita na Loja A: 10,98 €."}
		},
	}

	profiles := t.TempDir()
	rt := newRuntime(t, llm, "chrome_path: "+chrome+"\nprofiles_dir: "+profiles+"\n")
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: &nexusv1.RunAssignment{
			RunId: "run_m", TaskId: "tsk_m", Prompt: "Compra o livro",
			Instructions: "# Shopper test",
			Agent:        &nexusv1.Agent{Id: "multi-test", Model: "smart", Tools: []string{"browser", "shops.get"}},
		},
		Workdir: t.TempDir(),
		Tools:   []kernel.Tool{shopsGet},
	})
	if err != nil {
		t.Fatal(err)
	}
	var approvals []*nexusv1.Approval
	c := drainWith(t, run, 2*time.Minute, func(ev *nexusv1.Event) {
		if ap := ev.GetApproval(); ap != nil {
			approvals = append(approvals, ap)
			if err := run.Resolve(context.Background(), &nexusv1.ApprovalResolution{RunId: "run_m", Approval: &nexusv1.Approval{Id: ap.GetId(), State: nexusv1.ApprovalState_APPROVAL_STATE_APPROVED}}); err != nil {
				t.Errorf("resolve: %v", err)
			}
		}
	})
	<-run.(*Run).BrowserClosed()
	last := c.events[len(c.events)-1].GetTaskUpdated().GetTask()
	if last.GetState() != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("final %v: %s\n%s", last.GetState(), last.GetSummary(), strings.Join(llm.log, "\n---\n"))
	}
	if len(approvals) != 2 {
		t.Fatalf("approvals = %d", len(approvals))
	}
	if approvals[0].GetActionClass() != nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL || approvals[0].GetLevel() != nexusv1.TrustLevel_TRUST_LEVEL_APPROVE {
		t.Errorf("allow_domain approval %v %v", approvals[0].GetActionClass(), approvals[0].GetLevel())
	}
	sp := approvals[1]
	p := sp.GetPayload().AsMap()
	store, _ := p["store"].(map[string]any)
	items, _ := p["items"].([]any)
	if sp.GetActionClass() != nexusv1.ActionClass_ACTION_CLASS_SPEND || store["id"] != "loja-a" || p["total"] != "10,98 €" ||
		len(items) != 2 || !strings.HasPrefix(fmt.Sprint(p["screenshot"]), "att_") || p["element"] != "Fechar pedido" {
		t.Errorf("spend payload %v", p)
	}
	if !strings.Contains(sp.GetSummary(), "10,98 €") {
		t.Errorf("summary %q", sp.GetSummary())
	}
	mu.Lock()
	if !ordered {
		t.Error("not ordered")
	}
	mu.Unlock()
	for _, prof := range []string{"loja-a", "multi-test"} {
		if _, err := os.Stat(filepath.Join(profiles, prof)); err != nil {
			t.Errorf("profile %s: %v", prof, err)
		}
	}
}
