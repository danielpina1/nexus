package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
	"unicode"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// Capture writes a raw note into inbox/ (implements kernel.Vault):
//
//	inbox/20261011-210000-<slug>.md
//	---
//	id: <ULID>
//	type: capture
//	title: <first line or URL>
//	source: ios-share
//	created: 2026-10-11T21:00:00Z
//	url: https://…            (when given)
//	---
//	<text>
//
//	<url>
//	![[_attachments/20261011-210000-photo.jpg]]
//
// Attachments with inline data are saved under _attachments/. A repeated
// idempotency_key returns the note created the first time.
func (v *Vault) Capture(ctx context.Context, req *nexusv1.CaptureRequest) (*nexusv1.NoteRef, error) {
	text := strings.TrimSpace(req.GetText())
	url := strings.TrimSpace(req.GetUrl())
	if text == "" && url == "" && len(req.GetAttachments()) == 0 {
		return nil, fmt.Errorf("%w: capture needs text, url or attachments", ErrInvalid)
	}
	idemKey := ""
	if k := req.GetIdempotencyKey(); k != "" {
		idemKey = "capture:" + k
		if rel, ok := v.ix.KVGet(ctx, idemKey); ok {
			if n, err := v.Read(ctx, rel); err == nil {
				return n.Ref, nil
			}
		}
	}
	now := v.now()
	ts := now.Format("20060102-150405")
	title := captureTitle(text, url, req.GetAttachments())
	typ := "capture"
	var clip *Clip
	if req.GetClip() && url != "" {
		if v.Fetch == nil {
			v.log.Info("vault: web clip requested but no fetcher is available; saving the link only", "url", url)
		} else if c, err := v.Fetch(ctx, url); err != nil {
			v.log.Warn("vault: web clip failed; saving the link only", "url", url, "err", err)
		} else {
			clip = c
			typ = "resource"
			if c.Title != "" {
				title = truncate(c.Title, 80)
			}
		}
	}

	var body strings.Builder
	if text != "" {
		body.WriteString(text + "\n")
	}
	if url != "" && !strings.Contains(text, url) && clip == nil {
		if body.Len() > 0 {
			body.WriteString("\n")
		}
		body.WriteString(url + "\n")
	}
	if clip != nil {
		if body.Len() > 0 {
			body.WriteString("\n")
		}
		body.WriteString(clip.Markdown(url))
	}
	for i, a := range req.GetAttachments() {
		if body.Len() > 0 && i == 0 {
			body.WriteString("\n")
		}
		name := SafeFileName(a.GetName())
		if name == "" {
			name = fmt.Sprintf("attachment-%d%s", i+1, extForMime(a.GetMimeType()))
		}
		if len(a.GetData()) == 0 {
			if a.GetUrl() != "" {
				fmt.Fprintf(&body, "- [%s](%s)\n", name, a.GetUrl())
			}
			continue
		}
		rel, _, err := v.StoreAttachment(ctx, ts+"-"+name, a.GetData(), "capture attachment: "+name)
		if err != nil {
			return nil, fmt.Errorf("vault: save attachment: %w", err)
		}
		fmt.Fprintf(&body, "![[%s]]\n", rel)
	}

	source := req.GetSource()
	if source == "" {
		source = "api"
	}
	keys := []string{"id", "type", "title", "source", "created"}
	fields := map[string]any{
		"id": NewID(), "type": typ, "title": title, "source": source,
		"created": now.UTC().Format(time.RFC3339),
	}
	if url != "" {
		keys = append(keys, "url")
		fields["url"] = url
	}
	if clip != nil {
		for _, kv := range [][2]string{{"author", clip.Byline}, {"site", clip.SiteName}, {"published", clip.Published}} {
			if kv[1] != "" {
				keys = append(keys, kv[0])
				fields[kv[0]] = kv[1]
			}
		}
		keys = append(keys, "read")
		fields["read"] = false
	}
	md, err := note.Render(keys, fields, body.String())
	if err != nil {
		return nil, err
	}
	rel, err := v.uniquePath("inbox/" + ts + "-" + Slugify(title, 50) + ".md")
	if err != nil {
		return nil, err
	}
	n, err := v.WriteNote(ctx, rel, md, "capture: "+title, "")
	if err != nil {
		return nil, err
	}
	if idemKey != "" {
		if err := v.ix.KVPut(ctx, idemKey, n.Ref.Path); err != nil {
			v.log.Warn("vault: store idempotency key", "err", err)
		}
	}
	return n.Ref, nil
}

func captureTitle(text, url string, atts []*nexusv1.Attachment) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#-*> "))
		if line != "" {
			return truncate(line, 60)
		}
	}
	if url != "" {
		return truncate(url, 60)
	}
	if len(atts) > 0 && atts[0].GetName() != "" {
		return truncate(atts[0].GetName(), 60)
	}
	return "Capture"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// uniquePath returns rel, or rel with -2, -3… before the extension when a
