package vault

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// Templates live in templates/ (TemplatesDir). Variables use {{…}}:
//
//	{{title}}                  the new note's title
//	{{date}} {{time}}          today (YYYY-MM-DD) / now (HH:mm)
//	{{date:dddd D MMMM YYYY}}  moment.js-style format (as in Obsidian)
//	{{time:HH:mm:ss}}
//	{{now}}                    RFC 3339 timestamp
//	{{yesterday}} {{tomorrow}} YYYY-MM-DD (relative to the note's date)
//	{{id}}                     the new note's id
//	{{prompt:Question}}        must be answered: variables["Question"]
//	{{ask:name}}               same, keyed by name
//	{{anything}}               variables["anything"] (empty when missing)
//
// Periodic notes (daily, weekly, monthly, yearly) are created from
// templates/<period>.md when it exists, in folders named by vault config.

// DefaultTemplatesDir is the template folder.
const DefaultTemplatesDir = "templates"

var reTemplateVar = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// Template describes a template note.
type Template struct {
	Path      string
	Name      string
	Type      string
	Variables []string
	Prompts   []string
}

// ListTemplates lists the notes under templates/.
func (v *Vault) ListTemplates(ctx context.Context) ([]Template, error) {
	es, err := v.ix.ListPrefix(ctx, v.templatesDir()+"/")
	if err != nil {
		return nil, err
	}
	var out []Template
	for _, e := range es {
		b, _, err := v.ReadRaw(ctx, e.Path)
		if err != nil {
			continue
		}
		vars, prompts := templateVars(string(b))
		out = append(out, Template{Path: e.Path, Name: strings.TrimSuffix(path.Base(e.Path), ".md"), Type: e.Type, Variables: vars, Prompts: prompts})
	}
	return out, nil
}

func (v *Vault) templatesDir() string {
	if v.templates != "" {
		return v.templates
	}
	return DefaultTemplatesDir
}

func templateVars(s string) (vars, prompts []string) {
	seenV, seenP := map[string]bool{}, map[string]bool{}
	for _, m := range reTemplateVar.FindAllStringSubmatch(s, -1) {
		expr := m[1]
		if k, ok := promptKey(expr); ok {
			if !seenP[k] {
				seenP[k] = true
				prompts = append(prompts, k)
			}
			continue
		}
		name := expr
		if i := strings.Index(name, ":"); i >= 0 {
			name = name[:i]
		}
		if !seenV[name] {
			seenV[name] = true
			vars = append(vars, name)
		}
	}
	return vars, prompts
}

func promptKey(expr string) (string, bool) {
	for _, p := range []string{"prompt:", "ask:"} {
		if k, ok := strings.CutPrefix(expr, p); ok {
			return strings.TrimSpace(k), true
		}
	}
	return "", false
}

// TemplateData is the input of RenderTemplate.
type TemplateData struct {
	Title string
	Date  time.Time // the note's date (periodic notes); zero = Now
	Now   time.Time
	ID    string
	Vars  map[string]string
}

// MissingPromptsError lists prompts that need answers.
type MissingPromptsError struct{ Prompts []string }

func (e *MissingPromptsError) Error() string {
	return "vault: template needs answers for: " + strings.Join(e.Prompts, ", ")
}

func (e *MissingPromptsError) Unwrap() error { return ErrInvalid }

