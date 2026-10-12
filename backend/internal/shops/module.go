package shops

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Config is the "shops" section of nexus.yaml:
//
//	shops:
//	  dir: shops        # vault folder with shop notes (relative to the vault root)
type Config struct {
	Dir string `yaml:"dir"`
}

// Module is the "shops" hub module.
type Module struct {
	kernel.BaseModule
	h   kernel.Host
	cfg Config
}

func (m *Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "shops",
		Description:    "Store registry for shopping agents (vault shops/*.md): hub tools shops.list and shops.get",
		Side:           kernel.SideHub,
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	m.h = h
	if err := h.Config(&m.cfg); err != nil {
		return fmt.Errorf("shops config: %w", err)
	}
	if m.cfg.Dir == "" {
		m.cfg.Dir = DefaultDir
	}
	for _, t := range m.Tools() {
		if err := h.Tools().Register(t); err != nil {
			return err
		}
	}
	return nil
}

// dir resolves the shops folder through the vault (resolved lazily: the
// vault is optional at Init).
func (m *Module) dir() (string, error) {
	if filepath.IsAbs(m.cfg.Dir) {
		return m.cfg.Dir, nil
	}
	v, err := kernel.Resolve[kernel.Vault](m.h, kernel.SvcVault)
	if err != nil {
		return "", fmt.Errorf("shops: no vault available: %w", err)
	}
	return filepath.Join(v.Root(), filepath.FromSlash(m.cfg.Dir)), nil
}

// Shops loads every shop note.
func (m *Module) Shops() ([]*Shop, error) {
	d, err := m.dir()
	if err != nil {
		return nil, err
	}
	return Load(d)
}

// summary is a shops.list entry (no playbook).
type summary struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Domains       []string `json:"domains"`
	Categories    []string `json:"categories,omitempty"`
	Country       string   `json:"country,omitempty"`
	SearchURL     string   `json:"search_url,omitempty"`
	SearchLink    string   `json:"search_link,omitempty"`
	LoginRequired bool     `json:"login_required"`
	ResearchOnly  bool     `json:"research_only,omitempty"`
	Notes         string   `json:"notes,omitempty"`
	Path          string   `json:"path"`
}

// stringsArg accepts a JSON list or a comma-separated string.
type stringsArg []string

func (s *stringsArg) UnmarshalJSON(b []byte) error {
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*s = list
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return fmt.Errorf("want a list of strings or a comma-separated string")
	}
	for _, p := range strings.Split(one, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*s = append(*s, p)
		}
	}
	return nil
}

// flexInt accepts 4 or "4" (workflow templates render strings).
type flexInt int

func (n *flexInt) UnmarshalJSON(b []byte) error {
	var i int
	if err := json.Unmarshal(b, &i); err == nil {
		*n = flexInt(i)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if strings.TrimSpace(s) == "" {
		return nil
	}
	_, err := fmt.Sscan(s, &i)
	*n = flexInt(i)
	return err
}

// flexBool accepts true or "true".
type flexBool bool

func (v *flexBool) UnmarshalJSON(b []byte) error {
	var x bool
	if err := json.Unmarshal(b, &x); err == nil {
		*v = flexBool(x)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*v = flexBool(strings.EqualFold(strings.TrimSpace(s), "true") || strings.TrimSpace(s) == "1")
	return nil
}

// Tools returns shops.list and shops.get.
func (m *Module) Tools() []kernel.Tool {
	read := nexusv1.ActionClass_ACTION_CLASS_READ
	return []kernel.Tool{
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name: "shops.list",
				Description: "List the online stores the user shops at (vault shops/ notes): id, name, domains, categories, search URL. " +
					"Filter by category (groceries, electronics, books, home, sports, price-comparison…) or ids; buyable=true leaves out research-only sites.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{
					"category":{"type":"string"},
					"ids":{"type":"array","items":{"type":"string"}},
					"query":{"type":"string","description":"product to search; fills search_link"},
					"buyable":{"type":"boolean"},
					"limit":{"type":"integer","minimum":1,"maximum":50}}}`),
				ActionClass: read,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct {
					Category string     `json:"category"`
					IDs      stringsArg `json:"ids"`
					Query    string     `json:"query"`
					Buyable  flexBool   `json:"buyable"`
					Limit    flexInt    `json:"limit"`
				}
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				all, err := m.Shops()
				if err != nil {
					return kernel.ToolResult{}, err
				}
				sel := Select(all, Filter{Category: in.Category, IDs: in.IDs, Buyable: bool(in.Buyable), Limit: int(in.Limit)})
				out := struct {
					Shops []summary `json:"shops"`
					Count int       `json:"count"`
				}{Shops: []summary{}}
				for _, s := range sel {
					out.Shops = append(out.Shops, summary{
						ID: s.ID, Name: s.Name, Domains: s.Domains, Categories: s.Categories, Country: s.Country,
						SearchURL: s.SearchURL, SearchLink: s.SearchLink(in.Query), LoginRequired: s.LoginRequired,
						ResearchOnly: s.ResearchOnly, Notes: s.Notes, Path: s.Path,
					})
				}
				out.Count = len(out.Shops)
				return kernel.ToolOutput(out)
			},
		},
		kernel.ToolFunc{
			S: kernel.ToolSpec{
				Name: "shops.get",
				Description: "Get one store (by id, name or domain) with its playbook: how search, cart, checkout, login and delivery work there. " +
					"In a browser run this also unlocks the store's domains and its own logged-in browser profile — call it before opening the store.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{
					"id":{"type":"string","description":"store id (e.g. worten), name or domain"},
					"query":{"type":"string","description":"product to search; fills search_link"}},"required":["id"]}`),
				ActionClass: read,
			},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				var in struct {
					ID    string `json:"id"`
					Query string `json:"query"`
				}
				if err := kernel.ToolInput(c, &in); err != nil {
					return kernel.ToolResult{}, err
				}
				all, err := m.Shops()
				if err != nil {
					return kernel.ToolResult{}, err
				}
				s := Find(all, in.ID)
				if s == nil {
					var ids []string
					for _, x := range Select(all, Filter{}) {
						ids = append(ids, x.ID)
					}
					return kernel.ToolResult{}, fmt.Errorf("unknown shop %q (known: %s); add a note in shops/ to support a new store", in.ID, strings.Join(ids, ", "))
				}
				if !s.Enabled {
					return kernel.ToolResult{}, fmt.Errorf("shop %q is disabled (enabled: false in %s)", s.ID, s.Path)
				}
				if !s.Valid() {
					return kernel.ToolResult{}, fmt.Errorf("shop note %s is invalid: %s", s.Path, strings.Join(s.Errors, "; "))
				}
				out := struct {
					*Shop
					SearchLink string `json:"search_link,omitempty"`
				}{Shop: s, SearchLink: s.SearchLink(in.Query)}
				return kernel.ToolOutput(out)
			},
		},
	}
}
