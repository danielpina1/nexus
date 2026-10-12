package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/danielpina1/nexus/backend/kernel"
)

func TestClassifier(t *testing.T) {
	c, err := NewClassifier(nil, []string{`fechar pedido`})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		label string
		spend bool
	}{
		{"Finalizar compra", true},
		{"FINALIZAR COMPRA", true},
		{"  Finalizar\n  a compra ", true},
		{"Pagar", true},
		{"Pagar 84,30 €", true},
		{"Confirmar encomenda", true},
		{"Confirmar Pagamento", true},
		{"Comprar agora", true},
		{"Comprar já", true},
		{"Checkout", true},
		{"Proceed to check out", true},
		{"Place order", true},
		{"Place your order", true},
		{"Pay now", true},
		{"Buy now", true},
		{"Concluir encomenda", true},
		{"Efetuar pagamento", true},
		{"Fechar pedido", true}, // extra pattern
		{"Adicionar ao carrinho", false},
		{"Ver carrinho", false},
		{"Carrinho (3)", false},
		{"Continuar a comprar", false},
		{"Comprar", false},
		{"Payment methods", false},
		{"Pagamentos seguros", false},
		{"Pesquisar", false},
		{"", false},
	}
	for _, tt := range tests {
		_, _, got := c.Match(tt.label)
		if got != tt.spend {
			t.Errorf("Match(%q) = %v, want %v", tt.label, got, tt.spend)
		}
	}
	if _, _, ok := c.Match("Adicionar", "Finalizar compra"); !ok {
		t.Error("any label should match")
	}
	if _, err := NewClassifier([]string{"("}, nil); err == nil {
		t.Error("invalid pattern accepted")
	}
	custom, _ := NewClassifier([]string{"only this"}, nil)
	if _, _, ok := custom.Match("Finalizar compra"); ok {
		t.Error("checkout_patterns should replace defaults")
	}
}

func TestFold(t *testing.T) {
	if got := Fold("  Já  PAGÁMOS\tà Ação "); got != "ja pagamos a acao" {
		t.Errorf("Fold = %q", got)
	}
}

func TestSensitive(t *testing.T) {
	tests := []struct {
		typ, ac, name, id, label string
		want                     bool
	}{
		{"password", "", "", "", "", true},
		{"text", "cc-number", "", "", "", true},
		{"text", "current-password", "", "", "", true},
		{"text", "", "cardNumber", "", "", true},
		{"tel", "", "", "card_cvv", "", true},
		{"text", "", "", "", "Security code", true},
		{"search", "", "q", "search", "Pesquisar produtos", false},
		{"text", "", "loyalty", "", "Cartão Continente", false},
		{"email", "email", "email", "", "Email", false},
	}
	for _, tt := range tests {
		if got := SensitiveField(tt.typ, tt.ac, tt.name, tt.id, tt.label) != ""; got != tt.want {
			t.Errorf("SensitiveField(%v) = %v, want %v", tt, got, tt.want)
		}
	}
	for s, want := range map[string]bool{
		"4111 1111 1111 1111": true,
		"4111-1111-1111-1111": true,
		"5500005555555559":    true,
		"4111 1111 1111 1112": false,
		"leite mimosa":        false,
		"912345678":           false,
	} {
		if got := LooksLikeCardNumber(s); got != want {
			t.Errorf("LooksLikeCardNumber(%q) = %v", s, got)
		}
	}
}

func TestDomainPolicy(t *testing.T) {
	p := NewDomainPolicy([]string{"continente.pt", "*.example.com", "https://shop.test:8443/x", ""})
	if !p.Restricted() || len(p.Domains()) != 3 {
		t.Fatalf("domains %v", p.Domains())
	}
	for host, want := range map[string]bool{
		"continente.pt":         true,
		"www.continente.pt":     true,
		"WWW.Continente.PT:443": true,
		"evilcontinente.pt":     false,
		"continente.pt.evil.io": false,
		"a.b.example.com":       true,
		"shop.test":             true,
		"other.test":            false,
	} {
		if got := p.AllowHost(host); got != want {
			t.Errorf("AllowHost(%q) = %v", host, got)
		}
	}
	if err := p.CheckURL("https://www.continente.pt/pesquisa?q=leite"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"https://evil.io/", "file:///etc/passwd", "javascript:alert(1)", "chrome://settings"} {
		if err := p.CheckURL(bad); err == nil {
			t.Errorf("CheckURL(%q) allowed", bad)
		}
	}
	open := NewDomainPolicy(nil)
	if open.Restricted() || !open.AllowHost("anything.io") || open.CheckURL("ftp://x") == nil {
		t.Error("open policy")
	}
}

