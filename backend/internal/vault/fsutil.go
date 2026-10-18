package vault

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Crash safety
//
// Every file the vault writes goes through writeAtomic: the data is written
// to a temp file in the destination directory, fsynced, renamed over the
// destination, and then the directory itself is fsynced so the rename is
// durable. A crash leaves either the old or the new file, never a torn one;
// stray ".nexus-tmp-*" files are reported (and removed) by Check/Repair.
//
// Multi-file operations (rename with link updates, merge, split, trash
// restore) are recorded in a write-ahead journal first (journal.go).

// tempPrefix marks in-flight temp files.
const tempPrefix = ".nexus-tmp-"

// stateDirName is the vault's private folder (lock, journal). Hidden
// folders are never indexed, exposed or committed by the vault.
const stateDirName = ".nexus"

// writeAtomic writes data via a temp file + rename in the same directory and
// fsyncs the file and its directory.
func writeAtomic(p string, data []byte) error {
	return writeAtomicMode(p, data, 0o644)
}

func writeAtomicMode(p string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(p)
	if err := mkdirAllSync(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if _, err := f.Write(data); err != nil {
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
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir fsyncs a directory so renames/creates inside it are durable.
// Errors from platforms that cannot fsync directories are ignored.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) && !isUnsupported(err) {
		return fmt.Errorf("fsync %s: %w", dir, err)
	}
	return nil
}

func isUnsupported(err error) bool {
	s := err.Error()
	return strings.Contains(s, "not supported") || strings.Contains(s, "invalid argument") || strings.Contains(s, "bad file descriptor")
}

// mkdirAllSync creates dir (and parents) and fsyncs each new directory's
// parent so the new entries survive a crash.
func mkdirAllSync(dir string) error {
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		return nil
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := mkdirAllSync(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return syncDir(parent)
}

// renameDurable renames a file (creating the destination folder) and fsyncs
// both directories.
func renameDurable(from, to string) error {
	if err := mkdirAllSync(filepath.Dir(to)); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(to)); err != nil {
		return err
	}
	if filepath.Dir(from) != filepath.Dir(to) {
		return syncDir(filepath.Dir(from))
	}
	return nil
}

// copyFileAtomic copies src to dst atomically.
func copyFileAtomic(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	b, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	return writeAtomicMode(dst, b, mode)
}

// fileExists reports whether p exists (without following a final symlink).
func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
