package vault

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// Issue kinds reported by Check.
const (
	IssueIndexMissing      = "index_missing"       // file not in the index
	IssueIndexOrphan       = "index_orphan"        // indexed note without a file
	IssueIndexStale        = "index_stale"         // index content hash differs (full)
	IssueBrokenLink        = "broken_link"         // link to a missing note (full)
	IssueDuplicateID       = "duplicate_id"        // two notes share an id
	IssueMissingID         = "missing_id"          // note without id (full)
	IssueInvalidFM         = "invalid_frontmatter" // YAML error (full)
	IssueOrphanAttachment  = "orphan_attachment"   // attachment nothing links to (full)
	IssueOversize          = "oversize"            // over the size limits (full)
	IssueTempFile          = "temp_file"           // leftover temp file from a crash
	IssueConflictCopy      = "conflict_copy"       // "(conflict …)" copy to merge
	IssuePendingJournal    = "pending_journal"     // interrupted multi-file operation
	IssueUnreadable        = "unreadable"          // cannot read/decrypt (full)
	IssueEncryptionMissing = "plaintext_private"   // plaintext in an encrypted folder (full)
)

// Issue is one integrity problem.
type Issue struct {
	Kind     string
	Severity string // error | warning | info
	Path     string
	Detail   string
	Fixable  bool
	Related  []string
}

// Report is the result of Check.
type Report struct {
	At          time.Time
	Full        bool
	Notes       int
	Attachments int
	Issues      []Issue
	Duration    time.Duration
}

// Counts returns issues per kind.
func (r *Report) Counts() map[string]int32 {
	m := map[string]int32{}
	for _, i := range r.Issues {
		m[i.Kind]++
	}
	return m
}

// Serious reports whether the report has errors or warnings.
func (r *Report) Serious() int {
	n := 0
	for _, i := range r.Issues {
		if i.Severity != "info" {
			n++
		}
	}
	return n
}

// Proto converts a report to its API form.
func (r *Report) Proto() *nexusv1.IntegrityReport {
	out := &nexusv1.IntegrityReport{At: timestamppb.New(r.At), Full: r.Full, Notes: int32(r.Notes), Attachments: int32(r.Attachments),
		DurationMs: r.Duration.Milliseconds(), Counts: r.Counts()}
	for _, i := range r.Issues {
		out.Issues = append(out.Issues, &nexusv1.IntegrityIssue{Kind: i.Kind, Severity: i.Severity, Path: i.Path, Detail: i.Detail, Fixable: i.Fixable, RelatedPaths: i.Related})
	}
	return out
}

// Summary is a one-paragraph description for notices.
func (r *Report) Summary() string {
	if len(r.Issues) == 0 {
		return fmt.Sprintf("No problems in %d notes and %d attachments.", r.Notes, r.Attachments)
	}
	counts := r.Counts()
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], strings.ReplaceAll(k, "_", " ")))
	}
	return fmt.Sprintf("Checked %d notes and %d attachments: %s.", r.Notes, r.Attachments, strings.Join(parts, ", "))
}

const maxIssuesPerKind = 200

