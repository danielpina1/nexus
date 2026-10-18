package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// RobustConfig is the part of the "vault" config section for durability and
// the extended features (see docs/VAULT.md §8):
//
//	vault:
//	  conflicts: keep_both        # keep_both | reject (stale writes that cannot be merged)
//	  snapshots: true             # local version store for undo when the vault has no git
//	  snapshot_keep: 50           # versions kept per note
//	  trash_days: 30              # purge trashed files after N days (0 = never)
//	  max_note_mb: 5
//	  max_attachment_mb: 100
//	  templates_dir: templates
//	  periodic:                   # daily | weekly | monthly | yearly
//	    daily: {folder: daily, format: YYYY-MM-DD, template: templates/daily.md}
//	  check:
//	    on_start: true            # quick integrity check at startup
//	    full_at: "03:30"          # daily full check (local time); "" = off
//	  backup:
//	    dir: ~/Backups/nexus-vault  # e.g. an external disk or iCloud Drive folder
//	    at: "02:30"               # daily backup time; "" = only on demand
//	    keep: {daily: 7, weekly: 4, monthly: 12}
//	    bundle: true              # also a git bundle (git vaults)
//	    remote: ""                # private git remote to push to (never forced)
//	    branch: ""                # default: current branch
//	    push: false               # push on the schedule too
//	  encryption:
//	    folders: [private/]
//	    key_source: file          # file | keychain (macOS) | env
//	    key_file: ""              # default <data dir>/vault.key (created 0600)
//	    keychain_account: vault-key
//	    key_env: NEXUS_VAULT_KEY  # base64 key, for key_source: env
//	    searchable: false         # index decrypted bodies (search/snippets)
//	    agent_access: false       # let agent tools read encrypted notes
//	  publish_changes: false      # also publish vault changes on the event bus
type RobustConfig struct {
	Conflicts       string                    `yaml:"conflicts"`
	Snapshots       *bool                     `yaml:"snapshots"`
	SnapshotKeep    int                       `yaml:"snapshot_keep"`
	TrashDays       *int                      `yaml:"trash_days"`
	MaxNoteMB       float64                   `yaml:"max_note_mb"`
	MaxAttachmentMB float64                   `yaml:"max_attachment_mb"`
	TemplatesDir    string                    `yaml:"templates_dir"`
	Periodic        map[string]PeriodicConfig `yaml:"periodic"`
	Check           struct {
		OnStart *bool  `yaml:"on_start"`
		FullAt  string `yaml:"full_at"`
	} `yaml:"check"`
	Backup         BackupConfig     `yaml:"backup"`
	Encryption     EncryptionConfig `yaml:"encryption"`
	PublishChanges bool             `yaml:"publish_changes"`
}

// BackupConfig is vault.backup.
type BackupConfig struct {
	Dir    string    `yaml:"dir"`
	At     string    `yaml:"at"`
	Keep   Retention `yaml:"keep"`
	Bundle *bool     `yaml:"bundle"`
	Remote string    `yaml:"remote"`
	Branch string    `yaml:"branch"`
	Push   bool      `yaml:"push"`
}

// EncryptionConfig is vault.encryption.
type EncryptionConfig struct {
	Folders         []string `yaml:"folders"`
	KeySource       string   `yaml:"key_source"`
	KeyFile         string   `yaml:"key_file"`
	KeychainAccount string   `yaml:"keychain_account"`
	KeyEnv          string   `yaml:"key_env"`
	Searchable      bool     `yaml:"searchable"`
	AgentAccess     bool     `yaml:"agent_access"`
}

// ExpandHome expands a leading "~/".
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// LoadKey resolves the encryption key from config. dataDir is the default
// location of the key file.
func (c EncryptionConfig) LoadKey(dataDir string) ([]byte, error) {
	switch strings.ToLower(c.KeySource) {
	case "", "file":
		p := ExpandHome(c.KeyFile)
		if p == "" {
			p = filepath.Join(dataDir, "vault.key")
		}
		return LoadOrCreateKeyFile(p)
	case "keychain":
		return LoadOrCreateKeychainKey(c.KeychainAccount)
	case "env":
		name := c.KeyEnv
		if name == "" {
			name = "NEXUS_VAULT_KEY"
		}
		s := os.Getenv(name)
		if s == "" {
			return nil, fmt.Errorf("vault: encryption key env %s is empty", name)
		}
		return decodeKey(s)
	}
	return nil, fmt.Errorf("vault: encryption.key_source must be file, keychain or env")
}

