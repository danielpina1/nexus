package vault

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/internal/webtools"
)

// AttachmentsDir is where captured files are stored.
const AttachmentsDir = "_attachments"

// maxAttachmentText caps the extracted text kept per attachment.
const maxAttachmentText = 200_000

// extractAttachment is the index's attachment extractor: text from PDFs
// (webtools' built-in reader) and plain-text files, EXIF capture date from
// JPEG photos.
func extractAttachment(rel string, data []byte) (string, time.Time) {
	switch strings.ToLower(path.Ext(rel)) {
	case ".pdf":
		text, _ := webtools.ExtractPDFText(data)
		return clipText(text), time.Time{}
	case ".txt", ".csv", ".json":
		if utf8.Valid(data) {
			return clipText(string(data)), time.Time{}
		}
	case ".jpg", ".jpeg":
		if t, ok := ExifDate(data); ok {
			return "", t
		}
	}
	return "", time.Time{}
}

func clipText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxAttachmentText {
		s = s[:maxAttachmentText]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// ExifDate reads DateTimeOriginal (else DateTime) from a JPEG's EXIF block.
func ExifDate(b []byte) (time.Time, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return time.Time{}, false
	}
	p := 2
	for p+4 <= len(b) {
		if b[p] != 0xFF {
			return time.Time{}, false
		}
		marker := b[p+1]
		if marker == 0xD9 || marker == 0xDA { // EOI / start of scan
			break
		}
		size := int(binary.BigEndian.Uint16(b[p+2:]))
		if size < 2 || p+2+size > len(b) {
			break
		}
		seg := b[p+4 : p+2+size]
		if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return tiffDate(seg[6:])
		}
		p += 2 + size
	}
	return time.Time{}, false
}

func tiffDate(t []byte) (time.Time, bool) {
	if len(t) < 8 {
		return time.Time{}, false
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return time.Time{}, false
	}
	readIFD := func(off uint32) map[uint16][2]uint32 {
		out := map[uint16][2]uint32{}
		if int(off)+2 > len(t) {
			return out
		}
		n := int(bo.Uint16(t[off:]))
		for i := 0; i < n; i++ {
			e := int(off) + 2 + 12*i
			if e+12 > len(t) {
				break
			}
			tag := bo.Uint16(t[e:])
			count := bo.Uint32(t[e+4:])
			val := bo.Uint32(t[e+8:])
			out[tag] = [2]uint32{count, val}
		}
		return out
	}
	str := func(f [2]uint32) string {
		count, off := int(f[0]), int(f[1])
		if count <= 4 || off+count > len(t) {
			return ""
		}
		return strings.TrimRight(string(t[off:off+count]), "\x00 ")
	}
	parse := func(s string) (time.Time, bool) {
		tm, err := time.ParseInLocation("2006:01:02 15:04:05", s, time.Local)
		return tm, err == nil && !tm.IsZero() && tm.Year() > 1900
	}
	ifd0 := readIFD(bo.Uint32(t[4:]))
	if ptr, ok := ifd0[0x8769]; ok {
		if f, ok := readIFD(ptr[1])[0x9003]; ok {
			if tm, ok := parse(str(f)); ok {
				return tm, true
			}
		}
	}
	if f, ok := ifd0[0x0132]; ok {
		return parse(str(f))
	}
	return time.Time{}, false
}

// StoreAttachment saves data under _attachments/ with content-addressed
// deduplication: when a file with the same SHA-256 already exists anywhere
// in the vault, its path is returned and nothing is written.
func (v *Vault) StoreAttachment(ctx context.Context, name string, data []byte, msg string) (rel string, deduped bool, err error) {
	if int64(len(data)) > v.maxAttach {
		return "", false, fmt.Errorf("%w: %s is %d bytes (limit %d)", ErrTooLarge, name, len(data), v.maxAttach)
	}
	h := index.Hash(data)
	if same, err := v.ix.ListAttachments(ctx, index.AttachmentFilter{Hash: h, Limit: 1}); err == nil && len(same) > 0 {
		if p, err := v.abs(same[0].Path); err == nil && fileExists(p) {
			return same[0].Path, true, nil
		}
	}
	rel, err = v.uniquePath(AttachmentsDir + "/" + name)
	if err != nil {
		return "", false, err
	}
	if err := v.writeFile(ctx, rel, data, msg); err != nil {
		return "", false, err
	}
	return rel, false, nil
}

// AttachmentFilter narrows ListAttachments.
type AttachmentFilter struct {
	Prefix       string
	OrphanedOnly bool
	Query        string
	Limit        int
}

// AttachmentInfo is an attachment with its referencing notes.
type AttachmentInfo struct {
	index.Attachment
	ReferencedBy []string
}

// ListAttachments lists indexed attachments with the notes that embed or
// link them.
func (v *Vault) ListAttachments(ctx context.Context, f AttachmentFilter) ([]AttachmentInfo, error) {
	atts, err := v.ix.ListAttachments(ctx, index.AttachmentFilter{Prefix: f.Prefix, Query: f.Query})
	if err != nil {
		return nil, err
	}
	refs, err := v.ix.AttachmentLinks(ctx)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	var out []AttachmentInfo
	for _, a := range atts {
		info := AttachmentInfo{Attachment: *a, ReferencedBy: attachmentRefs(refs, a.Path)}
		if f.OrphanedOnly && len(info.ReferencedBy) > 0 {
			continue
		}
		out = append(out, info)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// attachmentRefs finds the notes linking to an attachment: links by full
// path, by a path suffix, or by bare file name (Obsidian's shortest form).
func attachmentRefs(refs map[string][]string, rel string) []string {
	l := strings.ToLower(rel)
	seen := map[string]bool{}
	var out []string
	for target, srcs := range refs {
		t := strings.TrimPrefix(target, "/")
		if t == l || strings.HasSuffix(l, "/"+t) || path.Base(l) == t {
			for _, s := range srcs {
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func (v *Vault) attachmentProto(ctx context.Context, a AttachmentInfo) *nexusv1.AttachmentInfo {
	out := &nexusv1.AttachmentInfo{
		Path: a.Path, MimeType: a.MIME, SizeBytes: a.Size, Sha256: a.Hash,
		ModifiedAt: timestamppb.New(a.Modified), HasText: a.Text != "",
		TextExcerpt: truncate(strings.Join(strings.Fields(a.Text), " "), 200),
	}
	if !a.Taken.IsZero() {
		out.TakenAt = timestamppb.New(a.Taken)
	}
	for _, p := range a.ReferencedBy {
		if e, ok := v.ix.Get(ctx, p); ok {
			out.ReferencedBy = append(out.ReferencedBy, entryRef(e))
		} else {
			out.ReferencedBy = append(out.ReferencedBy, &nexusv1.NoteRef{Path: p, Title: p})
		}
	}
	return out
}
