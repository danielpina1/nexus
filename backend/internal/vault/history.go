package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Version is one version of a note (see History).
type Version struct {
	// Commit is a git commit hash, or "snap:<sha256>" (snapshot store).
	Commit  string
	Path    string
	At      time.Time
	Message string
	Author  string
	RunID   string
	Diff    string
	Deleted bool
}

// Proto converts a version to its API form.
func (ver Version) Proto() *nexusv1.NoteVersion {
	return &nexusv1.NoteVersion{
		Commit: ver.Commit, Path: ver.Path, At: timestamppb.New(ver.At), Message: ver.Message,
		Author: ver.Author, Diff: ver.Diff, RunId: ver.RunID, Deleted: ver.Deleted,
	}
}

// HistoryAvailable reports whether the vault keeps versions (git or the
// snapshot store).
func (v *Vault) HistoryAvailable() bool { return v.git != nil || v.snaps != nil }

// History returns the versions of a note, newest first (limit default 50).
// With git, pending batched writes are committed first so the list is
// complete; renames are followed. includeDiff adds a unified diff of each
// version against the previous one (decrypted for encrypted notes).
func (v *Vault) History(ctx context.Context, idOrPath string, limit int, includeDiff bool) ([]Version, error) {
	rel, err := v.locate(ctx, idOrPath)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var out []Version
	contents := map[int]string{}
	switch {
	case v.git != nil:
		v.Flush()
		gvs, err := v.git.logFile(ctx, rel, limit+1)
		if err != nil {
			return nil, err
		}
		for i, gv := range gvs {
			ver := Version{Commit: gv.Commit, Path: gv.Path, At: gv.At, Message: firstLine(gv.Message), Author: gv.Author,
				RunID: trailer(gv.Message, "Nexus-Run"), Deleted: gv.Status == "D"}
			out = append(out, ver)
			if includeDiff && !ver.Deleted {
				if raw, err := v.git.show(ctx, gv.Commit, gv.Path); err == nil {
					if b, err := v.decode(raw); err == nil {
						contents[i] = string(b)
					}
				}
			}
		}
	case v.snaps != nil:
		es, err := v.snaps.versions(rel, limit+1)
		if err != nil {
			return nil, err
		}
		for i, e := range es {
			ver := Version{Commit: SnapshotPrefix + e.Hash, Path: e.Path, At: e.time(), Message: e.Message, RunID: e.Run, Author: "Nexus", Deleted: e.Hash == ""}
			if e.Hash == "" {
				ver.Commit = SnapshotPrefix + "deleted-" + fmt.Sprint(e.At)
			}
			out = append(out, ver)
			if includeDiff && e.Hash != "" {
				if raw, err := v.snaps.object(e.Hash); err == nil {
					if b, err := v.decode(raw); err == nil {
						contents[i] = string(b)
					}
				}
			}
		}
	default:
		return nil, fmt.Errorf("%w: history needs git or vault.snapshots (no versions are kept)", ErrInvalid)
	}
	if len(out) == 0 {
		if _, _, err := v.ReadRaw(ctx, rel); errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	extra := len(out) > limit
	if includeDiff {
		for i := range out {
			if extra && i == len(out)-1 {
				break
			}
			prev := ""
			if i+1 < len(out) {
				prev = contents[i+1]
			}
			out[i].Diff = UnifiedDiff("a/"+pathOr(out, i+1, out[i].Path), "b/"+out[i].Path, prev, contents[i])
		}
	}
	if extra {
		out = out[:limit]
	}
	return out, nil
}

func pathOr(vs []Version, i int, def string) string {
	if i < len(vs) {
		return vs[i].Path
	}
	return def
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func trailer(msg, key string) string {
	for _, l := range strings.Split(msg, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), key+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// GetVersion returns the decrypted Markdown of a note at a version.
func (v *Vault) GetVersion(ctx context.Context, idOrPath, commit string) ([]byte, error) {
	rel, err := v.locate(ctx, idOrPath)
	if err != nil {
		return nil, err
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return nil, fmt.Errorf("%w: commit is required", ErrInvalid)
	}
	if h, ok := strings.CutPrefix(commit, SnapshotPrefix); ok {
		if v.snaps == nil {
			return nil, fmt.Errorf("%w: no snapshot store", ErrNotFound)
		}
		raw, err := v.snaps.object(h)
		if err != nil {
			return nil, err
		}
		return v.decode(raw)
	}
	if h, ok := strings.CutPrefix(commit, HashPrefix); ok {
		if b, ok := v.baseContent(ctx, rel, HashPrefix+h); ok {
			return b, nil
		}
		return nil, fmt.Errorf("%w: version %s", ErrNotFound, commit)
	}
	if v.git == nil {
		return nil, fmt.Errorf("%w: version %s (no git)", ErrNotFound, commit)
	}
	raw, err := v.git.show(ctx, commit, rel)
	if err != nil {
		// The note may have had another name at that commit.
		gvs, lerr := v.git.logFile(ctx, rel, 0)
		if lerr != nil {
			return nil, fmt.Errorf("%w: %s at %s", ErrNotFound, rel, commit)
		}
		found := false
		for _, gv := range gvs {
			if strings.HasPrefix(gv.Commit, commit) && gv.Status != "D" {
				raw, err = v.git.show(ctx, gv.Commit, gv.Path)
				found = err == nil
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %s at %s", ErrNotFound, rel, commit)
		}
	}
	return v.decode(raw)
}

// RestoreVersion writes an old version of a note back (as a new change, so
// the restore itself can be undone).
func (v *Vault) RestoreVersion(ctx context.Context, idOrPath, commit, msg string) (*nexusv1.Note, error) {
	rel, err := v.locate(ctx, idOrPath)
	if err != nil {
		return nil, err
	}
	b, err := v.GetVersion(ctx, rel, commit)
	if err != nil {
		return nil, err
	}
	if msg == "" {
		short := commit
		if len(short) > 12 {
			short = short[:12]
		}
		msg = fmt.Sprintf("restore %s to %s", rel, short)
	}
	return v.WriteNote(ctx, rel, b, msg, "")
}
