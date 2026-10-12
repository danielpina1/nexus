package webtools

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Passage is a chunk of a page ranked against a question.
type Passage struct {
	Index   int     `json:"index"`
	Score   float64 `json:"score"`
	Heading string  `json:"heading,omitempty"`
	Text    string  `json:"text"`
}

// stopwords (English + Portuguese, the user's languages) ignored by BM25.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an and are as at be by for from has have how in is it its of on or that the
		this to was were what when where which who why will with do does did can about into than then there these
		those their them they you your we our i me my not no but if so such also more most other some any all
		o os as um uma uns umas de do da dos das em no na nos nas por para com sem que e ou se ao aos à às é
		foi são ser como mais mas qual quais quando onde quem porque pelo pela pelos pelas este esta isso isto
		esse essa seu sua seus suas há tem têm`) {
		m[foldWord(w)] = true
	}
	return m
}()

var folder = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

func foldWord(s string) string {
	out, _, err := transform.String(folder, strings.ToLower(s))
	if err != nil {
		return strings.ToLower(s)
	}
	return out
}

// tokenize lower-cases, folds accents, splits on non-alphanumerics, drops
// stopwords and applies a light plural stemmer.
func tokenize(s string) []string {
	s = foldWord(s)
	var out []string
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(w) < 2 || stopwords[w] {
			continue
		}
		out = append(out, stem(w))
	}
	return out
}

func stem(w string) string {
	n := len(w)
	switch {
	case n > 5 && strings.HasSuffix(w, "ies"):
		return w[:n-3] + "y"
	case n > 4 && strings.HasSuffix(w, "es") && !strings.HasSuffix(w, "ses"):
		return w[:n-2]
	case n > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us"):
		return w[:n-1]
	}
	return w
}

type chunk struct {
	heading string
	text    string
}

// splitPassages groups paragraphs into chunks of about size characters,
// remembering the closest heading of each chunk.
func splitPassages(text string, size int) []chunk {
	if size <= 0 {
		size = 800
	}
	var out []chunk
	var cur strings.Builder
	heading, curHeading := "", ""
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, chunk{heading: curHeading, text: t})
		}
		cur.Reset()
		curHeading = heading
	}
	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if strings.HasPrefix(para, "#") && !strings.Contains(para, "\n") {
			flush()
			heading = strings.TrimSpace(strings.TrimLeft(para, "#"))
			curHeading = heading
			continue
		}
		for _, piece := range splitLong(para, size) {
			if cur.Len() > 0 && cur.Len()+len(piece) > size {
				flush()
			}
			if cur.Len() > 0 {
				cur.WriteString("\n\n")
			}
			cur.WriteString(piece)
		}
	}
	flush()
	return out
}

// splitLong splits a paragraph longer than size at sentence ends.
func splitLong(p string, size int) []string {
	if len(p) <= size {
		return []string{p}
	}
	var out []string
	for len(p) > size {
		cut := -1
		for _, sep := range []string{". ", "? ", "! ", "; ", "\n", ", ", " "} {
			if i := strings.LastIndex(p[:size], sep); i > size/3 {
				cut = i + len(sep)
				break
			}
		}
		if cut < 0 {
			cut = size
			for cut < len(p) && !utf8.RuneStart(p[cut]) {
				cut++
			}
		}
		out = append(out, strings.TrimSpace(p[:cut]))
		p = strings.TrimSpace(p[cut:])
	}
	if p != "" {
		out = append(out, p)
	}
	return out
}

// RankPassages scores chunks against query with Okapi BM25 (k1=1.2, b=0.75)
// and returns the top k with a positive score, in document order.
func RankPassages(text, query string, size, k int) (top []Passage, total int) {
	chunks := splitPassages(text, size)
	total = len(chunks)
	q := tokenize(query)
	if len(chunks) == 0 || len(q) == 0 {
		return nil, total
	}
	docs := make([][]string, len(chunks))
	df := map[string]int{}
	avg := 0.0
	for i, c := range chunks {
		docs[i] = tokenize(c.heading + " " + c.text)
		avg += float64(len(docs[i]))
		seen := map[string]bool{}
		for _, t := range docs[i] {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	avg /= float64(len(chunks))
	if avg == 0 {
		avg = 1
	}
	qset := map[string]bool{}
	for _, t := range q {
		qset[t] = true
	}
	const k1, b = 1.2, 0.75
	n := float64(len(chunks))
	var scored []Passage
	for i, d := range docs {
		tf := map[string]int{}
		for _, t := range d {
			if qset[t] {
				tf[t]++
			}
		}
		s := 0.0
		for t := range qset {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (n-float64(df[t])+0.5)/(float64(df[t])+0.5))
			s += idf * f * (k1 + 1) / (f + k1*(1-b+b*float64(len(d))/avg))
		}
		if s > 0 {
			scored = append(scored, Passage{Index: i, Score: math.Round(s*1000) / 1000, Heading: chunks[i].heading, Text: chunks[i].text})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if k > 0 && len(scored) > k {
		scored = scored[:k]
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Index < scored[j].Index })
	return scored, total
}

// leadPassages returns the first k chunks (fallback when nothing matches).
func leadPassages(text string, size, k int) []Passage {
	var out []Passage
	for i, c := range splitPassages(text, size) {
		if i >= k {
			break
		}
		out = append(out, Passage{Index: i, Heading: c.heading, Text: c.text})
	}
	return out
}