// file already exists.
func (v *Vault) uniquePath(rel string) (string, error) {
	c, err := CleanPath(rel)
	if err != nil {
		return "", err
	}
	ext := path.Ext(c)
	base := strings.TrimSuffix(c, ext)
	for i := 1; i < 1000; i++ {
		cand := c
		if i > 1 {
			cand = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		p, err := v.abs(cand)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("vault: no free name for %s", rel)
}

// Slugify makes a lower-case ASCII file-name slug ("Café com João!" →
// "cafe-com-joao"), at most max bytes.
func Slugify(s string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range foldAccents.Replace(strings.ToLower(s)) {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if max > 0 && b.Len() >= max {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "note"
	}
	return out
}

var foldAccents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "æ", "ae",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o", "œ", "oe",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n", "ß", "ss", "ý", "y", "ÿ", "y",
)

// SafeFileName keeps a user-supplied file name usable on every OS.
func SafeFileName(name string) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32 || strings.ContainsRune(`<>:"/\|?*#^[]`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimLeft(b.String(), ". ")
	if len(out) > 120 {
		ext := path.Ext(out)
		out = out[:120-len(ext)] + ext
	}
	return out
}

func extForMime(m string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(m, ";")[0])) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/heic":
		return ".heic"
	case "image/gif":
		return ".gif"
	case "application/pdf":
		return ".pdf"
	case "audio/m4a", "audio/x-m4a", "audio/mp4":
		return ".m4a"
	case "audio/mpeg":
		return ".mp3"
	case "text/plain":
		return ".txt"
	case "video/mp4":
		return ".mp4"
	}
	return ".bin"
}

// DailyAppend appends text to daily/YYYY-MM-DD.md (creating it with
// frontmatter type: daily). Single-line text becomes a "- HH:MM text" bullet;
// multi-line text a "### HH:MM" section.
func (v *Vault) DailyAppend(ctx context.Context, text string, day time.Time) (*nexusv1.Note, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("%w: empty text", ErrInvalid)
	}
	now := v.now()
	if day.IsZero() {
		day = now
	}
	date := day.Format("2006-01-02")
	rel, err := v.PeriodicPath("daily", day)
	if err != nil {
		return nil, err
	}
	// Render the template (if any) before taking the write lock.
	fresh, err := v.periodicContent(ctx, "daily", day)
	if err != nil {
		return nil, err
	}
	return v.Update(ctx, rel, "daily: append "+date, func(old []byte) ([]byte, error) {
		if old == nil {
			old = fresh
		}
		s := string(old)
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		hm := now.Format("15:04")
		if strings.Contains(text, "\n") {
			s += "\n### " + hm + "\n\n" + text + "\n"
		} else {
			s += "\n- " + hm + " " + text + "\n"
		}
		return []byte(s), nil
	})
}

// Link adds a link from one note to another: an inline field
// "relation:: [[to]]" for typed relations, a plain "[[to]]" line otherwise.
// from and to are ids or paths; to may also be a not-yet-existing note name.
func (v *Vault) Link(ctx context.Context, from, to, relation string) (*nexusv1.Note, error) {
	src, err := v.locate(ctx, from)
	if err != nil {
		return nil, err
	}
	if _, _, err := v.ReadRaw(ctx, src); err != nil {
		return nil, err
	}
	target := strings.TrimSpace(to)
	if target == "" {
		return nil, fmt.Errorf("%w: empty link target", ErrInvalid)
	}
	if e, ok := v.ix.Lookup(ctx, target); ok {
		target = linkName(ctx, v, e.Path)
	} else if e, ok := v.ix.Resolve(ctx, target, src); ok {
		target = linkName(ctx, v, e.Path)
	} else {
		target = strings.TrimSuffix(target, ".md")
	}
	rel := note.NormalizeKey(relation)
	line := "[[" + target + "]]"
	if rel != "" && rel != note.RelLinksTo {
		line = relation + ":: " + line
		if strings.ContainsAny(relation, ":[]") {
			return nil, fmt.Errorf("%w: invalid relation %q", ErrInvalid, relation)
		}
	}
	return v.Update(ctx, src, fmt.Sprintf("link: %s → %s", src, target), func(old []byte) ([]byte, error) {
		if old == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, src)
		}
		s := string(old)
		if strings.Contains(s, line) {
			return old, nil
		}
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		return []byte(s + line + "\n"), nil
	})
}

// linkName is the shortest unambiguous link text for a note (its file name,
// or its path without .md when the name is ambiguous).
func linkName(ctx context.Context, v *Vault, rel string) string {
	name := strings.TrimSuffix(path.Base(rel), ".md")
	if e, ok := v.ix.Resolve(ctx, name, ""); ok && e.Path == rel {
		return name
	}
	return strings.TrimSuffix(rel, ".md")
}