func TestDomainsFromContext(t *testing.T) {
	s1, _ := structpb.NewStruct(map[string]any{"browser": map[string]any{"allowed_domains": []any{"continente.pt"}}})
	s2, _ := structpb.NewStruct(map[string]any{"browser.allowed_domains": "a.pt, b.pt"})
	s3, _ := structpb.NewStruct(map[string]any{"list": "milk"})
	if d, ok := domainsFromContext(s1); !ok || len(d) != 1 || d[0] != "continente.pt" {
		t.Errorf("s1 %v %v", d, ok)
	}
	if d, ok := domainsFromContext(s2); !ok || len(d) != 2 || d[1] != "b.pt" {
		t.Errorf("s2 %v %v", d, ok)
	}
	if _, ok := domainsFromContext(s3); ok {
		t.Error("s3")
	}
	if _, ok := domainsFromContext(nil); ok {
		t.Error("nil")
	}
}

// snapshotFixture is what js/snapshot.js returns for a small shop page.
const snapshotFixture = `{"url":"http://127.0.0.1:1234/search?q=leite","title":"Pesquisa: leite — Loja",
"scroll_y":0,"scroll_height":1800,"viewport_height":900,"total":5,"truncated":false,
"elements":[
 {"ref":1,"role":"searchbox","name":"Pesquisar produtos","type":"search","value":"leite"},
 {"ref":2,"role":"button","name":"Pesquisar"},
 {"ref":3,"role":"link","name":"Leite Mimosa Sem Lactose 1L","href":"/p/1"},
 {"ref":4,"role":"button","name":"Adicionar ao carrinho","text":"Adicionar ao carrinho 0,99 €","disabled":true},
 {"ref":5,"role":"checkbox","name":"Bio","type":"checkbox","checked":false,"offscreen":true},
 {"ref":6,"role":"combobox","name":"Ordenar","value":"Relevância","options":["Relevância","Preço"]}
],
"text":"Resultados para leite\nLeite Mimosa Sem Lactose 1L 0,99 €"}`