// RenderTemplate expands template variables.
func RenderTemplate(tmpl string, d TemplateData) (string, error) {
	if d.Now.IsZero() {
		d.Now = time.Now()
	}
	date := d.Date
	if date.IsZero() {
		date = d.Now
	}
	var missing []string
	out := reTemplateVar.ReplaceAllStringFunc(tmpl, func(m string) string {
		expr := strings.TrimSpace(m[2 : len(m)-2])
		if k, ok := promptKey(expr); ok {
			if val, ok := d.Vars[k]; ok {
				return val
			}
			missing = append(missing, k)
			return m
		}
		name, arg, hasArg := strings.Cut(expr, ":")
		name = strings.TrimSpace(name)
		if val, ok := d.Vars[name]; ok && !hasArg {
			return val
		}
		switch strings.ToLower(name) {
		case "title":
			return d.Title
		case "date":
			if hasArg {
				return FormatMoment(date, strings.TrimSpace(arg))
			}
			return date.Format("2006-01-02")
		case "time":
			if hasArg {
				return FormatMoment(d.Now, strings.TrimSpace(arg))
			}
			return d.Now.Format("15:04")
		case "now":
			return d.Now.Format(time.RFC3339)
		case "yesterday":
			return date.AddDate(0, 0, -1).Format("2006-01-02")
		case "tomorrow":
			return date.AddDate(0, 0, 1).Format("2006-01-02")
		case "id":
			return d.ID
		}
		return d.Vars[name]
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", &MissingPromptsError{Prompts: uniqueStrings(missing, "")}
	}
	return out, nil
}

// findTemplate resolves a template by path or name.
func (v *Vault) findTemplate(ctx context.Context, name string) (string, []byte, error) {
	cands := []string{name}
	dir := v.templatesDir()
	if !strings.HasPrefix(name, dir+"/") {
		cands = append(cands, dir+"/"+name)
	}
	for _, c := range cands {
		if b, rel, err := v.ReadRaw(ctx, c); err == nil {
			return rel, b, nil
		}
	}
	return "", nil, fmt.Errorf("%w: template %q", ErrNotFound, name)
}

// NewFromTemplate creates a note from a template. dest defaults to
// "inbox/<title>.md"; an existing dest is an error.
func (v *Vault) NewFromTemplate(ctx context.Context, tmpl, dest, title string, vars map[string]string, msg string) (*nexusv1.Note, error) {
	trel, tb, err := v.findTemplate(ctx, tmpl)
	if err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if dest == "" {
		if title == "" {
			return nil, fmt.Errorf("%w: title or path is required", ErrInvalid)
		}
		dest = "inbox/" + SafeFileName(title) + ".md"
	}
	c, err := notePath(dest)
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = strings.TrimSuffix(path.Base(c), ".md")
	}
	id := NewID()
	text, err := RenderTemplate(string(tb), TemplateData{Title: title, Now: v.now(), ID: id, Vars: vars})
	if err != nil {
		return nil, err
	}
	text = stripTemplateType(text, trel)
	if _, _, err := v.ReadRaw(ctx, c); err == nil {
		return nil, fmt.Errorf("%w: %s already exists", ErrInvalid, c)
	}
	out := withID(text, id)
	if msg == "" {
		msg = fmt.Sprintf("new %s from %s", c, trel)
	}
	return v.WriteNote(ctx, c, out, msg, "")
}

// stripTemplateType drops "type: template" from a rendered template and
// replaces an id copied from the template with the new note's id.
func stripTemplateType(text, _ string) string {
	n := note.Parse("", []byte(text))
	fields := map[string]any{}
	if strings.EqualFold(n.Type, "template") {
		fields["type"] = nil
	}
	if len(fields) > 0 {
		if out, err := note.SetFields([]byte(text), fields); err == nil {
			return string(out)
		}
	}
	return text
}

// withID sets the frontmatter id (adding frontmatter when missing).
func withID(text, id string) []byte {
	n := note.Parse("", []byte(text))
	if n.ID == "" {
		out, _, _ := note.EnsureID([]byte(text), func() string { return id })
		return out
	}
	if n.ID == id || n.FrontmatterErr != nil {
		return []byte(text)
	}
	if out, err := note.SetFields([]byte(text), map[string]any{"id": id}, "id"); err == nil {
		return out
	}
	return []byte(text)
}

// ---------------------------------------------------------------------------
// Periodic notes

// PeriodicConfig configures one kind of periodic note.
type PeriodicConfig struct {
	Folder   string `yaml:"folder"`
	Format   string `yaml:"format"`   // moment.js format of the file name
	Template string `yaml:"template"` // template path; used when it exists
}

