package vault

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// Facts are notes of type "fact" under facts/:
//
//	---
//	id: <ULID>
//	type: fact
//	subject: "[[João Silva]]"
//	predicate: works_at
//	object: "[[ACME]]"
//	confidence: 0.8
//	status: proposed          # proposed | confirmed | rejected
//	source: agent:librarian
//	valid_from: 2026-10-11
//	created: 2026-10-11T21:00:00Z
//	---
//	João Silva works_at ACME
//
// Proposed facts form the review queue (ListFacts/ReviewFact).

// FactInput is a fact proposal.
type FactInput struct {
	Subject    string  `json:"subject"`
	Predicate  string  `json:"predicate"`
	Object     string  `json:"object"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source,omitempty"`
	ValidFrom  string  `json:"valid_from,omitempty"`
}

// ProposeFact writes a fact note with status "proposed".
func (v *Vault) ProposeFact(ctx context.Context, in FactInput) (*nexusv1.Fact, error) {
	in.Subject, in.Predicate, in.Object = strings.TrimSpace(in.Subject), strings.TrimSpace(in.Predicate), strings.TrimSpace(in.Object)
	if in.Subject == "" || in.Predicate == "" || in.Object == "" {
		return nil, fmt.Errorf("%w: subject, predicate and object are required", ErrInvalid)
	}
	if in.Confidence < 0 || in.Confidence > 1 {
		return nil, fmt.Errorf("%w: confidence must be in [0,1]", ErrInvalid)
	}
	now := v.now()
	subj := asLink(ctx, v, in.Subject)
	keys := []string{"id", "type", "subject", "predicate", "object", "confidence", "status", "source", "created"}
	fields := map[string]any{
		"id": NewID(), "type": "fact", "subject": subj, "predicate": in.Predicate,
		"object": in.Object, "confidence": in.Confidence, "status": "proposed",
		"source": in.Source, "created": now.UTC().Format(time.RFC3339),
	}
	if in.Source == "" {
		delete(fields, "source")
	}
	if in.ValidFrom != "" {
		keys = append(keys, "valid_from")
		fields["valid_from"] = in.ValidFrom
	}
	body := fmt.Sprintf("%s %s %s\n", subj, in.Predicate, in.Object)
	md, err := note.Render(keys, fields, body)
	if err != nil {
		return nil, err
	}
	name := Slugify(stripLink(in.Subject)+" "+in.Predicate+" "+stripLink(in.Object), 60)
	rel, err := v.uniquePath("facts/" + name + ".md")
	if err != nil {
		return nil, err
	}
	n, err := v.WriteNote(ctx, rel, md, "fact: propose "+name, "")
	if err != nil {
		return nil, err
	}
	return v.factFromNote(ctx, n.Ref.Path)
}

// asLink wraps a subject in [[ ]] unless it already is a link.
func asLink(ctx context.Context, v *Vault, s string) string {
	if strings.HasPrefix(s, "[[") {
		return s
	}
	if e, ok := v.ix.Lookup(ctx, s); ok {
		return "[[" + linkName(ctx, v, e.Path) + "]]"
	}
	return "[[" + s + "]]"
}

func stripLink(s string) string {
	if ls := note.ExtractLinks(s); len(ls) > 0 {
		if ls[0].Alias != "" {
			return ls[0].Alias
		}
		return ls[0].Target
	}
	return s
}

// ParseFactStatus maps a frontmatter status to the enum.
func ParseFactStatus(s string) nexusv1.FactStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "proposed", "":
		return nexusv1.FactStatus_FACT_STATUS_PROPOSED
	case "confirmed":
		return nexusv1.FactStatus_FACT_STATUS_CONFIRMED
	case "rejected":
		return nexusv1.FactStatus_FACT_STATUS_REJECTED
	}
	return nexusv1.FactStatus_FACT_STATUS_UNSPECIFIED
}

func factStatusName(s nexusv1.FactStatus) string {
	switch s {
	case nexusv1.FactStatus_FACT_STATUS_PROPOSED:
		return "proposed"
	case nexusv1.FactStatus_FACT_STATUS_CONFIRMED:
		return "confirmed"
	case nexusv1.FactStatus_FACT_STATUS_REJECTED:
		return "rejected"
	}
	return ""
}

