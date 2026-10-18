package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Write-ahead journal for multi-file operations.
//
// A rename that rewrites links in 30 notes must not be left half done by a
// crash or power loss. Such operations are first recorded as an intent in
// <vault>/.nexus/journal/<id>/:
//
//	0.data, 1.data …   the new content of every file to write (fsynced)
//	manifest.json      the ordered list of operations (written last, atomically)
//
// manifest.json is the commit point. The operations are then applied; each
// is idempotent (write = atomic replace with the staged content, move =
// rename unless already done), and the journal directory is removed. On
// Open, journals with a manifest are rolled forward and journals without one
// (crash before the commit point: nothing was applied) are discarded.

type journalOp struct {
	// Kind is "write" (Path ← staged data) or "move" (From → Path).
	Kind string `json:"kind"`
	Path string `json:"path"`
	From string `json:"from,omitempty"`
	// Staged is the data file name inside the journal directory.
	Staged string `json:"staged,omitempty"`
	// Data is the content for writes (not serialised; staged on disk).
	Data []byte `json:"-"`
	// Mode is the file mode for writes (0 = 0644).
	Mode os.FileMode `json:"mode,omitempty"`
}

type journalManifest struct {
	ID      string      `json:"id"`
	Created time.Time   `json:"created"`
	Message string      `json:"message"`
	Ops     []journalOp `json:"ops"`
}

func (v *Vault) journalDir() string { return filepath.Join(v.root, stateDirName, "journal") }

// runJournal records ops durably, applies them and clears the journal.
// Paths are vault-relative (hidden paths such as .trash/ are allowed: the
// caller validated them). crashAfter is a test hook: apply only that many
// ops and return without clearing (simulates a crash).
func (v *Vault) runJournal(msg string, ops []journalOp) error {
	m := journalManifest{ID: NewID(), Created: v.now().UTC(), Message: msg}
	dir := filepath.Join(v.journalDir(), m.ID)
	if err := mkdirAllSync(dir); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	for i := range ops {
		if ops[i].Kind == "write" {
			name := strconv.Itoa(i) + ".data"
			if err := writeAtomic(filepath.Join(dir, name), ops[i].Data); err != nil {
				os.RemoveAll(dir)
				return fmt.Errorf("journal: stage: %w", err)
			}
			ops[i].Staged = name
		}
	}
	m.Ops = ops
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	if err := writeAtomic(filepath.Join(dir, "manifest.json"), b); err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("journal: commit: %w", err)
	}
	n := len(ops)
	if v.journalCrashAfter >= 0 {
		n = min(n, v.journalCrashAfter)
	}
	if err := v.applyOps(dir, ops[:n]); err != nil {
		// Leave the journal: recovery retries on the next Open.
		return fmt.Errorf("journal: apply (will be retried on restart): %w", err)
	}
	if n < len(ops) {
		return errSimulatedCrash
	}
	return v.clearJournal(dir)
}

var errSimulatedCrash = errors.New("journal: simulated crash")

func (v *Vault) applyOps(dir string, ops []journalOp) error {
	for _, op := range ops {
		dst := filepath.Join(v.root, filepath.FromSlash(op.Path))
		switch op.Kind {
		case "write":
			data, err := os.ReadFile(filepath.Join(dir, op.Staged))
			if err != nil {
				return err
			}
			mode := op.Mode
			if mode == 0 {
				mode = 0o644
			}
			if err := writeAtomicMode(dst, data, mode); err != nil {
				return err
			}
		case "move":
			src := filepath.Join(v.root, filepath.FromSlash(op.From))
			srcOK, dstOK := fileExists(src), fileExists(dst)
			switch {
			case srcOK && !dstOK:
				if err := renameDurable(src, dst); err != nil {
					return err
				}
			case !srcOK && dstOK:
				// already applied
			case srcOK && dstOK:
				return fmt.Errorf("move %s → %s: destination exists", op.From, op.Path)
			default:
				return fmt.Errorf("move %s → %s: source missing", op.From, op.Path)
			}
		default:
			return fmt.Errorf("unknown journal op %q", op.Kind)
		}
	}
	return nil
}

func (v *Vault) clearJournal(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dir))
}

// recoverJournal rolls forward committed journals and discards uncommitted
// ones. It returns the vault paths touched by replayed operations.
func (v *Vault) recoverJournal() ([]string, error) {
	ents, err := os.ReadDir(v.journalDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	var touched []string
	var firstErr error
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(v.journalDir(), e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			v.log.Warn("vault: discarding uncommitted journal", "id", e.Name())
			_ = v.clearJournal(dir)
			continue
		}
		var m journalManifest
		if err := json.Unmarshal(b, &m); err != nil {
			v.log.Error("vault: unreadable journal manifest; left in place", "id", e.Name(), "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := v.applyOps(dir, m.Ops); err != nil {
			v.log.Error("vault: journal replay failed; left in place", "id", e.Name(), "op", m.Message, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		v.log.Warn("vault: completed interrupted operation", "op", m.Message, "files", len(m.Ops))
		for _, op := range m.Ops {
			touched = append(touched, op.Path)
			if op.From != "" {
				touched = append(touched, op.From)
			}
		}
		_ = v.clearJournal(dir)
	}
	return touched, firstErr
}

// pendingJournals lists journal ids still on disk (Check).
func (v *Vault) pendingJournals() []string {
	ents, err := os.ReadDir(v.journalDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}
