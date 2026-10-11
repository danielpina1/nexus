package approval

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// CanonicalJSON returns the RFC 8785 (JSON Canonicalization Scheme, JCS)
// form of v:
//
//   - no insignificant whitespace;
//   - object members sorted by key, comparing keys as UTF-16 code units;
//   - strings escaped like ECMAScript JSON.stringify: only `"` `\` and
//     U+0000..U+001F are escaped (\b \t \n \f \r or \u00xx lowercase hex);
//     everything else (including non-ASCII and '/') is emitted literally;
//   - numbers are IEEE-754 doubles printed with the ECMAScript
//     Number.prototype.toString algorithm (1, 1.5, 1e+21, 1e-7, -0 → 0);
//     integers beyond ±2^53 lose precision, so send those as strings;
//   - duplicate object keys are an error.
//
// v may be []byte / json.RawMessage (parsed as JSON), a proto.Message
// (rendered with protojson first, e.g. google.protobuf.Struct), or any value
// encoding/json can marshal.
func CanonicalJSON(v any) ([]byte, error) {
	var raw []byte
	var err error
	switch x := v.(type) {
	case json.RawMessage:
		raw = x
	case []byte:
		raw = x
	case proto.Message:
		raw, err = protojson.Marshal(x)
	default:
		raw, err = json.Marshal(v)
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out bytes.Buffer
	if err := canonValue(dec, &out); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("canonical json: trailing data")
	}
	return out.Bytes(), nil
}

// ActionHash is SHA256(CanonicalJSON(v)): the action_sha256 / payload_hash
// that devices sign. Clients recompute it from the exact action preview.
func ActionHash(v any) ([]byte, error) {
	c, err := CanonicalJSON(v)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(c)
	return h[:], nil
}

// ActionHashProto is SHA256(deterministic protobuf encoding of m). Only for
// Go-to-Go use (hub <-> node); the protobuf wire encoding is not canonical
// across languages, so anything a phone must recompute uses ActionHash.
func ActionHashProto(m proto.Message) ([]byte, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	return h[:], nil
}

func canonValue(dec *json.Decoder, out *bytes.Buffer) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	return canonToken(dec, tok, out)
}

func canonToken(dec *json.Decoder, tok json.Token, out *bytes.Buffer) error {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return canonObject(dec, out)
		case '[':
			out.WriteByte('[')
			first := true
			for dec.More() {
				if !first {
					out.WriteByte(',')
				}
				first = false
				if err := canonValue(dec, out); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // ']'
				return err
			}
			out.WriteByte(']')
			return nil
		}
		return fmt.Errorf("canonical json: unexpected %v", t)
	case string:
		return writeString(out, t)
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil {
			return fmt.Errorf("canonical json: number %q: %w", t, err)
		}
		s, err := formatNumber(f)
		if err != nil {
			return err
		}
		out.WriteString(s)
		return nil
	case bool:
		if t {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
		return nil
	case nil:
		out.WriteString("null")
		return nil
	}
	return fmt.Errorf("canonical json: unexpected token %T", tok)
}

func canonObject(dec *json.Decoder, out *bytes.Buffer) error {
	type member struct {
		key string
		val []byte
	}
	var members []member
	seen := map[string]bool{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		k, ok := kt.(string)
		if !ok {
			return errors.New("canonical json: non-string key")
		}
		if seen[k] {
			return fmt.Errorf("canonical json: duplicate key %q", k)
		}
		seen[k] = true
		var vb bytes.Buffer
		if err := canonValue(dec, &vb); err != nil {
			return err
		}
		members = append(members, member{k, vb.Bytes()})
	}
	if _, err := dec.Token(); err != nil { // '}'
		return err
	}
	sort.Slice(members, func(i, j int) bool { return lessUTF16(members[i].key, members[j].key) })
	out.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		if err := writeString(out, m.key); err != nil {
			return err
		}
		out.WriteByte(':')
		out.Write(m.val)
	}
	out.WriteByte('}')
	return nil
}

func lessUTF16(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

func writeString(out *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return errors.New("canonical json: invalid UTF-8")
	}
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return nil
}

// formatNumber implements ECMAScript Number::toString for finite doubles.
func formatNumber(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", errors.New("canonical json: NaN/Inf not allowed")
	}
	if f == 0 {
		return "0", nil
	}
	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}
	// Shortest round-trip digits: d.ddddde±XX
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(e, "e")
	exp, err := strconv.Atoi(expStr)
	if err != nil {
		return "", err
	}
	digits := strings.Replace(mant, ".", "", 1)
	k := len(digits)
	n := exp + 1 // value = 0.digits × 10^n
	var s string
	switch {
	case k <= n && n <= 21:
		s = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		s = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		s = "0." + strings.Repeat("0", -n) + digits
	default:
		ee := n - 1
		es := "+"
		if ee < 0 {
			es = "-"
			ee = -ee
		}
		if k == 1 {
			s = digits + "e" + es + strconv.Itoa(ee)
		} else {
			s = digits[:1] + "." + digits[1:] + "e" + es + strconv.Itoa(ee)
		}
	}
	return sign + s, nil
}