// Check verifies the vault. Quick (full=false): index ↔ files (existence),
// duplicate ids, leftover temp files, interrupted journals, conflict
// copies. Full adds content hashes, frontmatter validity, missing ids,
// broken links, orphaned attachments, size limits, readability and
// encryption of private folders.
func (v *Vault) Check(ctx context.Context, full bool) (*Report, error) {
	start := time.Now()
	r := &Report{At: v.now(), Full: full}
	perKind := map[string]int{}
	add := func(i Issue) {
		perKind[i.Kind]++
		if perKind[i.Kind] <= maxIssuesPerKind {
			r.Issues = append(r.Issues, i)
		}
	}
	for _, j := range v.pendingJournals() {
		add(Issue{Kind: IssuePendingJournal, Severity: "error", Path: ".nexus/journal/" + j, Detail: "an interrupted multi-file operation could not be completed", Fixable: true})
	}
	// Walk every file (temp files live in hidden-named files).
	files := map[string]bool{}
	attachments := map[string]int64{}
	err := filepath.WalkDir(v.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != v.root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(v.root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(name, tempPrefix) {
			add(Issue{Kind: IssueTempFile, Severity: "warning", Path: rel, Detail: "leftover from an interrupted write", Fixable: true})
			return nil
		}
		if index.IsNotePath(rel) {
			files[rel] = true
			if isConflictCopy(rel) {
				add(Issue{Kind: IssueConflictCopy, Severity: "warning", Path: rel, Detail: "conflict copy: merge it into the original and delete it"})
			}
		} else if index.IsAttachmentPath(rel) {
			if fi, err := d.Info(); err == nil {
				attachments[rel] = fi.Size()
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.Notes, r.Attachments = len(files), len(attachments)
	indexed, err := v.ix.ListByType(ctx, "")
	if err != nil {
		return nil, err
	}
	byPath := map[string]*index.Entry{}
	for _, e := range indexed {
		byPath[e.Path] = e
		if !files[e.Path] {
			add(Issue{Kind: IssueIndexOrphan, Severity: "warning", Path: e.Path, Detail: "indexed but the file is gone", Fixable: true})
		}
	}
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if byPath[p] == nil {
			add(Issue{Kind: IssueIndexMissing, Severity: "warning", Path: p, Detail: "file not in the index", Fixable: true})
		}
	}
	// Duplicate ids.
	ids := map[string][]string{}
	for _, e := range indexed {
		if e.ID != "" && files[e.Path] {
			k := strings.ToLower(e.ID)
			ids[k] = append(ids[k], e.Path)
		}
	}
	for id, ps := range ids {
		if len(ps) > 1 {
			sort.Strings(ps)
			add(Issue{Kind: IssueDuplicateID, Severity: "error", Path: ps[0], Detail: "id " + id + " is used by " + fmt.Sprint(len(ps)) + " notes", Fixable: true, Related: ps[1:]})
		}
	}
	if !full {
		r.Duration = time.Since(start)
		return r, nil
	}
	tdir := v.templatesDir() + "/"
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ap := filepath.Join(v.root, filepath.FromSlash(p))
		raw, err := os.ReadFile(ap)
		if err != nil {
			add(Issue{Kind: IssueUnreadable, Severity: "error", Path: p, Detail: err.Error()})
			continue
		}
		if v.crypt.covers(p) && !IsEncrypted(raw) && v.crypt != nil {
			add(Issue{Kind: IssueEncryptionMissing, Severity: "warning", Path: p, Detail: "stored as plaintext in an encrypted folder", Fixable: true})
		}
		plain, err := v.decode(raw)
		if err != nil {
			add(Issue{Kind: IssueUnreadable, Severity: "error", Path: p, Detail: err.Error()})
			continue
		}
		if int64(len(plain)) > v.maxNote {
			add(Issue{Kind: IssueOversize, Severity: "warning", Path: p, Detail: fmt.Sprintf("%d bytes (limit %d)", len(plain), v.maxNote)})
		}
		if e := byPath[p]; e != nil && e.Hash != index.Hash(plain) {
			add(Issue{Kind: IssueIndexStale, Severity: "warning", Path: p, Detail: "index is out of date", Fixable: true})
		}
		n := note.Parse(p, plain)
		if n.FrontmatterErr != nil {
			add(Issue{Kind: IssueInvalidFM, Severity: "warning", Path: p, Detail: n.FrontmatterErr.Error()})
		} else if n.ID == "" && !strings.HasPrefix(p, tdir) {
			add(Issue{Kind: IssueMissingID, Severity: "info", Path: p, Detail: "no frontmatter id (links survive renames better with one)", Fixable: true})
		}
	}
	edges, err := v.ix.AllEdges(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range edges {
		if e.Dst == "" && !strings.HasPrefix(e.Src, tdir) {
			add(Issue{Kind: IssueBrokenLink, Severity: "info", Path: e.Src, Detail: fmt.Sprintf("line %d: [[%s]] does not match any note", e.Line, e.Target)})
		}
	}
	refs, err := v.ix.AttachmentLinks(ctx)
	if err != nil {
		return nil, err
	}
	var apaths []string
	for p := range attachments {
		apaths = append(apaths, p)
	}
	sort.Strings(apaths)
	for _, p := range apaths {
		if attachments[p] > v.maxAttach {
			add(Issue{Kind: IssueOversize, Severity: "warning", Path: p, Detail: fmt.Sprintf("%d bytes (limit %d)", attachments[p], v.maxAttach)})
		}
		if len(attachmentRefs(refs, p)) == 0 {
			add(Issue{Kind: IssueOrphanAttachment, Severity: "info", Path: p, Detail: "no note links to it"})
		}
	}
	for k, n := range perKind {
		if n > maxIssuesPerKind {
			r.Issues = append(r.Issues, Issue{Kind: k, Severity: "info", Detail: fmt.Sprintf("… and %d more", n-maxIssuesPerKind)})
		}
	}
	r.Duration = time.Since(start)
	return r, nil
}

// Repair fixes what Check can fix: completes interrupted journals, removes
// stale temp files, re-syncs the index, gives notes without an id one,
// gives duplicate ids fresh ones (the oldest note keeps its id) and
// encrypts plaintext notes in encrypted folders. With dryRun it only lists
// the actions. It returns the actions and a full check afterwards.
func (v *Vault) Repair(ctx context.Context, dryRun bool) ([]string, *Report, error) {
	before, err := v.Check(ctx, true)
	if err != nil {
		return nil, nil, err
	}
	var actions []string
	act := func(s string) { actions = append(actions, s) }
	ctx = WithOrigin(ctx, "system")
	if len(v.pendingJournals()) > 0 {
		act("complete interrupted operations")
		if !dryRun {
			v.wmu.Lock()
			touched, err := v.recoverJournal()
			v.wmu.Unlock()
			if err != nil {
				return actions, nil, err
			}
			if v.git != nil && len(touched) > 0 {
				v.git.schedule(touched, "vault: complete interrupted operation", "")
			}
		}
	}
	for _, i := range before.Issues {
		if i.Kind != IssueTempFile {
			continue
		}
		ap := filepath.Join(v.root, filepath.FromSlash(i.Path))
		if fi, err := os.Stat(ap); err == nil && time.Since(fi.ModTime()) > 10*time.Minute {
			act("remove " + i.Path)
			if !dryRun {
				_ = os.Remove(ap)
			}
		}
	}
	needSync := false
	for _, i := range before.Issues {
		switch i.Kind {
		case IssueIndexMissing, IssueIndexOrphan, IssueIndexStale:
			needSync = true
		}
	}
	if needSync {
		act("re-sync the index")
		if !dryRun {
			if _, err := v.Sync(ctx); err != nil {
				return actions, nil, err
			}
		}
	}
	for _, i := range before.Issues {
		switch i.Kind {
		case IssueMissingID:
			act("add an id to " + i.Path)
			if !dryRun {
				if _, err := v.Update(ctx, i.Path, "vault: add id to "+i.Path, func(old []byte) ([]byte, error) {
					if old == nil {
						return nil, fmt.Errorf("%w: %s", ErrNotFound, i.Path)
					}
					out, _, _ := note.EnsureID(old, NewID)
					return out, nil
				}); err != nil {
					v.log.Warn("vault: repair id", "path", i.Path, "err", err)
				}
			}
		case IssueDuplicateID:
			all := append([]string{i.Path}, i.Related...)
			keeper := v.oldestNote(all)
			for _, p := range all {
				if p == keeper {
					continue
				}
				act("give " + p + " a new id (" + keeper + " keeps it)")
				if dryRun {
					continue
				}
				if _, err := v.Update(ctx, p, "vault: new id for "+p+" (duplicate of "+keeper+")", func(old []byte) ([]byte, error) {
					if old == nil {
						return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
					}
					prev := note.Parse(p, old).ID
					return note.SetFields(old, map[string]any{"id": NewID(), "previous_id": prev}, "id", "previous_id")
				}); err != nil {
					v.log.Warn("vault: repair duplicate id", "path", p, "err", err)
				}
			}
		case IssueEncryptionMissing:
			act("encrypt " + i.Path)
			if !dryRun {
				if _, err := v.Update(ctx, i.Path, "vault: encrypt "+i.Path, func(old []byte) ([]byte, error) { return old, nil }); err != nil {
					v.log.Warn("vault: repair encrypt", "path", i.Path, "err", err)
				}
			}
		}
	}
	if dryRun {
		return actions, before, nil
	}
	after, err := v.Check(ctx, true)
	return actions, after, err
}

// oldestNote picks the note that keeps a shared id: earliest frontmatter
// created, then earliest modification time, then path.
func (v *Vault) oldestNote(paths []string) string {
	type cand struct {
		p string
		t time.Time
	}
	var cs []cand
	for _, p := range paths {
		t := time.Now()
		if e, ok := v.ix.Get(context.Background(), p); ok {
			t = e.Created
		}
		cs = append(cs, cand{p, t})
	}
	sort.Slice(cs, func(i, j int) bool {
		if !cs[i].t.Equal(cs[j].t) {
			return cs[i].t.Before(cs[j].t)
		}
		return cs[i].p < cs[j].p
	})
	return cs[0].p
}
