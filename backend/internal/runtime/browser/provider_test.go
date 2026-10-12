package browser

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/mcp"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func providerOf(t *testing.T, h *kerneltest.Host) kernel.RunToolProvider {
	t.Helper()
	ps := kernel.RunToolProvidersOf(h).Providers()
	if len(ps) != 1 || ps[0].Name() != "browser" {
		t.Fatalf("providers %v", ps)
	}
	return ps[0]
}

func newProviderHost(t *testing.T, yaml string) *kerneltest.Host {
	t.Helper()
	h := kerneltest.New(t)
	h.SideValue = kernel.SideNode
	h.ConfigYAML = yaml
	if err := (&Module{}).Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestProviderSelection(t *testing.T) {
	h := newProviderHost(t, "chrome_path: /bin/true\n")
	p := providerOf(t, h)
	shopsGet := kernel.ToolFunc{S: kernel.ToolSpec{Name: HubToolShopsGet}, F: func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
		return kernel.ToolOutput(map[string]any{"id": "continente", "domains": []string{"continente.pt"}})
	}}
	spec := func(runtime string, tools ...string) kernel.RunSpec {
		return kernel.RunSpec{Assignment: &nexusv1.RunAssignment{RunId: "r", TaskId: "t",
			Agent: &nexusv1.Agent{Id: "shop", Runtime: runtime, Tools: tools}},
			Workdir: t.TempDir(), Tools: []kernel.Tool{shopsGet}, Gate: kernel.NewRunGate()}
	}
	for _, tc := range []struct {
		name  string
		spec  kernel.RunSpec
		tools []string
	}{
		{"browser runtime builds its own", spec("browser", "browser"), nil},
		{"agent without browser", spec("claude-code", "memory.search"), nil},
		{"cli shopping agent", spec("claude-code", "browser", "shops.get"), []string{ToolNavigate, ToolAllowDomain, HubToolShopsGet}},
		{"single tool", spec("codex", "browser.navigate"), []string{ToolNavigate}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, err := p.RunTools(context.Background(), tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			if set.Cleanup != nil {
				defer set.Cleanup()
			}
			names := map[string]bool{}
			for _, tl := range set.Tools {
				names[tl.Spec().Name] = true
			}
			if len(tc.tools) == 0 {
				if len(set.Tools) != 0 {
					t.Fatalf("tools %v", names)
				}
				return
			}
			for _, n := range tc.tools {
				if !names[n] {
					t.Errorf("missing %s in %v", n, names)
				}
			}
			if !strings.Contains(set.Instructions, "## Using the browser") {
				t.Errorf("instructions %q", set.Instructions)
			}
			if names[HubToolShopsGet] {
				// The wrapper registers the store: its domain becomes allowed.
				for _, tl := range set.Tools {
					if tl.Spec().Name == HubToolShopsGet {
						res, err := tl.Invoke(context.Background(), kernel.ToolCall{})
						if err != nil || !strings.Contains(string(res.Output), `"newly_allowed":["continente.pt"]`) {
							t.Errorf("shops.get wrapper %s %v", res.Output, err)
						}
					}
				}
			}
		})
	}
	// No Chrome: an error, so the node logs why browser tools are missing.
	h2 := newProviderHost(t, "chrome_path: /nonexistent/chrome\n")
	if _, err := providerOf(t, h2).RunTools(context.Background(), spec("claude-code", "browser")); err == nil && FindChrome("/nonexistent/chrome") == "" {
		t.Fatal("expected an error without Chrome")
	}
}

