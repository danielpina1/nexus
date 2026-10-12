package webtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func TestModuleTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			fmt.Fprintf(w, `{"results":[{"url":"http://%s/article","title":"Heat pumps","content":"about heat pumps"}]}`, r.Host)
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: *\nDisallow: /private\n")
		case "/private":
			fmt.Fprint(w, "<p>private</p>")
		default:
			fmt.Fprint(w, `<html><head><title>Heat pumps explained</title></head><body><article>
<h2>Efficiency</h2><p>A heat pump moves heat instead of generating it, so its efficiency (COP) is usually between 3 and 5.</p>
<h2>Noise</h2><p>Outdoor units produce around 40 to 60 dB, comparable to a quiet conversation.</p>
<h2>Cost</h2><p>Installation in Portugal costs between 6000 and 12000 euros depending on the house.</p>
</article></body></html>`)
		}
	}))
	defer srv.Close()

	for _, side := range []kernel.Side{kernel.SideNode, kernel.SideHub} {
		h := kerneltest.New(t)
		h.SideValue = side
		h.ConfigYAML = fmt.Sprintf("search:\n  provider: searxng\n  searxng: { base_url: %q }\nfetch:\n  allow_private: true\n  respect_robots: true\n", srv.URL)
		m := &Module{side: side}
		if m.Info().Side != side || !m.Info().DefaultEnabled {
			t.Fatalf("info = %+v", m.Info())
		}
		ctx := context.Background()
		if err := m.Init(ctx, h); err != nil {
			t.Fatal(err)
		}
		if err := m.Start(ctx); err != nil {
			t.Fatal(err)
		}
		specs := h.Tools().List()
		if len(specs) != 3 || specs[0].Name != ToolExtract || specs[1].Name != ToolFetch || specs[2].Name != ToolSearch {
			t.Fatalf("specs = %+v", specs)
		}
		for _, s := range specs {
			if s.ActionClass != nexusv1.ActionClass_ACTION_CLASS_READ || !json.Valid(s.InputSchema) {
				t.Errorf("%s: class %v / schema invalid", s.Name, s.ActionClass)
			}
		}
		call := func(name, in string) (map[string]any, error) {
			tool, _ := h.Tools().Get(name)
			res, err := tool.Invoke(ctx, kernel.ToolCall{Input: json.RawMessage(in)})
			if err != nil {
				return nil, err
			}
			var out map[string]any
			return out, json.Unmarshal(res.Output, &out)
		}
		out, err := call(ToolSearch, `{"query":"heat pumps","count":3}`)
		if err != nil {
			t.Fatal(err)
		}
		results := out["results"].([]any)
		if out["provider"] != "searxng" || len(results) != 1 {
			t.Fatalf("search = %v", out)
		}
		articleURL := results[0].(map[string]any)["url"].(string)
		out, err = call(ToolFetch, fmt.Sprintf(`{"url":%q}`, articleURL))
		if err != nil {
			t.Fatal(err)
		}
		if out["title"] != "Heat pumps explained" || !strings.Contains(out["text"].(string), "## Noise") {
			t.Errorf("fetch = %v", out)
		}
		out, err = call(ToolExtract, fmt.Sprintf(`{"url":%q,"question":"how much does installation cost in euros?","max_passages":1,"passage_chars":200}`, articleURL))
		if err != nil {
			t.Fatal(err)
		}
		ps := out["passages"].([]any)
		if len(ps) != 1 || !strings.Contains(ps[0].(map[string]any)["text"].(string), "6000 and 12000 euros") || out["cached"] != true {
			t.Errorf("extract = %v", out)
		}
		if _, err := call(ToolFetch, fmt.Sprintf(`{"url":%q}`, srv.URL+"/private")); err == nil || !strings.Contains(err.Error(), "robots.txt") {
			t.Errorf("robots: %v", err)
		}
		if _, err := call(ToolExtract, fmt.Sprintf(`{"url":%q}`, articleURL)); err == nil {
			t.Error("extract without question should fail")
		}
		if err := m.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestModuleConfigErrors(t *testing.T) {
	h := kerneltest.New(t)
	h.SideValue = kernel.SideNode
	h.ConfigYAML = "search:\n  provider: bing\n"
	if err := (&Module{side: kernel.SideNode}).Init(context.Background(), h); err == nil || !strings.Contains(err.Error(), "unknown search provider") {
		t.Errorf("err = %v", err)
	}
	h = kerneltest.New(t)
	h.ConfigYAML = "search:\n  provider: searxng\n"
	if err := (&Module{side: kernel.SideNode}).Init(context.Background(), h); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Errorf("err = %v", err)
	}
	h = kerneltest.New(t)
	h.ConfigYAML = "search:\n  provider: searxng, duckduckgo\n  searxng: { base_url: http://s }\ncache: { ttl: 30m }\nfetch: { timeout: 10 }\n"
	if err := (&Module{side: kernel.SideNode}).Init(context.Background(), h); err != nil {
		t.Errorf("comma list: %v", err)
	}
}