func (v *Vault) factFromNote(ctx context.Context, rel string) (*nexusv1.Fact, error) {
	b, rel, err := v.ReadRaw(ctx, rel)
	if err != nil {
		return nil, err
	}
	n := note.Parse(rel, b)
	if n.Type != "fact" {
		return nil, fmt.Errorf("%w: %s is not a fact", ErrNotFound, rel)
	}
	fm := n.Frontmatter
	f := &nexusv1.Fact{
		Id:        noteKey(n.ID, rel),
		Predicate: fmt.Sprint(orEmpty(fm["predicate"])),
		Object:    fmt.Sprint(orEmpty(fm["object"])),
		Status:    ParseFactStatus(fmt.Sprint(orEmpty(fm["status"]))),
		Source:    &nexusv1.NoteRef{Id: noteKey(n.ID, rel), Path: rel, Title: n.Title},
	}
	if c, err := strconv.ParseFloat(fmt.Sprint(orEmpty(fm["confidence"])), 32); err == nil {
		f.Confidence = float32(c)
	}
	subj := fmt.Sprint(orEmpty(fm["subject"]))
	if ls := note.ExtractLinks(subj); len(ls) > 0 {
		f.Subject = v.refFor(ctx, ls[0].Target, rel)
	} else {
		f.Subject = &nexusv1.NoteRef{Title: subj}
	}
	f.ValidFrom = parseTS(fm["valid_from"])
	f.ValidTo = parseTS(fm["valid_to"])
	return f, nil
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func parseTS(v any) *timestamppb.Timestamp {
	s, _ := v.(string)
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return timestamppb.New(t)
		}
	}
	return nil
}

// ListFacts returns facts with the given status (UNSPECIFIED = all), newest
// first. page_token is an offset.
func (v *Vault) ListFacts(ctx context.Context, status nexusv1.FactStatus, page *nexusv1.PageRequest) ([]*nexusv1.Fact, string, error) {
	entries, err := v.ix.ListByType(ctx, "fact")
	if err != nil {
		return nil, "", err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Updated.After(entries[j].Updated) })
	var all []*nexusv1.Fact
	for _, e := range entries {
		f, err := v.factFromNote(ctx, e.Path)
		if err != nil {
			continue
		}
		if status != nexusv1.FactStatus_FACT_STATUS_UNSPECIFIED && f.Status != status {
			continue
		}
		all = append(all, f)
	}
	size := int(page.GetPageSize())
	if size <= 0 || size > 200 {
		size = 50
	}
	off, _ := strconv.Atoi(page.GetPageToken())
	if off < 0 || off > len(all) {
		off = len(all)
	}
	end := min(off+size, len(all))
	next := ""
	if end < len(all) {
		next = strconv.Itoa(end)
	}
	return all[off:end], next, nil
}

// ReviewFact sets a fact's status (and optionally corrects its object).
// Confirming sets valid_from (today) when missing.
func (v *Vault) ReviewFact(ctx context.Context, id string, status nexusv1.FactStatus, correctedObject string) (*nexusv1.Fact, error) {
	name := factStatusName(status)
	if name == "" {
		return nil, fmt.Errorf("%w: status is required", ErrInvalid)
	}
	rel, err := v.locate(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := v.factFromNote(ctx, rel); err != nil {
		return nil, err
	}
	_, err = v.Update(ctx, rel, "fact: "+name+" "+rel, func(old []byte) ([]byte, error) {
		if old == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, rel)
		}
		fields := map[string]any{"status": name, "reviewed": v.now().UTC().Format(time.RFC3339)}
		if correctedObject != "" {
			fields["object"] = correctedObject
		}
		if status == nexusv1.FactStatus_FACT_STATUS_CONFIRMED {
			if n := note.Parse(rel, old); n.Frontmatter["valid_from"] == nil {
				fields["valid_from"] = v.now().Format("2006-01-02")
			}
		}
		return note.SetFields(old, fields)
	})
	if err != nil {
		return nil, err
	}
	return v.factFromNote(ctx, rel)
}
