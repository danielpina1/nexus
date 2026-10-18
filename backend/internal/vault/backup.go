package vault

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Backups
//
// A backup is a gzip'ed tar of the vault folder (everything except .git/,
// the lock and temp files; the .nexus/journal is included so a backup taken
// mid-operation is completed on restore) named
// nexus-vault-YYYYMMDD-HHMMSS.tar.gz, ending with a manifest
// (.nexus-backup.json: sha256 and size of every file) used by Verify. When
// the vault is a git repository a git bundle of the full history
// (nexus-vault-YYYYMMDD-HHMMSS.bundle) is written too. Files are written to
// a temp name, fsynced and renamed, so a backup folder never holds a torn
// archive. Retention keeps the newest backup of each of the last N days,
// weeks and months.
//
// The encryption key is never part of a backup: back it up separately.

// BackupManifestName is the last entry of every backup archive.
const BackupManifestName = ".nexus-backup.json"

// Retention is a grandfather-father-son policy.
type Retention struct {
	Daily   int `yaml:"daily"`
	Weekly  int `yaml:"weekly"`
	Monthly int `yaml:"monthly"`
}

// DefaultRetention keeps 7 daily, 4 weekly and 12 monthly backups.
var DefaultRetention = Retention{Daily: 7, Weekly: 4, Monthly: 12}

// BackupOptions configure CreateBackup.
type BackupOptions struct {
	Dir       string
	Retention Retention
	// NoBundle skips the git bundle.
	NoBundle bool
	// Remote, when set, is a git remote (URL or name) to push the current
	// branch to. Never forced: a rejected push is reported, not overwritten.
	Remote string
	Branch string
	Now    time.Time
	Logger interface{ Warn(string, ...any) }
}

// BackupInfo describes a backup file.
type BackupInfo struct {
	Name    string
	Path    string
	Kind    string // tar.gz | bundle
	Size    int64
	Created time.Time
	SHA256  string
}

// BackupResult reports CreateBackup.
type BackupResult struct {
	Created    []BackupInfo
	Pruned     []string
	PushResult string
}

type backupManifest struct {
	Version int                    `json:"version"`
	Created time.Time              `json:"created"`
	Source  string                 `json:"source"`
	Files   map[string]manifestEnt `json:"files"`
}

type manifestEnt struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

var reBackupName = regexp.MustCompile(`^nexus-vault-(\d{8}-\d{6})\.(tar\.gz|bundle)$`)