func TestSnapshotFormat(t *testing.T) {
	s, err := ParseSnapshot(snapshotFixture)
	if err != nil {
		t.Fatal(err)
	}
	out := s.Format(0)
	for _, want := range []string{
		"Page: Pesquisa: leite — Loja",
		"URL: http://127.0.0.1:1234/search?q=leite",
		"untrusted",
		`[1] searchbox "Pesquisar produtos" type=search value="leite"`,
		`[3] link "Leite Mimosa Sem Lactose 1L" href=/p/1`,
		`[4] button "Adicionar ao carrinho" text="Adicionar ao carrinho 0,99 €" disabled`,
		`[5] checkbox "Bio" type=checkbox checked=false (offscreen)`,
		`options="Relevância | Preço"`,
		"Visible text:\nResultados para leite",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Size cap: many elements and a long text stay within the budget.
	big := &Snapshot{URL: "u", Title: "t", Text: strings.Repeat("palavra ", 5000)}
	for i := 1; i <= 400; i++ {
		big.Elements = append(big.Elements, Element{Ref: i, Role: "link", Name: strings.Repeat("x", 90)})
	}
	if got := big.Format(12000); len(got) > 12000 || !strings.Contains(got, "more elements not shown") {
		t.Errorf("cap: len %d", len(got))
	}
}

func TestScripts(t *testing.T) {
	// The embedded scripts are function expressions invoked with arguments.
	for name, src := range map[string]string{"snapshot": snapshotJS, "element": elementJS} {
		s := strings.TrimSpace(src)
		if !strings.Contains(s, "(function (") || !strings.HasSuffix(s, "})") {
			t.Errorf("%s.js is not a function expression", name)
		}
	}
	if e := snapshotExpr(10, 20); !strings.HasSuffix(e, ")(10, 20)") || !strings.Contains(e, "data-nexus-ref") {
		t.Error("snapshotExpr")
	}
	e := elementExpr("select", 7, `a "quoted" </script>`)
	arg, _ := json.Marshal(`a "quoted" </script>`)
	if !strings.HasSuffix(e, `)("select", 7, `+string(arg)+`)`) || strings.Contains(e, "</script>") {
		t.Errorf("elementExpr: %s", e[len(e)-60:])
	}
}

func TestFindChrome(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "my-chrome")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindChrome(fake); got != fake {
		t.Errorf("configured: %q", got)
	}
	t.Setenv("CHROME_PATH", fake)
	if got := FindChrome(""); got != fake {
		t.Errorf("env: %q", got)
	}
	pw := filepath.Join(dir, "pw")
	for _, p := range []string{"chromium-1100/chrome-linux/chrome", "chromium-1194/chrome-linux/chrome", "chromium_headless_shell-1194/chrome-linux/headless_shell"} {
		full := filepath.Join(pw, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("x"), 0o755)
	}
	got := playwrightChromes(pw)
	if len(got) != 3 || !strings.Contains(got[0], "chromium-1194") || !strings.Contains(got[2], "headless_shell") {
		t.Errorf("playwright order: %v", got)
	}
}

func TestSelectBrowserTools(t *testing.T) {
	k := &toolkit{}
	all := k.tools()
	if len(all) != 10 {
		t.Fatalf("tools %d", len(all))
	}
	classes := map[string]string{}
	for _, tl := range all {
		classes[tl.Spec().Name] = tl.Spec().ActionClass.String()
		if !json.Valid(tl.Spec().InputSchema) {
			t.Errorf("%s schema invalid", tl.Spec().Name)
		}
	}
	for _, n := range []string{ToolNavigate, ToolSnapshot, ToolScreenshot, ToolExtract, ToolWait, ToolScroll, ToolBack} {
		if classes[n] != "ACTION_CLASS_READ" {
			t.Errorf("%s class %s", n, classes[n])
		}
	}
	for _, n := range []string{ToolClick, ToolType, ToolSelect} {
		if classes[n] != "ACTION_CLASS_COMPUTE" {
			t.Errorf("%s class %s", n, classes[n])
		}
	}
	if got := selectBrowserTools(all, []string{"browser", "ask_user"}); len(got) != 10 {
		t.Error("browser → all")
	}
	if got := selectBrowserTools(all, []string{"ask_user"}); len(got) != 10 {
		t.Error("none named → all")
	}
	if got := selectBrowserTools(all, []string{"browser.navigate", "browser.snapshot"}); len(got) != 2 {
		t.Error("named subset")
	}
	hub := []kernel.Tool{kernel.ToolFunc{S: kernel.ToolSpec{Name: "browser"}}, kernel.ToolFunc{S: kernel.ToolSpec{Name: "ask_user"}}}
	if got := dropBrowserNames(hub); len(got) != 1 || got[0].Spec().Name != "ask_user" {
		t.Error("dropBrowserNames")
	}
}

func TestInstructionsAndProfile(t *testing.T) {
	s := Instructions("# Grocery\nBuy things.", NewDomainPolicy([]string{"continente.pt"}))
	for _, want := range []string{"# Grocery", "untrusted data", "approve", "ask_user", "continente.pt", "Never type passwords"} {
		if !strings.Contains(s, want) {
			t.Errorf("instructions missing %q", want)
		}
	}
	if got := ProfileDir("/p", "../grocery continente"); got != "/p/_grocery_continente" {
		t.Errorf("profile %q", got)
	}
	if got := ProfileDir("/p", ""); got != "/p/default" {
		t.Errorf("profile %q", got)
	}
}