// DefaultPeriodic are the Obsidian "Periodic Notes" defaults.
var DefaultPeriodic = map[string]PeriodicConfig{
	"daily":   {Folder: "daily", Format: "YYYY-MM-DD", Template: "templates/daily.md"},
	"weekly":  {Folder: "weekly", Format: "GGGG-[W]WW", Template: "templates/weekly.md"},
	"monthly": {Folder: "monthly", Format: "YYYY-MM", Template: "templates/monthly.md"},
	"yearly":  {Folder: "yearly", Format: "YYYY", Template: "templates/yearly.md"},
}

// SetPeriodic overrides periodic note settings (empty fields keep defaults).
func (v *Vault) SetPeriodic(cfg map[string]PeriodicConfig) {
	m := map[string]PeriodicConfig{}
	for k, d := range DefaultPeriodic {
		c := cfg[k]
		if c.Folder == "" {
			c.Folder = d.Folder
		}
		if c.Format == "" {
			c.Format = d.Format
		}
		if c.Template == "" {
			c.Template = d.Template
		}
		m[k] = c
	}
	v.periodic = m
}

func (v *Vault) periodicConfig(period string) (PeriodicConfig, bool) {
	if v.periodic != nil {
		c, ok := v.periodic[period]
		return c, ok
	}
	c, ok := DefaultPeriodic[period]
	return c, ok
}

// periodStart returns the first day of the period containing t.
func periodStart(period string, t time.Time) time.Time {
	y, m, d := t.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	switch period {
	case "weekly":
		wd := int(day.Weekday())
		if wd == 0 {
			wd = 7
		}
		return day.AddDate(0, 0, -(wd - 1))
	case "monthly":
		return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
	case "yearly":
		return time.Date(y, 1, 1, 0, 0, 0, 0, t.Location())
	}
	return day
}

// PeriodicPath returns the path of the periodic note for a date.
func (v *Vault) PeriodicPath(period string, date time.Time) (string, error) {
	c, ok := v.periodicConfig(period)
	if !ok {
		return "", fmt.Errorf("%w: period must be daily, weekly, monthly or yearly", ErrInvalid)
	}
	name := FormatMoment(periodStart(period, date), c.Format)
	rel := strings.Trim(c.Folder, "/")
	if rel != "" {
		rel += "/"
	}
	return notePath(rel + name)
}

// PeriodicNote returns the periodic note for date (zero = today). With
// create it is created from its template (or a minimal note) when missing.
func (v *Vault) PeriodicNote(ctx context.Context, period string, date time.Time, create bool) (*nexusv1.Note, string, bool, error) {
	if date.IsZero() {
		date = v.now()
	}
	rel, err := v.PeriodicPath(period, date)
	if err != nil {
		return nil, "", false, err
	}
	if n, err := v.Read(ctx, rel); err == nil {
		return n, rel, false, nil
	}
	if !create {
		return nil, rel, false, nil
	}
	text, err := v.periodicContent(ctx, period, date)
	if err != nil {
		return nil, rel, false, err
	}
	created := false
	n, err := v.Update(ctx, rel, period+" note "+strings.TrimSuffix(path.Base(rel), ".md"), func(old []byte) ([]byte, error) {
		if old != nil {
			return old, nil
		}
		created = true
		return text, nil
	})
	return n, rel, created, err
}

// periodicContent renders the template for a new periodic note.
func (v *Vault) periodicContent(ctx context.Context, period string, date time.Time) ([]byte, error) {
	c, _ := v.periodicConfig(period)
	start := periodStart(period, date)
	title := FormatMoment(start, c.Format)
	id := NewID()
	if c.Template != "" {
		if tb, _, err := v.ReadRaw(ctx, c.Template); err == nil {
			text, err := RenderTemplate(string(tb), TemplateData{Title: title, Date: start, Now: v.now(), ID: id})
			if err != nil {
				return nil, err
			}
			return withID(stripTemplateType(text, c.Template), id), nil
		}
	}
	return note.Render([]string{"id", "type", "title", "created"}, map[string]any{
		"id": id, "type": period, "title": title, "created": v.now().UTC().Format(time.RFC3339),
	}, "# "+title+"\n")
}

