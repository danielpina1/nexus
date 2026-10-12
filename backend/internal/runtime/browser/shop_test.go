package browser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func TestDomainPolicyDynamic(t *testing.T) {
	open := NewDomainPolicy(nil)
	if open.Restricted() || !open.AllowHost("any.example") {
		t.Fatal("empty policy must be unrestricted")
	}
	if added, _ := open.Add("worten.pt"); added != nil || open.Restricted() {
		t.Fatal("Add must not restrict an unrestricted policy")
	}
	var zero DomainPolicy
	if zero.Restricted() || zero.CheckURL("https://x.pt/") != nil || zero.Domains() != nil {
		t.Fatal("zero policy")
	}
	if _, err := zero.Add("x.pt"); err != nil {
		t.Fatal(err)
	}

	p := NewStrictDomainPolicy(nil)
	if !p.Restricted() || p.AllowHost("worten.pt") {
		t.Fatal("strict empty policy must block everything")
	}
	if err := p.CheckURL("https://www.worten.pt/"); err == nil || !strings.Contains(err.Error(), "none yet") || !strings.Contains(err.Error(), "browser.allow_domain") {
		t.Fatalf("err = %v", err)
	}
	copyOf := p // copies share the list
	added, err := copyOf.Add("https://www.worten.pt/", "*.fnac.pt", "www.worten.pt", "pt", "com.pt", "bad_domain!.pt")
	if strings.Join(added, ",") != "www.worten.pt,fnac.pt" {
		t.Fatalf("added = %v", added)
	}
	if err == nil || !strings.Contains(err.Error(), `"pt" is not`) || !strings.Contains(err.Error(), "public suffix") || !strings.Contains(err.Error(), "not a valid domain") {
		t.Fatalf("err = %v", err)
	}
	if !p.AllowHost("www.worten.pt") || p.AllowHost("worten.pt") || !p.AllowHost("m.www.worten.pt") || !p.AllowHost("loja.fnac.pt:443") || p.AllowHost("evil.pt") {
		t.Fatalf("allow = %v", p.Domains())
	}
	for _, ok := range []string{"localhost", "127.0.0.1", "loja.example.co.uk"} {
		if err := ValidAllowDomain(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "com", "co.uk", "*.com.pt", "a b.pt"} {
		if ValidAllowDomain(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestShopRegistry(t *testing.T) {
	p := NewStrictDomainPolicy([]string{"kuantokusta.pt"})
	reg := newShopRegistry(p, true, nil)
	added, err := reg.register(ShopInfo{ID: "Worten", Name: "Worten", Domains: []string{"worten.pt"}, CheckoutLabels: []string{"Confirmar e pagar", "/pagar (ja|agora)/", "("}})
	if err != nil || len(added) != 1 || added[0] != "worten.pt" {
		t.Fatalf("register: %v %v", added, err)
	}
	if _, err := reg.register(ShopInfo{ID: "ikea", Domains: []string{"www.ikea.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.register(ShopInfo{}); err == nil {
		t.Fatal("shop without id accepted")
	}
	if s, ok := reg.forHost("loja.worten.pt"); !ok || s.ID != "worten" {
		t.Fatalf("forHost = %v %v", s, ok)
	}
	if _, ok := reg.forHost("ikea.com"); ok {
		t.Fatal("parent domain must not match a subdomain entry")
	}
	if _, ok := reg.forHost("kuantokusta.pt"); ok {
		t.Fatal("plain allowed domain is not a shop")
	}
	for label, want := range map[string]bool{
		"CONFIRMAR E PAGAR":         true,
		"Confirmar e pagar 12,99 €": true,
		"Pagar já":                  true,
		"Confirmar":                 false,
		"Reconfirmar e pagaremos":   false,
	} {
		if _, _, ok := reg.match(label); ok != want {
			t.Errorf("match(%q) = %v", label, ok)
		}
	}
	if strings.Join(reg.ids(), ",") != "ikea,worten" {
		t.Fatalf("ids = %v", reg.ids())
	}
	// Not trusted: domains are not added.
	p2 := NewStrictDomainPolicy(nil)
	reg2 := newShopRegistry(p2, false, nil)
	if added, _ := reg2.register(ShopInfo{ID: "fnac", Domains: []string{"fnac.pt"}}); added != nil || p2.AllowHost("fnac.pt") {
		t.Fatal("untrusted shop domains added")
	}
	if _, ok := reg2.forHost("fnac.pt"); !ok {
		t.Fatal("untrusted shop still maps to its profile")
	}

	// The toolkit consults store labels after the defaults.
	cls, _ := NewClassifier(nil, nil)
	k := &toolkit{cls: cls, shops: reg}
	if _, _, ok := k.matchSpend("Confirmar e pagar"); !ok {
		t.Error("shop label not matched")
	}
	if _, _, ok := (&toolkit{cls: cls}).matchSpend("Finalizar compra"); !ok {
		t.Error("defaults without shops")
	}
}

func TestWrapShopsGet(t *testing.T) {
	p := NewStrictDomainPolicy(nil)
	reg := newShopRegistry(p, true, nil)
	inner := kernel.ToolFunc{S: kernel.ToolSpec{Name: HubToolShopsGet, ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ},
		F: func(_ context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
			var in struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(c.Input, &in)
			if in.ID == "none" {
				return kernel.ToolOutput(map[string]any{"error": "x"})
			}
			return kernel.ToolOutput(map[string]any{"id": in.ID, "name": "Fnac", "domains": []string{"fnac.pt"}, "checkout_labels": []string{"Pagar encomenda"}, "playbook": "# Fnac"})
		}}
	w := wrapShopsGet(inner, reg)
	if w.Spec().Name != HubToolShopsGet || w.Spec().ActionClass != nexusv1.ActionClass_ACTION_CLASS_READ {
		t.Fatal("spec changed")
	}
	res, err := w.Invoke(context.Background(), kernel.ToolCall{Input: json.RawMessage(`{"id":"fnac"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(res.Output, &out)
	b, _ := out["browser"].(map[string]any)
	if out["playbook"] != "# Fnac" || b["shop"] != "fnac" || b["newly_allowed"].([]any)[0] != "fnac.pt" {
		t.Fatalf("out = %v", out)
	}
	if !p.AllowHost("www.fnac.pt") {
		t.Fatal("domain not added")
	}
	if _, _, ok := reg.match("Pagar encomenda"); !ok {
		t.Fatal("label not registered")
	}
	res, _ = w.Invoke(context.Background(), kernel.ToolCall{Input: json.RawMessage(`{"id":"none"}`)})
	if strings.Contains(string(res.Output), "browser") {
		t.Fatalf("non-shop result changed: %s", res.Output)
	}
	if s, ok := parseShopResult(json.RawMessage(`{"shop":{"id":"x","domains":["x.pt"]}}`)); !ok || s.ID != "x" {
		t.Fatal("wrapped form")
	}
}

func TestAllowDomainTool(t *testing.T) {
	p := NewStrictDomainPolicy(nil)
	tool := allowDomainTool(p, nil)
	if tool.Spec().Name != ToolAllowDomain || tool.Spec().ActionClass != nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL || !json.Valid(tool.Spec().InputSchema) {
		t.Fatalf("spec %+v", tool.Spec())
	}
	inv := func(in string) (string, error) {
		r, err := tool.Invoke(context.Background(), kernel.ToolCall{Input: json.RawMessage(in)})
		return string(r.Output), err
	}
	if _, err := inv(`{"domain":"example.pt"}`); err == nil {
		t.Error("reason required")
	}
	if _, err := inv(`{"domain":"com.pt","reason":"x"}`); err == nil {
		t.Error("public suffix accepted")
	}
	if out, err := inv(`{"domain":"https://loja.example.pt/x","reason":"store from the comparison"}`); err != nil || !strings.Contains(out, "loja.example.pt") {
		t.Fatalf("%s %v", out, err)
	}
	if !p.AllowHost("loja.example.pt") {
		t.Fatal("not added")
	}
	// Only offered to restricted runs.
	k := &toolkit{domains: p}
	if n := len(k.tools()); n != 11 {
		t.Fatalf("restricted toolkit tools = %d", n)
	}
	if got := selectBrowserTools(k.tools(), []string{"browser"}); len(got) != 11 {
		t.Fatal("browser → all incl. allow_domain")
	}
}

func TestShopContext(t *testing.T) {
	c, _ := structpb.NewStruct(map[string]any{
		"browser": map[string]any{"profile": "continente", "restrict_domains": true},
		"shops":   []any{"Worten", map[string]any{"id": "fnac", "domains": []any{"fnac.pt"}, "checkout_labels": []any{"Pagar"}}, map[string]any{"id": "nodomain"}, "worten"},
		"shop":    "ikea",
	})
	if profileFromContext(c) != "continente" {
		t.Error("profile")
	}
	if v, set := restrictFromContext(c); !v || !set {
		t.Error("restrict")
	}
	ids, infos := shopsFromContext(c)
	if strings.Join(ids, ",") != "worten,fnac,nodomain,ikea" || len(infos) != 1 || infos[0].Domains[0] != "fnac.pt" {
		t.Fatalf("ids %v infos %v", ids, infos)
	}
	flat, _ := structpb.NewStruct(map[string]any{"browser.profile": "x", "shops": "a, b"})
	if profileFromContext(flat) != "x" {
		t.Error("flat profile")
	}
	if ids, _ := shopsFromContext(flat); strings.Join(ids, ",") != "a,b" {
		t.Errorf("csv shops %v", ids)
	}
	if _, set := restrictFromContext(flat); set {
		t.Error("restrict set")
	}
	if ids, infos := shopsFromContext(nil); ids != nil || infos != nil {
		t.Error("nil")
	}
	g := ShopGuidance([]string{"worten"}, true, NewStrictDomainPolicy(nil))
	for _, want := range []string{"shops.get", "worten", "browser.allow_domain"} {
		if !strings.Contains(g, want) {
			t.Errorf("guidance missing %q", want)
		}
	}
}

func TestCartSummary(t *testing.T) {
	text := `O seu carrinho
Auscultadores Sony WH-1000XM5 Preto
329,99 €
Cabo USB-C 1m — 9,99 €
Portes de envio 3,99 €
Desconto -10,00 €
Subtotal 339,98 €
Total a pagar
333,97 €
Pagar`
	total, items := CartSummary(text)
	if total != "333,97 €" {
		t.Errorf("total = %q", total)
	}
	if len(items) != 2 || items[0] != "Auscultadores Sony WH-1000XM5 Preto — 329,99 €" || !strings.HasPrefix(items[1], "Cabo USB-C") {
		t.Errorf("items = %q", items)
	}
	total, items = CartSummary("Order total: €1,234.50\nWidget €12.00")
	if total != "€1,234" && total != "€1,234.50" {
		t.Logf("english total = %q (best effort)", total)
	}
	if len(items) != 1 {
		t.Errorf("english items %q", items)
	}
	if total, items := CartSummary(""); total != "" || items != nil {
		t.Error("empty")
	}
}

func TestSessionPoolProfiles(t *testing.T) {
	reg := newShopRegistry(NewStrictDomainPolicy(nil), true, nil)
	_, _ = reg.register(ShopInfo{ID: "worten", Domains: []string{"worten.pt"}})
	_, _ = reg.register(ShopInfo{ID: "amazon-es", Domains: []string{"amazon.es"}})
	overrides := map[string]string{"amazon-es": "amazon"}
	var made []string
	pool := newSessionPool("shop", func(p string) *session {
		made = append(made, p)
		return newSession(sessionConfig{ProfileDir: "/profiles/" + p})
	}, func(host string) string {
		s, ok := reg.forHost(host)
		if !ok {
			return ""
		}
		if o := overrides[s.ID]; o != "" {
			return o
		}
		return s.ID
	})
	if s := pool.current(); s.cfg.ProfileDir != "/profiles/shop" {
		t.Fatalf("default %s", s.cfg.ProfileDir)
	}
	if s, p := pool.forHost("www.worten.pt"); p != "worten" || pool.current() != s {
		t.Fatalf("worten → %s", p)
	}
	if _, p := pool.forHost("www.amazon.es"); p != "amazon" {
		t.Fatalf("amazon → %s", p)
	}
	if _, p := pool.forHost("other.pt"); p != "shop" {
		t.Fatalf("other → %s", p)
	}
	pool.forHost("worten.pt")
	if strings.Join(made, ",") != "shop,worten,amazon" || len(pool.profiles()) != 3 {
		t.Fatalf("made %v", made)
	}
	pool.Close()
	<-pool.done
	pool.Kill() // idempotent
	if s, _ := pool.forHost("x.pt"); s.closed != true {
		t.Fatal("session after close must be closed")
	}
}