// robustOptions fills Options from the config.
func (m *Module) robustOptions(o *Options) error {
	c := m.cfg.RobustConfig
	switch strings.ToLower(c.Conflicts) {
	case "", "keep_both", "keep-both":
		o.KeepConflicts = true
	case "reject":
	default:
		return fmt.Errorf("vault config: conflicts must be keep_both or reject")
	}
	if c.Snapshots == nil || *c.Snapshots {
		o.StateDir = m.h.DataDir()
	}
	o.SnapshotKeep = c.SnapshotKeep
	o.MaxNoteBytes = int64(c.MaxNoteMB * (1 << 20))
	o.MaxAttachmentBytes = int64(c.MaxAttachmentMB * (1 << 20))
	if len(c.Encryption.Folders) > 0 {
		key, err := c.Encryption.LoadKey(m.h.DataDir())
		if err != nil {
			return err
		}
		o.Encryption = &EncryptionOptions{Folders: c.Encryption.Folders, Key: key, Searchable: c.Encryption.Searchable, AgentAccess: c.Encryption.AgentAccess}
	}
	return nil
}

// wireVault connects the vault to optional services (resolved lazily).
func (m *Module) wireVault(v *Vault) {
	c := m.cfg.RobustConfig
	if c.TemplatesDir != "" {
		v.templates = strings.Trim(c.TemplatesDir, "/")
	}
	v.SetPeriodic(c.Periodic)
	v.SetNoticeSink(func(n Notice) { m.publishNotice(n) })
	v.Fetch = func(ctx context.Context, url string) (*Clip, error) {
		t, ok := m.h.Tools().Get("web.fetch")
		if !ok {
			return nil, fmt.Errorf("web.fetch tool not available (enable the tools.web module)")
		}
		in, _ := json.Marshal(map[string]any{"url": url, "max_chars": 100000})
		res, err := t.Invoke(ctx, kernel.ToolCall{Input: in})
		if err != nil {
			return nil, err
		}
		var out struct {
			FinalURL  string `json:"final_url"`
			Title     string `json:"title"`
			Byline    string `json:"byline"`
			SiteName  string `json:"site_name"`
			Published string `json:"published"`
			Text      string `json:"text"`
		}
		if err := json.Unmarshal(res.Output, &out); err != nil {
			return nil, err
		}
		u := out.FinalURL
		if u == "" {
			u = url
		}
		return &Clip{URL: u, Title: out.Title, Byline: out.Byline, SiteName: out.SiteName, Published: out.Published, Text: out.Text}, nil
	}
}

// backupConfig returns the backup options (nil when not configured).
func (m *Module) backupConfig() *BackupOptions {
	b := m.cfg.Backup
	if b.Dir == "" {
		return nil
	}
	return &BackupOptions{
		Dir: ExpandHome(b.Dir), Retention: b.Keep, NoBundle: b.Bundle != nil && !*b.Bundle,
		Remote: b.Remote, Branch: b.Branch,
	}
}

// publishNotice puts a vault notice in the inbox (event bus), if present.
func (m *Module) publishNotice(n Notice) {
	bus, err := kernel.Resolve[kernel.EventBus](m.h, kernel.SvcEventBus)
	if err != nil {
		return
	}
	pn := &nexusv1.Notice{Id: "ntc_" + ulid.Make().String(), Kind: n.Kind, Title: n.Title, Body: n.Body}
	if n.Data != nil {
		if s, err := structpb.NewStruct(n.Data); err == nil {
			pn.Data = s
		}
	}
	for _, p := range n.Paths {
		ref := &nexusv1.NoteRef{Path: p, Title: p}
		if e, ok := m.V.Index().Get(context.Background(), p); ok {
			ref = entryRef(e)
		}
		pn.Notes = append(pn.Notes, ref)
	}
	if _, err := bus.Publish(context.Background(), &nexusv1.Event{Actor: "hub", Body: &nexusv1.Event_Notice{Notice: pn}}); err != nil {
		m.h.Logger().Warn("vault: publish notice", "err", err)
	}
}

