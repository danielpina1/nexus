package browser

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// DefaultCheckoutPatterns match the visible label of elements whose click
// commits money (Portuguese + English). They are matched case-insensitively
// against the element's label with accents removed, so "Finalizar Compra",
// "FINALIZAR COMPRA" and "finalizar compra" are the same. Labels only —
// never URLs: Continente's cart lives under /checkout/carrinho/, and opening
// the cart is routine work.
var DefaultCheckoutPatterns = []string{
	`finalizar (a )?(compra|encomenda|pedido)`,
	`concluir (a )?(compra|encomenda|pedido)`,
	`confirmar (a )?(compra|encomenda|pedido|pagamento)`,
	`efetuar (o )?(pagamento|encomenda)`,
	`\bpagar\b`,
	`\bpagamento\b`,
	`comprar (agora|ja)`,
	`\bencomendar\b`,
	`\bcheck ?out\b`,
	`place (your )?order`,
	`submit (your )?order`,
	`confirm (your )?(order|purchase|payment)`,
	`complete (your )?(order|purchase|payment)`,
	`\bpay( now)?\b`,
	`\bbuy now\b`,
	`\bpurchase\b`,
}

// Classifier decides whether clicking an element is a SPEND action.
type Classifier struct {
	res []*regexp.Regexp
}

// NewClassifier compiles patterns (nil = DefaultCheckoutPatterns) plus extra.
func NewClassifier(patterns, extra []string) (*Classifier, error) {
	if len(patterns) == 0 {
		patterns = DefaultCheckoutPatterns
	}
	c := &Classifier{}
	for _, p := range append(append([]string(nil), patterns...), extra...) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		re, err := regexp.Compile("(?i)" + Fold(p))
		if err != nil {
			return nil, fmt.Errorf("checkout pattern %q: %w", p, err)
		}
		c.res = append(c.res, re)
	}
	return c, nil
}

// Match reports the first label that matches a checkout pattern and the
// pattern it matched. Empty labels are ignored.
func (c *Classifier) Match(labels ...string) (label, pattern string, ok bool) {
	for _, l := range labels {
		f := Fold(l)
		if strings.TrimSpace(f) == "" {
			continue
		}
		for _, re := range c.res {
			if re.MatchString(f) {
				return l, re.String(), true
			}
		}
	}
	return "", "", false
}

// Fold lower-cases s, strips diacritics and collapses whitespace.
func Fold(s string) string {
	d := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(d))
	space := false
	for _, r := range d {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case unicode.IsSpace(r):
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.TrimSpace(b.String())
}

var (
	reCardField = regexp.MustCompile(`(?i)(card.?num|cc.?(num|number|csc|cvv|cvc)|cvv|cvc|\bcsc\b|security.?code|\biban\b)`)
	reDigits    = regexp.MustCompile(`^[0-9 \-]{13,23}$`)
)

// SensitiveField reports why an element must never be typed into by an agent
// (passwords, payment card fields), or "".
func SensitiveField(typ, autocomplete, name, id, label string) string {
	if strings.EqualFold(typ, "password") {
		return "password field"
	}
	ac := strings.ToLower(autocomplete)
	if strings.HasPrefix(ac, "cc-") || strings.Contains(ac, " cc-") || strings.Contains(ac, "current-password") || strings.Contains(ac, "new-password") {
		return "payment/password field (autocomplete=" + autocomplete + ")"
	}
	for _, s := range []string{name, id, label} {
		if reCardField.MatchString(s) {
			return "payment card field"
		}
	}
	return ""
}

// LooksLikeCardNumber reports whether text is a Luhn-valid 13–19 digit
// number (spaces and dashes allowed).
func LooksLikeCardNumber(text string) bool {
	t := strings.TrimSpace(text)
	if !reDigits.MatchString(t) {
		return false
	}
	digits := strings.NewReplacer(" ", "", "-", "").Replace(t)
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		n, _ := strconv.Atoi(string(digits[i]))
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}
