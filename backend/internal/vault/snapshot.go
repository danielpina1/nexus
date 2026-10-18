package vault

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielpina1/nexus/backend/internal/vault/index"
)

// SnapshotPrefix marks snapshot-store versions in NoteVersion.commit.
const SnapshotPrefix = "snap:"

// snapStore is the undo history for vaults without git: a content-addressed
// object store plus an append-only log, under <state dir>/snapshots/:
//
//	objects/ab/abcdef…   file bytes exactly as on disk (so encrypted notes
//	                     stay encrypted), named by sha256
//	log.jsonl            {"p":path,"h":object hash ("" = deleted),"ph":plaintext
//	                     hash,"t":unix ms,"m":message,"r":run id,"o":old path}
//
// The log is compacted on open: at most keep versions per path are kept and
// unreferenced objects are deleted.
type snapStore struct {
	dir  string
	keep int

	mu   sync.Mutex
	last map[string]string // path → last object hash ("" = deleted)
	byPH map[string]string // plaintext hash → object hash
}

type snapEntry struct {
	Path    string `json:"p"`
	Hash    string `json:"h"`
	PH      string `json:"ph,omitempty"`
	At      int64  `json:"t"`
	Message string `json:"m,omitempty"`
	Run     string `json:"r,omitempty"`
	OldPath string `json:"o,omitempty"`
}

func openSnapStore(dir string, keep int) (*snapStore, error) {
	if keep <= 0 {
		keep = 50
	}
	s := &snapStore{dir: dir, keep: keep, last: map[string]string{}, byPH: map[string]string{}}
	if err := mkdirAllSync(filepath.Join(dir, "objects")); err != nil {
		return nil, err
	}
	if err := s.compact(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *snapStore) logPath() string { return filepath.Join(s.dir, "log.jsonl") }

func (s *snapStore) objPath(h string) string {
	if len(h) < 3 {
		return filepath.Join(s.dir, "objects", "_", h)
	}
	return filepath.Join(s.dir, "objects", h[:2], h)
}

func (s *snapStore) readLog() ([]snapEntry, error) {
	f, err := os.Open(s.logPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []snapEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		var e snapEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Path != "" {
			out = append(out, e)
		}
		// A torn last line (crash mid-append) is skipped.
	}
	return out, sc.Err()
}

// compact trims the log to keep versions per path and deletes unreferenced
// objects.
func (s *snapStore) compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLog()
	if err != nil {
		return err
	}
	count := map[string]int{}
	keep := make([]bool, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		p := entries[i].Path
		if count[p] < s.keep {
			keep[i] = true
			count[p]++
		}
	}
	var kept []snapEntry
	refs := map[string]bool{}
	for i, e := range entries {
		if keep[i] {
			kept = append(kept, e)
			if e.Hash != "" {
				refs[e.Hash] = true
			}
		}
	}
	if len(kept) != len(entries) {
		var b strings.Builder
		for _, e := range kept {
			line, _ := json.Marshal(e)
			b.Write(line)
			b.WriteByte('\n')
		}
		if err := writeAtomic(s.logPath(), []byte(b.String())); err != nil {
			return err
		}
		_ = filepath.WalkDir(filepath.Join(s.dir, "objects"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && !refs[d.Name()] {
				_ = os.Remove(p)
			}
			return nil
		})
	}
	s.last, s.byPH = map[string]string{}, map[string]string{}
	for _, e := range kept {
		s.last[e.Path] = e.Hash
		if e.PH != "" && e.Hash != "" {
			s.byPH[e.PH] = e.Hash
		}
	}
	return nil
}

// has reports whether path has any recorded version.
func (s *snapStore) has(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.last[path]
	return ok
}

// record stores a version of path. raw is the on-disk bytes (nil = deleted),
// plainHash the hash of the decrypted content.
func (s *snapStore) record(e snapEntry, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if raw != nil {
		e.Hash = index.Hash(raw)
		p := s.objPath(e.Hash)
		if !fileExists(p) {
			if err := writeAtomicMode(p, raw, 0o600); err != nil {
				return err
			}
		}
	} else {
		e.Hash = ""
	}
	if last, ok := s.last[e.Path]; ok && last == e.Hash && e.OldPath == "" {
		return nil // unchanged
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.logPath(), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.last[e.Path] = e.Hash
	if e.PH != "" && e.Hash != "" {
		s.byPH[e.PH] = e.Hash
	}
	return nil
}

// object returns stored bytes by object hash.
func (s *snapStore) object(h string) ([]byte, error) {
	if h == "" || strings.ContainsAny(h, "/\\.") {
		return nil, fmt.Errorf("%w: bad snapshot id", ErrInvalid)
	}
	b, err := os.ReadFile(s.objPath(h))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: snapshot %s", ErrNotFound, h)
	}
	return b, err
}

// byPlainHash returns the object for a plaintext content hash.
func (s *snapStore) byPlainHash(ph string) ([]byte, bool) {
	s.mu.Lock()
	h, ok := s.byPH[ph]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	b, err := s.object(h)
	return b, err == nil
}

// versions returns the entries of path (following renames backwards),
// newest first.
func (s *snapStore) versions(path string, limit int) ([]snapEntry, error) {
	s.mu.Lock()
	entries, err := s.readLog()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []snapEntry
	cur := path
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Path != cur {
			continue
		}
		out = append(out, e)
		if e.OldPath != "" {
			cur = e.OldPath
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out, nil
}

func (e snapEntry) time() time.Time { return time.UnixMilli(e.At) }