// CreateBackup writes a backup of the vault at root into o.Dir, applies
// retention and optionally pushes to a git remote. It only reads the vault
// (safe while the hub runs).
func CreateBackup(ctx context.Context, root string, o BackupOptions) (*BackupResult, error) {
	if o.Dir == "" {
		return nil, fmt.Errorf("%w: backup directory is not configured (vault.backup.dir)", ErrInvalid)
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.Retention == (Retention{}) {
		o.Retention = DefaultRetention
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, err
	}
	if r, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(r, "..") {
		return nil, fmt.Errorf("%w: backup directory must be outside the vault", ErrInvalid)
	}
	if err := mkdirAllSync(dir); err != nil {
		return nil, err
	}
	ts := o.Now.Format("20060102-150405")
	res := &BackupResult{}
	info, err := writeTarBackup(ctx, root, dir, "nexus-vault-"+ts+".tar.gz", o.Now)
	if err != nil {
		return nil, err
	}
	res.Created = append(res.Created, *info)
	if !o.NoBundle && isGitRepo(root) {
		if bi, err := writeBundle(ctx, root, dir, "nexus-vault-"+ts+".bundle", o.Now); err != nil {
			if o.Logger != nil {
				o.Logger.Warn("vault: git bundle skipped", "err", err)
			}
		} else {
			res.Created = append(res.Created, *bi)
		}
	}
	if pruned, err := PruneBackups(dir, o.Retention, o.Now); err == nil {
		res.Pruned = pruned
	}
	if o.Remote != "" {
		out, err := pushRemote(ctx, root, o.Remote, o.Branch)
		if err != nil {
			return res, fmt.Errorf("vault: backup written, but push failed: %w", err)
		}
		res.PushResult = out
	}
	return res, nil
}

func writeTarBackup(ctx context.Context, root, dir, name string, now time.Time) (*BackupInfo, error) {
	f, err := os.CreateTemp(dir, tempPrefix+"backup-*")
	if err != nil {
		return nil, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	sum := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(f, sum))
	tw := tar.NewWriter(gz)
	man := backupManifest{Version: 1, Created: now.UTC(), Source: filepath.Base(root), Files: map[string]manifestEnt{}}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped (Verify reports nothing for them)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == stateDirName+"/snapshots" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), tempPrefix) || rel == stateDirName+"/lock" {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		src, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer src.Close()
		hdr := &tar.Header{Name: rel, Mode: int64(fi.Mode().Perm()), Size: fi.Size(), ModTime: fi.ModTime(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(tw, h), io.LimitReader(src, fi.Size()))
		if err != nil {
			return err
		}
		if n != fi.Size() {
			return fmt.Errorf("%s changed size during backup", rel)
		}
		man.Files[rel] = manifestEnt{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
		return nil
	})
	if err != nil {
		f.Close()
		return nil, err
	}
	mb, _ := json.MarshalIndent(man, "", " ")
	if err := tw.WriteHeader(&tar.Header{Name: BackupManifestName, Mode: 0o644, Size: int64(len(mb)), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := tw.Write(mb); err != nil {
		f.Close()
		return nil, err
	}
	if err := tw.Close(); err != nil {
		f.Close()
		return nil, err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, name)
	if err := os.Chmod(tmp, 0o600); err != nil {
		return nil, err
	}
	if err := renameDurable(tmp, dst); err != nil {
		return nil, err
	}
	fi, _ := os.Stat(dst)
	return &BackupInfo{Name: name, Path: dst, Kind: "tar.gz", Size: fi.Size(), Created: now, SHA256: hex.EncodeToString(sum.Sum(nil))}, nil
}

func isGitRepo(root string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func gitCmd(ctx context.Context, root string, args ...string) (string, error) {
	g := &gitRepo{root: root}
	return g.run(ctx, args...)
}

func writeBundle(ctx context.Context, root, dir, name string, now time.Time) (*BackupInfo, error) {
	if _, err := gitCmd(ctx, root, "rev-parse", "-q", "--verify", "HEAD"); err != nil {
		return nil, fmt.Errorf("no commits yet")
	}
	tmp := filepath.Join(dir, tempPrefix+name)
	defer os.Remove(tmp)
	if _, err := gitCmd(ctx, root, "bundle", "create", "-q", tmp, "--all"); err != nil {
		return nil, err
	}
	f, err := os.Open(tmp)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, name)
	if err := renameDurable(tmp, dst); err != nil {
		return nil, err
	}
	fi, _ := os.Stat(dst)
	return &BackupInfo{Name: name, Path: dst, Kind: "bundle", Size: fi.Size(), Created: now, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// pushRemote pushes the current branch to a remote without forcing.
func pushRemote(ctx context.Context, root, remote, branch string) (string, error) {
	if strings.HasPrefix(remote, "-") {
		return "", fmt.Errorf("%w: bad remote", ErrInvalid)
	}
	if branch == "" {
		b, err := gitCmd(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return "", err
		}
		branch = strings.TrimSpace(b)
	}
	if strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " :") {
		return "", fmt.Errorf("%w: bad branch", ErrInvalid)
	}
	out, err := gitCmd(ctx, root, "push", "--porcelain", "--", remote, "HEAD:refs/heads/"+branch)
	if err != nil {
		return "", redactURL(err)
	}
	return redactString(strings.TrimSpace(out)), nil
}

var reURLCreds = regexp.MustCompile(`(https?://)[^/@\s]+@`)

func redactString(s string) string { return reURLCreds.ReplaceAllString(s, "$1***@") }
func redactURL(err error) error    { return errors.New(redactString(err.Error())) }

// ListBackups lists backups in dir, newest first.
func ListBackups(dir string) ([]BackupInfo, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []BackupInfo
	for _, e := range ents {
		m := reBackupName.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		t, err := time.ParseInLocation("20060102-150405", m[1], time.Local)
		if err != nil {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{Name: e.Name(), Path: filepath.Join(dir, e.Name()), Kind: m[2], Size: fi.Size(), Created: t})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// PruneBackups applies the retention policy and returns deleted names. The
// newest backup is always kept; a tar and bundle taken together share fate.
func PruneBackups(dir string, r Retention, now time.Time) ([]string, error) {
	all, err := ListBackups(dir)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	var stamps []time.Time
	seen := map[time.Time]bool{}
	for _, b := range all {
		if !seen[b.Created] {
			seen[b.Created] = true
			stamps = append(stamps, b.Created)
		}
	}
	keep := map[time.Time]bool{stamps[0]: true}
	pick := func(n int, key func(time.Time) string) {
		got := map[string]bool{}
		for _, t := range stamps {
			if len(got) >= n {
				break
			}
			k := key(t)
			if !got[k] {
				got[k] = true
				keep[t] = true
			}
		}
	}
	pick(r.Daily, func(t time.Time) string { return t.Format("2006-01-02") })
	pick(r.Weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-%02d", y, w) })
	pick(r.Monthly, func(t time.Time) string { return t.Format("2006-01") })
	var pruned []string
	for _, b := range all {
		if keep[b.Created] {
			continue
		}
		if err := os.Remove(b.Path); err == nil {
			pruned = append(pruned, b.Name)
		}
	}
	return pruned, nil
}

// VerifyBackup checks a backup: for archives, gzip/tar integrity and every
// file against the manifest; for bundles, `git bundle verify`.
func VerifyBackup(ctx context.Context, file string) (files int, problems []string, err error) {
	if strings.HasSuffix(file, ".bundle") {
		if _, err := exec.LookPath("git"); err != nil {
			return 0, nil, fmt.Errorf("git is needed to verify bundles")
		}
		out, err := exec.CommandContext(ctx, "git", "bundle", "verify", "-q", file).CombinedOutput()
		if err != nil {
			return 0, []string{strings.TrimSpace(string(out))}, nil
		}
		return 0, nil, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, []string{"not a gzip file: " + err.Error()}, nil
	}
	tr := tar.NewReader(gz)
	got := map[string]manifestEnt{}
	var man *backupManifest
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return files, append(problems, "corrupt archive: "+err.Error()), nil
		}
		if hdr.Name == BackupManifestName {
			var m backupManifest
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				problems = append(problems, "bad manifest: "+err.Error())
			} else {
				man = &m
			}
			continue
		}
		h := sha256.New()
		n, err := io.Copy(h, tr)
		if err != nil {
			return files, append(problems, "corrupt archive at "+hdr.Name+": "+err.Error()), nil
		}
		got[hdr.Name] = manifestEnt{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
		files++
	}
	if man == nil {
		return files, append(problems, "manifest missing (archive truncated?)"), nil
	}
	for name, want := range man.Files {
		have, ok := got[name]
		switch {
		case !ok:
			problems = append(problems, "missing: "+name)
		case have != want:
			problems = append(problems, "checksum mismatch: "+name)
		}
	}
	for name := range got {
		if _, ok := man.Files[name]; !ok {
			problems = append(problems, "not in manifest: "+name)
		}
	}
	sort.Strings(problems)
	return files, problems, nil
}

// RestoreBackup restores a backup into target. The archive is verified
// first. target must be missing or empty unless force is set, in which case
// the existing folder is moved aside to "<target>.before-restore-<time>".
// Bundles are restored with `git clone`.
func RestoreBackup(ctx context.Context, file, target string, force bool) (string, error) {
	if _, problems, err := VerifyBackup(ctx, file); err != nil {
		return "", err
	} else if len(problems) > 0 {
		return "", fmt.Errorf("backup failed verification: %s", strings.Join(problems, "; "))
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	aside := ""
	if ents, err := os.ReadDir(target); err == nil && len(ents) > 0 {
		if !force {
			return "", fmt.Errorf("%s is not empty (use -force to move it aside)", target)
		}
		aside = target + ".before-restore-" + time.Now().Format("20060102-150405")
		if err := os.Rename(target, aside); err != nil {
			return "", err
		}
	}
	if strings.HasSuffix(file, ".bundle") {
		_ = os.Remove(target)
		out, err := exec.CommandContext(ctx, "git", "clone", "-q", "--", file, target).CombinedOutput()
		if err != nil {
			return aside, fmt.Errorf("git clone: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return aside, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return aside, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return aside, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return aside, err
		}
		if hdr.Name == BackupManifestName || hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(hdr.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return aside, fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		dst := filepath.Join(target, filepath.FromSlash(name))
		b, err := io.ReadAll(tr)
		if err != nil {
			return aside, err
		}
		mode := os.FileMode(hdr.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := writeAtomicMode(dst, b, mode); err != nil {
			return aside, err
		}
		_ = os.Chtimes(dst, hdr.ModTime, hdr.ModTime)
	}
	return aside, nil
}

// Backup flushes pending commits and writes a backup (see CreateBackup).
func (v *Vault) Backup(ctx context.Context, o BackupOptions) (*BackupResult, error) {
	v.Flush()
	if o.Now.IsZero() {
		o.Now = v.now()
	}
	if o.Logger == nil {
		o.Logger = v.log
	}
	res, err := CreateBackup(ctx, v.root, o)
	if err != nil {
		v.notice(Notice{Kind: nexusv1NoticeAlert, Title: "Vault backup failed", Body: err.Error(), Data: map[string]any{"kind": "vault.backup_failed"}})
	}
	return res, err
}

const nexusv1NoticeAlert = nexusv1.NoticeKind_NOTICE_KIND_ALERT