// ---------------------------------------------------------------------------
// moment.js formats

var momentTokens = []string{
	"YYYY", "YY", "MMMM", "MMM", "MM", "M", "DDDD", "DDD", "DD", "Do", "D",
	"dddd", "ddd", "dd", "d", "GGGG", "gggg", "WW", "W", "ww", "w", "HH", "H", "hh", "h",
	"mm", "m", "ss", "s", "A", "a", "Q", "X", "x",
}

// FormatMoment formats t with a moment.js format string (the syntax
// Obsidian uses for daily-note names). [text] is literal.
func FormatMoment(t time.Time, f string) string {
	var b strings.Builder
	for i := 0; i < len(f); {
		if f[i] == '[' {
			if j := strings.IndexByte(f[i:], ']'); j > 0 {
				b.WriteString(f[i+1 : i+j])
				i += j + 1
				continue
			}
		}
		matched := false
		for _, tok := range momentTokens {
			if strings.HasPrefix(f[i:], tok) {
				b.WriteString(momentToken(t, tok))
				i += len(tok)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(f[i])
			i++
		}
	}
	return b.String()
}

func momentToken(t time.Time, tok string) string {
	isoY, isoW := t.ISOWeek()
	pad := func(n int) string { return fmt.Sprintf("%02d", n) }
	h12 := t.Hour() % 12
	if h12 == 0 {
		h12 = 12
	}
	switch tok {
	case "YYYY":
		return fmt.Sprintf("%04d", t.Year())
	case "YY":
		return pad(t.Year() % 100)
	case "MMMM":
		return t.Month().String()
	case "MMM":
		return t.Month().String()[:3]
	case "MM":
		return pad(int(t.Month()))
	case "M":
		return strconv.Itoa(int(t.Month()))
	case "DDDD":
		return fmt.Sprintf("%03d", t.YearDay())
	case "DDD":
		return strconv.Itoa(t.YearDay())
	case "DD":
		return pad(t.Day())
	case "Do":
		return ordinal(t.Day())
	case "D":
		return strconv.Itoa(t.Day())
	case "dddd":
		return t.Weekday().String()
	case "ddd":
		return t.Weekday().String()[:3]
	case "dd":
		return t.Weekday().String()[:2]
	case "d":
		return strconv.Itoa(int(t.Weekday()))
	case "GGGG", "gggg":
		return fmt.Sprintf("%04d", isoY)
	case "WW", "ww":
		return pad(isoW)
	case "W", "w":
		return strconv.Itoa(isoW)
	case "HH":
		return pad(t.Hour())
	case "H":
		return strconv.Itoa(t.Hour())
	case "hh":
		return pad(h12)
	case "h":
		return strconv.Itoa(h12)
	case "mm":
		return pad(t.Minute())
	case "m":
		return strconv.Itoa(t.Minute())
	case "ss":
		return pad(t.Second())
	case "s":
		return strconv.Itoa(t.Second())
	case "A":
		if t.Hour() < 12 {
			return "AM"
		}
		return "PM"
	case "a":
		if t.Hour() < 12 {
			return "am"
		}
		return "pm"
	case "Q":
		return strconv.Itoa((int(t.Month())-1)/3 + 1)
	case "X":
		return strconv.FormatInt(t.Unix(), 10)
	case "x":
		return strconv.FormatInt(t.UnixMilli(), 10)
	}
	return tok
}

func ordinal(n int) string {
	suf := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suf = "st"
	case n%10 == 2:
		suf = "nd"
	case n%10 == 3:
		suf = "rd"
	}
	return strconv.Itoa(n) + suf
}
