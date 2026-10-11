package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/agentdef"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
	"github.com/danielpina1/nexus/backend/kernel"
)

// AgentsDir is where agent notes live.
const AgentsDir = "agents"

// Catalog is the agent catalog backed by agents/*.md. It implements
// kernel.AgentCatalog and reloads itself when agent notes change.
type Catalog struct {
	v *Vault

	mu     sync.RWMutex
	byID   map[string]*agentEntry
	sorted []*agentEntry // by id, includes invalid ones
}

type agentEntry struct {
	agent        *nexusv1.Agent
	instructions string
	markdown     string
}

var _ kernel.AgentCatalog = (*Catalog)(nil)

// NewCatalog loads the catalog and subscribes to vault changes.
func NewCatalog(ctx context.Context, v *Vault) (*Catalog, error) {
	c := &Catalog{v: v, byID: map[string]*agentEntry{}}
	if err := c.Reload(ctx); err != nil {
		return nil, err
	}
	v.OnChange(func(paths []string) {
		if paths != nil {
			hit := false
			for _, p := range paths {
				if strings.HasPrefix(p, AgentsDir+"/") {
					hit = true
					break
				}
			}
			if !hit {
				return
			}
		}
		if err := c.Reload(context.Background()); err != nil {
			v.log.Warn("vault: reload agents", "err", err)
		}
	})
	return c, nil
}

// Reload re-reads agents/*.md. Invalid notes are kept with Errors set and
// Enabled=false; duplicate ids are reported on the later file (by path).
func (c *Catalog) Reload(ctx context.Context) error {
	dir := filepath.Join(c.v.root, AgentsDir)
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	byID := map[string]*agentEntry{}
	var list []*agentEntry
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(path.Ext(e.Name()), ".md") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		rel := AgentsDir + "/" + e.Name()
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			c.v.log.Warn("vault: read agent", "path", rel, "err", err)
			continue
		}
		a, instr := agentdef.Parse(rel, b)
		ent := &agentEntry{agent: a, instructions: instr, markdown: string(b)}
		if prev, dup := byID[a.Id]; dup {
			a.Errors = append(a.Errors, fmt.Sprintf("duplicate id %q (also in %s)", a.Id, prev.agent.GetNote().GetPath()))
			a.Enabled = false
			list = append(list, ent)
			continue
		}
		byID[a.Id] = ent
		list = append(list, ent)
		if len(a.Errors) > 0 {
			c.v.log.Warn("vault: invalid agent note", "path", rel, "errors", a.Errors)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].agent.Id < list[j].agent.Id })
	c.mu.Lock()
	c.byID, c.sorted = byID, list
	c.mu.Unlock()
	return nil
}

// ListAgents returns enabled, valid agents (all of them with includeDisabled).
func (c *Catalog) ListAgents(_ context.Context, includeDisabled bool) ([]*nexusv1.Agent, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []*nexusv1.Agent{}
	for _, e := range c.sorted {
		if !includeDisabled && !e.agent.Enabled {
			continue
		}
		out = append(out, e.agent)
	}
	return out, nil
}

// GetAgent returns the agent, its instructions (note body) and raw Markdown.
func (c *Catalog) GetAgent(_ context.Context, id string) (*nexusv1.Agent, string, string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byID[id]
	if !ok {
		return nil, "", "", fmt.Errorf("%w: agent %q", ErrNotFound, id)
	}
	return e.agent, e.instructions, e.markdown, nil
}

// ValidationError lists agent note problems.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid agent note: " + strings.Join(e.Problems, "; ")
}

func (e *ValidationError) Unwrap() error { return ErrInvalid }

// UpsertAgent validates the Markdown and writes agents/<id>.md (or the file
// that already defines this id). The note must declare its id.
func (c *Catalog) UpsertAgent(ctx context.Context, markdown, commitMessage string) (*nexusv1.Agent, error) {
	n := note.Parse("", []byte(markdown))
	id := strings.TrimSpace(n.ID)
	if id == "" {
		return nil, &ValidationError{Problems: []string{"missing id"}}
	}
	rel := AgentsDir + "/" + id + ".md"
	c.mu.RLock()
	if e, ok := c.byID[id]; ok {
		rel = e.agent.GetNote().GetPath()
	}
	c.mu.RUnlock()
	a, _ := agentdef.Parse(rel, []byte(markdown))
	if len(a.Errors) > 0 {
		return nil, &ValidationError{Problems: a.Errors}
	}
	if commitMessage == "" {
		commitMessage = "agent: update " + id
	}
	if _, err := c.v.WriteNote(ctx, rel, []byte(markdown), commitMessage, ""); err != nil {
		return nil, err
	}
	// The write listener reloads the catalog synchronously; reload again in
	// case listeners were not attached (catalog used standalone).
	if err := c.Reload(ctx); err != nil {
		return nil, err
	}
	got, _, _, err := c.GetAgent(ctx, id)
	return got, err
}
