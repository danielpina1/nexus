package cliproc

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Failure kinds (kernel.ErrorKindRateLimited / kernel.ErrorKindAuth; kept as
// literals so this package stays free of the kernel import).
const (
	KindRateLimited = "rate_limited"
	KindAuth        = "auth"
)

var (
	// "Claude AI usage limit reached|1736300000" (older Claude Code).
	reUnixReset = regexp.MustCompile(`(?i)limit reached\s*\|\s*(\d{9,11})`)
	reRate      = regexp.MustCompile(`(?i)usage limit|rate[ _-]?limit|limit reached|hit your (usage )?limit|too many requests|\b429\b|overloaded|quota exceeded|exceeded (your|the) quota|insufficient_quota`)
	reAuth      = regexp.MustCompile(`(?i)not logged in|please run /login|run /login|invalid api key|authentication_error|authentication failed|oauth token (has )?expired|token (has )?expired|unauthori[sz]ed|\b401\b|please log ?in|login required|codex login`)
	// "resets 3pm (Europe/Lisbon)", "reset at 15:00", "try again in 2 hours".
	reResetText = regexp.MustCompile(`(?i)(?:resets?|try again)\s+((?:at|in|on|after)\s+[^.\n|]{1,60}|[^.\n|]{1,60})`)
)

// ClassifyFailure inspects a CLI's error text (result message or stderr
// tail) and reports a machine-readable kind ("" when unknown) and, for usage
// limits, when the limit resets as a phrase to follow "resets" ("at 15:00
// CET", "in 2 hours", "3pm"; best effort, "" when not parseable).
// Rate/usage limits win over auth when both match.
func ClassifyFailure(text string) (kind, resetsAt string) {
	if strings.TrimSpace(text) == "" {
		return "", ""
	}
	if m := reUnixReset.FindStringSubmatch(text); m != nil {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return KindRateLimited, "at " + time.Unix(sec, 0).Local().Format("Mon 15:04 MST")
		}
	}
	if reRate.MatchString(text) {
		if m := reResetText.FindStringSubmatch(text); m != nil {
			return KindRateLimited, strings.TrimSpace(strings.TrimRight(m[1], " ,;:·∙"))
		}
		return KindRateLimited, ""
	}
	if reAuth.MatchString(text) {
		return KindAuth, ""
	}
	return "", ""
}

// LimitMessage is the "<what> reached" result text of a usage-limit stop
// that some CLI versions report as a normal result. It only matches short
// texts that start like the CLIs' own messages, so an agent's answer that
// merely mentions limits is not mistaken for one.
func LimitMessage(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 300 {
		return false
	}
	l := strings.ToLower(t)
	for _, p := range []string{"claude ai usage limit reached", "claude usage limit reached", "5-hour limit reached",
		"weekly limit reached", "opus weekly limit reached", "you've hit your limit", "you've hit your usage limit",
		"usage limit reached", "api error: 429", "api error: rate limit"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}