// TestProviderCheckoutOverMCP drives the browser provider's tools through a
// per-run Nexus MCP bridge, as Claude Code / Codex would: navigation works,
// and the checkout click still needs a SPEND (biometric) approval from the
// run's gate — denied: no order; approved: the order is placed.
func TestProviderCheckoutOverMCP(t *testing.T) {
	chrome := needChrome(t)
	shop := &fakeShop{cart: []string{products["1"]}}
	srv := httptest.NewServer(shop.handler())
	defer srv.Close()

	h := newProviderHost(t, "chrome_path: "+chrome+"\nprofiles_dir: "+t.TempDir()+"\n")
	p := providerOf(t, h)
	events := make(chan *nexusv1.Event, 64)
	emit := func(ev *nexusv1.Event) { events <- ev }
	agent := &nexusv1.Agent{Id: "shop-cli", Name: "Shopper", Runtime: "claude-code", Tools: []string{"browser"}}
	ctxStruct, _ := structpb.NewStruct(map[string]any{"browser": map[string]any{"allowed_domains": []any{"127.0.0.1"}}})
	spec := kernel.RunSpec{
		Assignment: &nexusv1.RunAssignment{RunId: "run_1", TaskId: "tsk_1", Agent: agent, Context: ctxStruct},
		Workdir:    t.TempDir(), Gate: kernel.NewRunGate(), Emit: emit,
	}
	set, err := p.RunTools(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Cleanup()

	// What the claude-code runtime does at Start: one gate for the run,
	// bound to RunSpec.Gate and used by the MCP bridge.
	gate := approvalgate.New(approvalgate.Config{TaskID: "tsk_1", RunID: "run_1", Agent: agent, Emit: emit, Log: discardLogger()})
	defer gate.Close()
	agentloop.BindGate(spec.Gate, gate)
	bridge, err := mcp.NewBridge(mcp.RunConfig{Tools: set.Tools, Gate: gate, Permission: true, TaskID: "tsk_1", RunID: "run_1", AgentID: agent.Id})
	if err != nil {
		t.Fatal(err)
	}
	ep, err := mcp.Listen(bridge.Server)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	c := mcp.NewClient(ep.URL())
	ctx := context.Background()
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	callText := func(name string, args any) (string, bool) {
		t.Helper()
		r, err := c.CallTool(ctx, name, args)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var b strings.Builder
		for _, x := range r.Content {
			b.WriteString(x.Text)
		}
		return b.String(), r.IsError
	}
	if out, isErr := callText("browser_navigate", map[string]string{"url": srv.URL + "/cart"}); isErr {
		t.Fatalf("navigate: %s", out)
	}
	if out, isErr := callText("browser_navigate", map[string]string{"url": "http://evil.invalid/"}); !isErr || !strings.Contains(out, "not allowed") {
		t.Fatalf("navigate outside allow-list: %s", out)
	}
	snap, _ := callText("browser_snapshot", map[string]any{})
	ref := refOf(t, snap, "button", "Finalizar compra")

	for i, decision := range []nexusv1.ApprovalState{nexusv1.ApprovalState_APPROVAL_STATE_DENIED, nexusv1.ApprovalState_APPROVAL_STATE_APPROVED} {
		type result struct {
			text  string
			isErr bool
		}
		done := make(chan result, 1)
		go func() {
			out, isErr := callText("browser_click", map[string]any{"ref": ref})
			done <- result{out, isErr}
		}()
		var apr *nexusv1.Approval
		var sawShot bool
		for apr == nil {
			select {
			case ev := <-events:
				if ev.GetArtifact() != nil {
					sawShot = true
				}
				apr = ev.GetApproval()
			case <-time.After(60 * time.Second):
				t.Fatal("no approval for the checkout click")
			}
		}
		if apr.ActionClass != nexusv1.ActionClass_ACTION_CLASS_SPEND || apr.Level != nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC || !sawShot {
			t.Fatalf("approval class=%v level=%v screenshot=%v", apr.ActionClass, apr.Level, sawShot)
		}
		if el := apr.GetPayload().AsMap()["element"]; el != "Finalizar compra" {
			t.Fatalf("payload %v", apr.GetPayload().AsMap())
		}
		if err := gate.Resolve(ctx, &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: apr.Id, State: decision}}); err != nil {
			t.Fatal(err)
		}
		r := <-done
		shop.mu.Lock()
		ordered := shop.ordered
		shop.mu.Unlock()
		if i == 0 && (!r.isErr || !strings.Contains(r.text, "NOT DONE (spend)") || ordered) {
			t.Fatalf("denied click: %s ordered=%v", r.text, ordered)
		}
		if i == 1 {
			var st struct {
				Checkout bool `json:"checkout_click"`
			}
			_ = json.Unmarshal([]byte(r.text), &st)
			if r.isErr || !st.Checkout || !ordered {
				t.Fatalf("approved click: %s ordered=%v", r.text, ordered)
			}
		}
	}
}

func TestProviderWithoutGateRefusesCheckout(t *testing.T) {
	k := &toolkit{gate: (*kernel.RunGate)(nil)}
	err := k.approveSpendLocked(context.Background(), nil, 1, "Pagar", "pagar", "click")
	if err == nil || !strings.Contains(err.Error(), "no approval channel") {
		t.Fatalf("err %v", err)
	}
}
