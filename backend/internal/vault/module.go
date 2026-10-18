package vault

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Config is the "vault" section of nexus.yaml:
//
//	vault:
//	  path: ./vault          # vault directory (created if missing)
//	  git: true              # auto-commit when the vault is a git repo
//	  commit_delay: 5s       # batch writes into one commit
//	  watch: true            # reindex external edits (Obsidian)
//	  embeddings: false      # embed notes with the LLM "embed" alias for semantic search
//	  embed_model: embed
type Config struct {
	Path        string `yaml:"path"`
	Git         *bool  `yaml:"git"`
	CommitDelay string `yaml:"commit_delay"`
	Watch       *bool  `yaml:"watch"`
	Embeddings  bool   `yaml:"embeddings"`
	EmbedModel  string `yaml:"embed_model"`
	// Episodes writes an episode note per finished top-level task (default true).
	Episodes *bool `yaml:"episodes"`
	// Robustness and feature settings (module_robust.go).
	RobustConfig `yaml:",inline"`
}

// Module is the "vault" hub module.
type Module struct {
	h       kernel.Host
	cfg     Config
	V       *Vault
	Catalog *Catalog
	Skills  *SkillCatalog
	Teams   *TeamCatalog

	stopWatch func()
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	vecKick   chan struct{}
}

func (m *Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "vault",
		Description:    "Markdown knowledge vault: notes, search, graph, facts and the agent catalog",
		Side:           kernel.SideHub,
		DefaultEnabled: true,
	}
}

func (m *Module) Init(ctx context.Context, h kernel.Host) error {
	m.h = h
	m.cfg = Config{Path: "./vault"}
	if err := h.Config(&m.cfg); err != nil {
		return fmt.Errorf("vault config: %w", err)
	}
	if m.cfg.Path == "" {
		m.cfg.Path = "./vault"
	}
	delay := 5 * time.Second
	if m.cfg.CommitDelay != "" {
		d, err := time.ParseDuration(m.cfg.CommitDelay)
		if err != nil {
			return fmt.Errorf("vault config: commit_delay: %w", err)
		}
		delay = d
	}
	git := m.cfg.Git == nil || *m.cfg.Git
	opts := Options{
		Root:        m.cfg.Path,
		IndexPath:   filepath.Join(h.DataDir(), "index.db"),
		Git:         git,
		CommitDelay: delay,
		Logger:      h.Logger(),
	}
	if err := m.robustOptions(&opts); err != nil {
		return err
	}
	v, err := Open(opts)
	if err != nil {
		return err
	}
	m.wireVault(v)
	v.LLM = func() (kernel.LLM, bool) {
		l, err := kernel.Resolve[kernel.LLM](h, kernel.SvcLLM)
		return l, err == nil
	}
	skills, err := NewSkillCatalog(ctx, v)
	if err != nil {
		v.Close()
		return err
	}
	cat, err := NewCatalog(ctx, v)
	if err != nil {
		v.Close()
		return err
	}
	cat.Skills = skills
	if err := cat.Reload(ctx); err != nil { // with skill warnings
		v.Close()
		return err
	}
	v.OnChange(func(paths []string) {
		if paths != nil && touches(paths, SkillsDir) {
			_ = cat.Reload(context.Background())
		}
	})
	teams, err := NewTeamCatalog(ctx, v, cat)
	if err != nil {
		v.Close()
		return err
	}
	m.V, m.Catalog, m.Skills, m.Teams = v, cat, skills, teams

	h.Provide(kernel.SvcVault, kernel.Vault(v))
	h.Provide(kernel.SvcAgents, kernel.AgentCatalog(cat))
	h.Provide(kernel.SvcSkills, kernel.SkillCatalog(skills))
	h.Provide(kernel.SvcTeams, kernel.TeamCatalog(teams))
	h.Handle(nexusv1connect.NewVaultServiceHandler(VaultHandler{V: v, BackupConfig: m.backupConfig}))
	h.Handle(nexusv1connect.NewAgentServiceHandler(AgentHandler{C: cat, S: skills}))
	for _, t := range Tools(v) {
		if err := h.Tools().Register(t); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	log := m.h.Logger()
	st, err := m.V.Sync(ctx)
	if err != nil {
		return fmt.Errorf("vault: index: %w", err)
	}
	log.Info("vault: indexed", "root", m.V.Root(), "notes", st.Scanned, "updated", st.Updated, "removed", st.Removed, "git", m.V.GitEnabled())
	if m.cfg.Watch == nil || *m.cfg.Watch {
		stop, err := m.V.Watch(context.Background(), 500*time.Millisecond)
		if err != nil {
			log.Warn("vault: file watcher unavailable; external edits need a restart", "err", err)
		} else {
			m.stopWatch = stop
		}
	}
	bg, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	if m.cfg.Episodes == nil || *m.cfg.Episodes {
		m.startEpisodes(bg)
	}
	if m.cfg.Embeddings {
		m.startVectors(bg)
	}
	m.startRobust(bg)
	return nil
}

// startVectors keeps note embeddings fresh in the background when an LLM
// service is available.
func (m *Module) startVectors(ctx context.Context) {
	model := m.cfg.EmbedModel
	if model == "" {
		model = "embed"
	}
	vec := index.NewVectors(m.V.Index(), llmEmbedder{v: m.V, model: model})
	m.vecKick = make(chan struct{}, 1)
	kick := func() {
		select {
		case m.vecKick <- struct{}{}:
		default:
		}
	}
	m.V.OnChange(func([]string) { kick() })
	kick()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		enabled := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.vecKick:
			}
			time.Sleep(2 * time.Second) // let bursts of writes settle
			if _, ok := m.V.LLM(); !ok {
				continue
			}
			n, err := vec.Refresh(ctx)
			if err != nil {
				m.h.Logger().Warn("vault: embedding refresh failed", "err", err)
				continue
			}
			if !enabled {
				m.V.Index().SetVectorSearcher(vec)
				enabled = true
			}
			if n > 0 {
				m.h.Logger().Debug("vault: embedded notes", "count", n)
			}
		}
	}()
}

type llmEmbedder struct {
	v     *Vault
	model string
}

func (e llmEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	l, ok := e.v.LLM()
	if !ok {
		return nil, fmt.Errorf("llm service not available")
	}
	return l.Embed(ctx, e.model, texts)
}

func (m *Module) Stop(ctx context.Context) error {
	if m.stopWatch != nil {
		m.stopWatch()
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	if m.V != nil {
		return m.V.Close()
	}
	return nil
}

// WriteEpisode is a convenience for other modules holding the module.
func (m *Module) WriteEpisode(ctx context.Context, task *nexusv1.Task, summary string) (*nexusv1.NoteRef, error) {
	return EpisodeWriter{Vault: m.V}.WriteEpisode(ctx, task, summary)
}
