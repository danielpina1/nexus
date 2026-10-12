package cliproc

import (
	"strings"
	"testing"
)

func TestClassifyFailure(t *testing.T) {
	for _, tc := range []struct {
		text, kind, resets string
	}{
		{"Claude AI usage limit reached|1767225600", KindRateLimited, "at "},
		{"5-hour limit reached ∙ resets 3pm", KindRateLimited, "3pm"},
		{"You've hit your limit · resets 5pm (Europe/Lisbon)", KindRateLimited, "5pm (Europe/Lisbon)"},
		{"You've hit your usage limit. Upgrade to Pro or try again in 2 hours 5 minutes.", KindRateLimited, "in 2 hours 5 minutes"},
		{"API Error: 429 {\"type\":\"rate_limit_error\"}", KindRateLimited, ""},
		{"stream error: exceeded retry limit, last status: 429 Too Many Requests", KindRateLimited, ""},
		{"Invalid API key · Please run /login", KindAuth, ""},
		{"fatal: not logged in", KindAuth, ""},
		{"OAuth token has expired. Please obtain a new token or refresh your existing token.", KindAuth, ""},
		{"error_max_turns: reached max turns", "", ""},
		{"boom: something broke", "", ""},
		{"", "", ""},
	} {
		kind, resets := ClassifyFailure(tc.text)
		if kind != tc.kind || !strings.HasPrefix(resets, tc.resets) || (tc.resets == "" && resets != "") {
			t.Errorf("ClassifyFailure(%q) = %q, %q; want %q, %q", tc.text, kind, resets, tc.kind, tc.resets)
		}
	}
}

func TestLimitMessage(t *testing.T) {
	for text, want := range map[string]bool{
		"Claude AI usage limit reached|1767225600":               true,
		"5-hour limit reached ∙ resets 3pm":                      true,
		"You've hit your limit · resets 5pm":                     true,
		"There are two files.":                                   false,
		"The usage limit reached by the API is documented in X…": false,
		strings.Repeat("Claude usage limit reached ", 20):        false,
	} {
		if got := LimitMessage(text); got != want {
			t.Errorf("LimitMessage(%q) = %v", text, got)
		}
	}
}
