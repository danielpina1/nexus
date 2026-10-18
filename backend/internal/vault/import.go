package vault

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// Clip is a web page fetched for a web clip capture.
type Clip struct {
	URL       string
	Title     string
	Byline    string
	SiteName  string
	Published string
	Text      string
}

// Markdown renders the clip body: the readable text and a source line.
func (c *Clip) Markdown(url string) string {
	var b strings.Builder
	if c.Title != "" {
		b.WriteString("# " + c.Title + "\n\n")
	}
	if text := strings.TrimSpace(c.Text); text != "" {
		b.WriteString(text + "\n\n")
	}
	src := c.URL
	if src == "" {
		src = url
	}
	b.WriteString("Source: " + src + "\n")
	return b.String()
}

// ImportOptions configure ImportNotes.
type ImportOptions struct {
	// Source is a directory on disk (an Obsidian vault or a Markdown folder).
	Source string
	// Dest is the vault folder to import into (default "imported/<name>").
	Dest string
	// Kind is "obsidian" or "markdown" (default: obsidian when Source has a
	// .obsidian/ folder). Both import the same way; Obsidian imports also
	// report the config folder as left untouched.
	Kind   string
	DryRun bool
}

// ImportResult reports what ImportNotes did.
type ImportResult struct {
	Notes, Attachments, IDsAssigned, IDsReplaced int
	Skipped, Warnings                            []string
}

// ImportNotes copies the notes and attachments of a folder into the vault.
// The source is never modified (its .obsidian/ config and .trash/ are not
// read). Notes get a frontmatter id when missing; ids that collide with
// existing notes (or with each other) are replaced. Existing vault files are
// never overwritten: the file is skipped and reported.
func (v *Vault) ImportNotes(ctx context.Context, o ImportOptions) (*ImportResult, error) {
	src, err := filepath.Abs(o.Source)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(src)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: import source %q is not a directory", ErrInvalid, o.Source)
	}
	if rel, err := filepath.Rel(v.root, src); err == nil && !strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("%w: import source is inside the vault", ErrInvalid)
	}
	kind := o.Kind
	if kind == "" {
		kind = "markdown"
		if st, err := os.Stat(filepath.Join(src, ".obsidian")); err == nil && st.IsDir() {
			kind = "obsidian"
		}
	}
	if kind != "obsidian" && kind != "markdown" {
		return nil, fmt.Errorf("%w: import kind must be obsidian or markdown", ErrInvalid)
	}
	dest := strings.Trim(o.Dest, "/")
	if dest == "" {
		dest = "imported/" + SafeFileName(filepath.Base(src))
	}
	if _, err := CleanPath(dest + "/x.md"); err != nil {
		return nil, err
	}
	res := &ImportResult{}
	if kind == "obsidian" {
		res.Warnings = append(res.Warnings, ".obsidian/ settings were left untouched and not imported")
	}
	ids := map[string]bool{}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == src {
				return err
			}
			res.Skipped = append(res.Skipped, p+": "+err.Error())
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if p != src && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			res.Skipped = append(res.Skipped, p+": symlink")
			return nil
		}
		relSrc, _ := filepath.Rel(src, p)
		relSrc = filepath.ToSlash(relSrc)
		if strings.HasPrefix(path.Base(relSrc), ".") {
			return nil
		}
		target, err := CleanPath(dest + "/" + relSrc)
		if err != nil {
			res.Skipped = append(res.Skipped, relSrc+": "+err.Error())
			return nil
		}
		isNote := index.IsNotePath(target)
		if !isNote && !index.IsAttachmentPath(target) {
			res.Skipped = append(res.Skipped, relSrc+": not a note or known attachment type")
			return nil
		}
		ap, err := v.abs(target)
		if err != nil {
			res.Skipped = append(res.Skipped, relSrc+": "+err.Error())
			return nil
		}
		if fileExists(ap) {
			res.Skipped = append(res.Skipped, relSrc+": "+target+" already exists")
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			res.Skipped = append(res.Skipped, relSrc+": "+err.Error())
			return nil
		}
		if !isNote {
			if int64(len(data)) > v.maxAttach {
				res.Skipped = append(res.Skipped, relSrc+": larger than the attachment limit")
				return nil
			}
			res.Attachments++
			if !o.DryRun {
				if err := v.writeFile(WithOrigin(ctx, "system"), target, data, "import "+relSrc); err != nil {
					res.Skipped = append(res.Skipped, relSrc+": "+err.Error())
				}
			}
			return nil
		}
		data = []byte(strings.ReplaceAll(string(data), "\r\n", "\n"))
		n := note.Parse(target, data)
		switch {
		case n.FrontmatterErr != nil:
			res.Warnings = append(res.Warnings, relSrc+": invalid frontmatter kept as is (no id assigned)")
		case n.ID == "":
			res.IDsAssigned++
		default:
			_, taken := v.ix.Lookup(ctx, n.ID)
			if taken || ids[strings.ToLower(n.ID)] {
				if out, err := note.SetFields(data, map[string]any{"id": NewID(), "imported_id": n.ID}, "id", "imported_id"); err == nil {
					data = out
					res.IDsReplaced++
				}
			}
		}
		if id := note.Parse(target, data).ID; id != "" {
			ids[strings.ToLower(id)] = true
		}
		res.Notes++
		if o.DryRun {
			return nil
		}
		if int64(len(data)) > v.maxNote {
			res.Skipped = append(res.Skipped, relSrc+": larger than the note limit")
			res.Notes--
			return nil
		}
		v.wmu.Lock()
		nn, err := v.writeLocked(WithOrigin(ctx, "system"), target, data, "import "+relSrc)
		v.wmu.Unlock()
		if err != nil {
			res.Skipped = append(res.Skipped, relSrc+": "+err.Error())
			res.Notes--
		} else if nn.GetRef().GetId() != "" {
			ids[strings.ToLower(nn.GetRef().GetId())] = true
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return res, err
	}
	return res, err
}