// startRobust runs the startup check and the daily jobs (full check, trash
// purge, backup) and, when enabled, publishes change summaries.
func (m *Module) startRobust(ctx context.Context) {
	c := m.cfg.RobustConfig
	log := m.h.Logger()
	if c.Check.OnStart == nil || *c.Check.OnStart {
		m.goJob(ctx, func(ctx context.Context) {
			rep, err := m.V.Check(ctx, false)
			if err != nil {
				log.Warn("vault: startup check failed", "err", err)
				return
			}
			m.V.setLastReport(rep)
			m.reportCheck(rep)
		})
	}
	trashDays := 30
	if c.TrashDays != nil {
		trashDays = *c.TrashDays
	}
	if trashDays > 0 {
		m.daily(ctx, "04:10", func(ctx context.Context) {
			if n, err := m.V.PurgeTrash(ctx, "", time.Duration(trashDays)*24*time.Hour); err == nil && n > 0 {
				log.Info("vault: purged trash", "items", n)
			}
		})
	}
	if c.Check.FullAt != "" {
		m.daily(ctx, c.Check.FullAt, func(ctx context.Context) {
			rep, err := m.V.Check(ctx, true)
			if err != nil {
				log.Warn("vault: nightly check failed", "err", err)
				return
			}
			m.V.setLastReport(rep)
			m.reportCheck(rep)
		})
	}
	if o := m.backupConfig(); o != nil && c.Backup.At != "" {
		m.daily(ctx, c.Backup.At, func(ctx context.Context) {
			opts := *o
			if !c.Backup.Push {
				opts.Remote = ""
			}
			res, err := m.V.Backup(ctx, opts)
			if err != nil {
				log.Warn("vault: scheduled backup failed", "err", err)
				return
			}
			log.Info("vault: backup written", "files", len(res.Created), "pruned", len(res.Pruned))
		})
	}
	if c.PublishChanges {
		m.publishChanges(ctx)
	}
}

// reportCheck raises a notice when a check finds errors or warnings.
func (m *Module) reportCheck(rep *Report) {
	if rep.Serious() == 0 {
		m.h.Logger().Info("vault: integrity check", "full", rep.Full, "notes", rep.Notes, "issues", len(rep.Issues))
		return
	}
	counts := map[string]any{}
	for k, n := range rep.Counts() {
		counts[k] = float64(n)
	}
	var paths []string
	for _, i := range rep.Issues {
		if i.Severity == "error" && i.Path != "" && len(paths) < 5 {
			paths = append(paths, i.Path)
		}
	}
	m.V.notice(Notice{
		Kind:  nexusv1.NoticeKind_NOTICE_KIND_ALERT,
		Title: "Vault check found problems",
		Body:  rep.Summary() + " Open Settings → Vault → Check to review; Repair fixes most of them.",
		Paths: paths,
		Data:  map[string]any{"kind": "vault.check", "full": rep.Full, "counts": counts},
	})
}

func (m *Module) goJob(ctx context.Context, fn func(context.Context)) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		fn(ctx)
	}()
}

// daily runs fn every day at hh:mm local time until ctx ends.
func (m *Module) daily(ctx context.Context, at string, fn func(context.Context)) {
	t, err := time.Parse("15:04", strings.TrimSpace(at))
	if err != nil {
		m.h.Logger().Warn("vault: bad time of day; job disabled", "at", at)
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
			if !next.After(now) {
				next = next.AddDate(0, 0, 1)
			}
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			fn(ctx)
		}
	}()
}

// publishChanges sends debounced change summaries to the event bus as
// notices (data.kind = "vault.changes"), for consumers that only read the
// event log. Real-time UIs should use WatchVault instead.
func (m *Module) publishChanges(ctx context.Context) {
	if _, err := kernel.Resolve[kernel.EventBus](m.h, kernel.SvcEventBus); err != nil {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		var mu sync.Mutex
		var batch []Change
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			_ = m.V.WatchChanges(cctx, 0, "", func(c Change) error {
				mu.Lock()
				batch = append(batch, c)
				mu.Unlock()
				return nil
			})
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			mu.Lock()
			b := batch
			batch = nil
			mu.Unlock()
			if len(b) == 0 {
				continue
			}
			var items []any
			var paths []string
			for _, c := range b {
				items = append(items, map[string]any{"rev": float64(c.Rev), "kind": strings.ToLower(strings.TrimPrefix(c.Kind.String(), "VAULT_CHANGE_KIND_")),
					"path": c.Path, "old_path": c.OldPath, "origin": c.Origin})
				if len(paths) < 10 && c.Path != "" {
					paths = append(paths, c.Path)
				}
			}
			m.publishNotice(Notice{Kind: nexusv1.NoticeKind_NOTICE_KIND_UNSPECIFIED, Title: fmt.Sprintf("Vault: %d changes", len(b)),
				Paths: paths, Data: map[string]any{"kind": "vault.changes", "changes": items, "head_rev": float64(b[len(b)-1].Rev)}})
		}
	}()
}
