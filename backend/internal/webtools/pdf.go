package webtools

import (
	"bytes"
	"compress/zlib"
	"io"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// PDF notes returned in FetchResult.Note.
const (
	pdfNoteOK   = "PDF text extracted with a best-effort built-in reader: layout, tables and some fonts may be lost. Use the pdf tool when exact text matters."
	pdfNoteFail = "PDF: no extractable text with the built-in reader (scanned, encrypted, or custom font encodings). Use the pdf tool to read it."
)

var (
	reStream     = regexp.MustCompile(`stream\r?\n`)
	rePDFTitle   = regexp.MustCompile(`/Title\s*\(((?:\\.|[^\\)])*)\)`)
	maxPDFInflat = int64(32 << 20)
)

// ExtractPDFText pulls text out of simple PDFs (FlateDecode or plain content
// streams, Tj/TJ operators, standard 8-bit encodings). It never fails: when
// the result does not look like text it returns "" and a note.
func ExtractPDFText(b []byte) (string, string) {
	if bytes.Contains(b, []byte("/Encrypt")) {
		return "", pdfNoteFail
	}
	var out strings.Builder
	budget := maxPDFInflat
	for _, loc := range reStream.FindAllIndex(b, -1) {
		start := loc[1]
		end := bytes.Index(b[start:], []byte("endstream"))
		if end < 0 {
			break
		}
		data := b[start : start+end]
		dictStart := loc[0] - 1024
		if dictStart < 0 {
			dictStart = 0
		}
		dict := b[dictStart:loc[0]]
		if i := bytes.LastIndex(dict, []byte("obj")); i >= 0 {
			dict = dict[i:]
		}
		if skipStream(dict) {
			continue
		}
		if bytes.Contains(dict, []byte("/FlateDecode")) {
			zr, err := zlib.NewReader(bytes.NewReader(data))
			if err != nil {
				continue
			}
			dec, _ := io.ReadAll(io.LimitReader(zr, budget))
			zr.Close()
			budget -= int64(len(dec))
			data = dec
		} else if bytes.Contains(dict, []byte("/Filter")) {
			continue // other filters (images, LZW, ASCII85…) are not handled
		}
		if t := contentText(data); t != "" {
			out.WriteString(t)
			out.WriteString("\n\n")
		}
		if budget <= 0 {
			break
		}
	}
	text := cleanPDFText(out.String())
	if !looksLikeText(text) {
		return "", pdfNoteFail
	}
	return text, pdfNoteOK
}

func skipStream(dict []byte) bool {
	for _, k := range []string{"/Image", "/XRef", "/ObjStm", "/FontFile", "/Length1", "/Type1C", "/CIDFontType0C", "/OpenType", "/Metadata", "/ICCBased", "/EmbeddedFile"} {
		if bytes.Contains(dict, []byte(k)) {
			return true
		}
	}
	return false
}

// contentText interprets the text operators of a content stream.
func contentText(s []byte) string {
	var out strings.Builder
	var operands []any // string | float64 | []any (array)
	var arr []any
	inArr := false
	push := func(v any) {
		if inArr {
			arr = append(arr, v)
		} else {
			operands = append(operands, v)
		}
	}
	lastStr := func() (string, bool) {
		for i := len(operands) - 1; i >= 0; i-- {
			if s, ok := operands[i].(string); ok {
				return s, true
			}
		}
		return "", false
	}
	sawText := false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '%':
			for i < len(s) && s[i] != '\n' && s[i] != '\r' {
				i++
			}
		case c == '(':
			str, n := readLiteral(s[i:])
			push(str)
			i += n
		case c == '<' && i+1 < len(s) && s[i+1] == '<':
			i += 2
		case c == '>' && i+1 < len(s) && s[i+1] == '>':
			i += 2
		case c == '<':
			j := bytes.IndexByte(s[i:], '>')
			if j < 0 {
				return out.String()
			}
			push(decodeHex(s[i+1 : i+j]))
			i += j + 1
		case c == '[':
			inArr, arr = true, nil
			i++
		case c == ']':
			if inArr {
				inArr = false
				operands = append(operands, arr)
			}
			i++
		case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(s) && (s[j] == '.' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			f, _ := strconv.ParseFloat(string(s[i:j]), 64)
			push(f)
			i = j
		case c == '/':
			j := i + 1
			for j < len(s) && !isPDFDelim(s[j]) {
				j++
			}
			i = j // names are not text
		case isPDFSpace(c):
			i++
		default:
			j := i
			for j < len(s) && !isPDFDelim(s[j]) {
				j++
			}
			if j == i {
				i++
				continue
			}
			op := string(s[i:j])
			i = j
			switch op {
			case "Tj":
				if t, ok := lastStr(); ok {
					out.WriteString(t)
					sawText = true
				}
			case "'", "\"":
				out.WriteByte('\n')
				if t, ok := lastStr(); ok {
					out.WriteString(t)
					sawText = true
				}
			case "TJ":
				if len(operands) > 0 {
					if a, ok := operands[len(operands)-1].([]any); ok {
						for _, v := range a {
							switch t := v.(type) {
							case string:
								out.WriteString(t)
								sawText = true
							case float64:
								if t < -250 {
									out.WriteByte(' ')
								}
							}
						}
					}
				}
			case "Td", "TD":
				if len(operands) >= 2 {
					if ty, ok := operands[len(operands)-1].(float64); ok && ty != 0 {
						out.WriteByte('\n')
					} else {
						out.WriteByte(' ')
					}
				}
			case "T*", "Tm", "ET":
				out.WriteByte('\n')
			}
			operands = operands[:0]
		}
	}
	if !sawText {
		return ""
	}
	return out.String()
}

func isPDFSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

func isPDFDelim(c byte) bool {
	return isPDFSpace(c) || strings.IndexByte("()<>[]{}/%", c) >= 0
}

// readLiteral decodes a (…) string starting at s[0]=='('; returns the text and
// bytes consumed.
func readLiteral(s []byte) (string, int) {
	var b []byte
	depth := 0
	i := 0
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			e := s[i]
			switch e {
			case 'n':
				b = append(b, '\n')
			case 'r':
				b = append(b, '\r')
			case 't':
				b = append(b, '\t')
			case 'b', 'f':
			case '\r', '\n':
				if e == '\r' && i+1 < len(s) && s[i+1] == '\n' {
					i++
				}
			default:
				if e >= '0' && e <= '7' {
					v, n := 0, 0
					for n < 3 && i < len(s) && s[i] >= '0' && s[i] <= '7' {
						v = v*8 + int(s[i]-'0')
						i++
						n++
					}
					i--
					b = append(b, byte(v))
				} else {
					b = append(b, e)
				}
			}
		case c == '(':
			if depth > 0 {
				b = append(b, c)
			}
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return bytesToText(b), i + 1
			}
			b = append(b, c)
		default:
			b = append(b, c)
		}
	}
	return bytesToText(b), i
}

func decodeHex(h []byte) string {
	var clean []byte
	for _, c := range h {
		if !isPDFSpace(c) {
			clean = append(clean, c)
		}
	}
	if len(clean)%2 == 1 {
		clean = append(clean, '0')
	}
	b := make([]byte, 0, len(clean)/2)
	for i := 0; i+1 < len(clean); i += 2 {
		v, err := strconv.ParseUint(string(clean[i:i+2]), 16, 8)
		if err != nil {
			return ""
		}
		b = append(b, byte(v))
	}
	return bytesToText(b)
}

// bytesToText maps 8-bit string bytes to runes (UTF-16BE with BOM, a
// 2-byte encoding of Latin text, or Latin-1/PDFDocEncoding).
func bytesToText(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		var r []rune
		for i := 2; i+1 < len(b); i += 2 {
			r = append(r, rune(b[i])<<8|rune(b[i+1]))
		}
		return string(r)
	}
	if len(b) >= 4 && len(b)%2 == 0 {
		zeros := 0
		for i := 0; i < len(b); i += 2 {
			if b[i] == 0 {
				zeros++
			}
		}
		if zeros*2 == len(b) {
			r := make([]rune, 0, len(b)/2)
			for i := 1; i < len(b); i += 2 {
				r = append(r, rune(b[i]))
			}
			return string(r)
		}
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

var reMultiSpace = regexp.MustCompile(`[ \t]+`)

func cleanPDFText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reMultiSpace.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(reNewlines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// looksLikeText rejects glyph-id soup from fonts with custom encodings.
func looksLikeText(s string) bool {
	letters, other, total := 0, 0, 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			letters++
		case strings.ContainsRune(".,;:!?'\"()[]-–—%/&€$@#*+=<>«»“”‘’", r):
		default:
			other++
		}
	}
	if letters < 20 {
		return false
	}
	return float64(other)/float64(total) < 0.1 && float64(letters)/float64(total) > 0.6
}

func pdfTitle(b []byte, u *url.URL) string {
	if m := rePDFTitle.FindSubmatch(b); m != nil {
		t, _ := readLiteral(append(append([]byte{'('}, m[1]...), ')'))
		if t = strings.TrimSpace(t); t != "" && looksLikeTitle(t) {
			return t
		}
	}
	if u != nil {
		if base := path.Base(u.Path); base != "." && base != "/" {
			if un, err := url.PathUnescape(base); err == nil {
				return un
			}
			return base
		}
	}
	return ""
}

func looksLikeTitle(t string) bool {
	for _, r := range t {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
