package browser

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/oklog/ulid/v2"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Tool names.
const (
	ToolNavigate   = "browser.navigate"
	ToolSnapshot   = "browser.snapshot"
	ToolClick      = "browser.click"
	ToolType       = "browser.type"
	ToolSelect     = "browser.select"
	ToolScroll     = "browser.scroll"
	ToolWait       = "browser.wait"
	ToolScreenshot = "browser.screenshot"
	ToolBack       = "browser.back"
	ToolExtract    = "browser.extract"
)

// Limits for individual tools.
const (
	maxWait            = 30 * time.Second
	maxWaitText        = 60 * time.Second
	defaultExtractMax  = 8000
	hardExtractMax     = 20000
	DefaultInlineShots = 1 << 20
)

// toolkit is the per-run set of browser tools over one session.
type toolkit struct {
	s           *session
	cls         *Classifier
	ledger      *ledger
	hasApproval bool
	workdir     string
	maxChars    int
	inlineMax   int
	log         *slog.Logger

	mu    sync.Mutex
	shots int
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }

// tools returns the browser tools.
func (k *toolkit) tools() []kernel.Tool {
	read, compute := nexusv1.ActionClass_ACTION_CLASS_READ, nexusv1.ActionClass_ACTION_CLASS_COMPUTE
	mk := func(name, desc, sch string, class nexusv1.ActionClass, f func(context.Context, kernel.ToolCall) (any, error)) kernel.Tool {
		return kernel.ToolFunc{
			S: kernel.ToolSpec{Name: name, Description: desc, InputSchema: schema(sch), ActionClass: class},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				v, err := f(ctx, c)
				if err != nil {
					return kernel.ToolResult{}, err
				}
				if r, ok := v.(kernel.ToolResult); ok {
					return r, nil
				}
				return kernel.ToolOutput(v)
			},
		}
	}
	return []kernel.Tool{
		mk(ToolNavigate, "Open a URL in the browser (http/https, allowed domains only). Then call browser.snapshot.",
			`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`, read, k.navigate),
		mk(ToolSnapshot, "Text view of the current page: title, URL, numbered interactive elements [ref] with role/name/value, and visible text. Refs are used by click/type/select and change after navigation.",
			`{"type":"object","properties":{}}`, read, k.snapshot),
		mk(ToolClick, "Click the element [ref] from the latest snapshot. Checkout/payment buttons require approval_id from an APPROVED request_approval.",
			`{"type":"object","properties":{"ref":{"type":"integer"},"approval_id":{"type":"string","description":"Only for checkout/payment clicks: browser_approval_id returned by an approved request_approval."}},"required":["ref"]}`, compute, k.click),
		mk(ToolType, "Type text into the field [ref] (replaces its content unless append=true); submit=true presses Enter. Never use for passwords or card data.",
			`{"type":"object","properties":{"ref":{"type":"integer"},"text":{"type":"string"},"submit":{"type":"boolean"},"append":{"type":"boolean"},"approval_id":{"type":"string"}},"required":["ref","text"]}`, compute, k.typeText),
		mk(ToolSelect, "Choose an option of the <select> [ref] by value or visible text.",
			`{"type":"object","properties":{"ref":{"type":"integer"},"value":{"type":"string"}},"required":["ref","value"]}`, compute, k.selectOpt),
		mk(ToolScroll, "Scroll the page: direction up|down|top|bottom (optional amount in px), or bring element [ref] into view.",
			`{"type":"object","properties":{"direction":{"type":"string","enum":["up","down","top","bottom"]},"amount":{"type":"integer"},"ref":{"type":"integer"}}}`, read, k.scroll),
		mk(ToolWait, "Wait for ms milliseconds (max 30000) or until text appears on the page (timeout_ms, default 10000).",
			`{"type":"object","properties":{"ms":{"type":"integer"},"text":{"type":"string"},"timeout_ms":{"type":"integer"}}}`, read, k.wait),
		mk(ToolScreenshot, "Take a PNG screenshot of the page (full_page=true for the whole page). It is attached to the task; reference its attachment_id in approvals.",
			`{"type":"object","properties":{"full_page":{"type":"boolean"}}}`, read, k.screenshot),
		mk(ToolBack, "Go back to the previous page.", `{"type":"object","properties":{}}`, read, k.back),
		mk(ToolExtract, "Visible text of element [ref], or of the whole page when ref is omitted (max_chars, default 8000).",
			`{"type":"object","properties":{"ref":{"type":"integer"},"max_chars":{"type":"integer"}}}`, read, k.extract),
	}
}

// pageState is returned by actions.
type pageState struct {
	OK                 bool     `json:"ok"`
	URL                string   `json:"url"`
	Title              string   `json:"title"`
	Hint               string   `json:"hint,omitempty"`
	Dialogs            []string `json:"dialogs,omitempty"`
	BlockedNavigations []string `json:"blocked_navigations,omitempty"`
	Checkout           bool     `json:"checkout_click,omitempty"`
}

func (k *toolkit) stateLocked(ctx context.Context, hint string) pageState {
	u, t := k.s.locationLocked(ctx)
	d, b := k.s.drainNotices()
	return pageState{OK: true, URL: u, Title: t, Hint: hint, Dialogs: d, BlockedNavigations: b}
}

const snapHint = "call browser.snapshot to see the page"

func (k *toolkit) navigate(ctx context.Context, c kernel.ToolCall) (any, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return nil, err
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.URL != "" && !strings.Contains(in.URL, "://") && in.URL != "about:blank" {
		in.URL = "https://" + in.URL
	}
	if err := k.s.cfg.Domains.CheckURL(in.URL); err != nil {
		return nil, err
	}
	if !k.s.cfg.Domains.Restricted() {
		k.log.Info("browser: navigate (no domain allow-list)", "url", in.URL)
	}
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	if err := k.s.doLocked(ctx, k.s.cfg.NavTimeout, chromedp.Navigate(in.URL)); err != nil {
		if strings.Contains(err.Error(), "ERR_BLOCKED_BY_CLIENT") {
			return nil, fmt.Errorf("navigation to %s was blocked: domain not allowed for this agent", in.URL)
		}
		return nil, fmt.Errorf("navigate: %w", err)
	}
	return k.stateLocked(ctx, snapHint), nil
}

func (k *toolkit) snapshot(ctx context.Context, _ kernel.ToolCall) (any, error) {
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	raw, err := k.s.evalLocked(ctx, snapshotExpr(snapshotMaxElements, snapshotMaxText))
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	snap, err := ParseSnapshot(raw)
	if err != nil {
		return nil, err
	}
	d, b := k.s.drainNotices()
	return map[string]any{
		"url": snap.URL, "title": snap.Title, "snapshot": snap.Format(k.maxChars),
		"dialogs": d, "blocked_navigations": b,
	}, nil
}

// describeLocked fetches element metadata for ref.
func (k *toolkit) describeLocked(ctx context.Context, ref int) (elementInfo, error) {
	var info elementInfo
	if ref <= 0 {
		return info, errors.New("ref must be a positive element number from browser.snapshot")
	}
	raw, err := k.s.evalLocked(ctx, elementExpr("describe", ref, ""))
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return info, err
	}
	if !info.Found {
		return info, fmt.Errorf("element [%d] not found on the current page; take a new browser.snapshot (refs change after navigation)", ref)
	}
	return info, nil
}

// SpendError explains why a checkout/payment action was refused.
type SpendError struct {
	Label, Pattern, Reason string
	CanRequest             bool
}

func (e *SpendError) Error() string {
	if !e.CanRequest {
		return fmt.Sprintf("BLOCKED (spend): %q looks like a checkout/payment action and this agent has no request_approval tool, so it cannot complete purchases. Stop and report the cart to the user.", e.Label)
	}
	return fmt.Sprintf("BLOCKED (spend): %q looks like a checkout/payment action (matched %s) and needs the user's approval: %s. "+
		"Take a browser.screenshot of the cart, call request_approval with action_class \"spend\", the item list, total, delivery slot and the screenshot attachment_id, "+
		"then retry this exact call with approval_id set to the browser_approval_id it returns. If the user denies, stop.", e.Label, e.Pattern, e.Reason)
}

// gate enforces approval for spend actions. It consumes the approval.
func (k *toolkit) gate(label, pattern, approvalID string) error {
	if !k.hasApproval {
		return &SpendError{Label: label, Pattern: pattern}
	}
	if err := k.ledger.consume(strings.TrimSpace(approvalID)); err != nil {
		return &SpendError{Label: label, Pattern: pattern, Reason: err.Error(), CanRequest: true}
	}
	k.log.Info("browser: approved checkout action", "label", label, "approval_id", approvalID)
	return nil
}

func refSel(ref int) string { return fmt.Sprintf(`[data-nexus-ref="%d"]`, ref) }

func (k *toolkit) click(ctx context.Context, c kernel.ToolCall) (any, error) {
	var in struct {
		Ref        int    `json:"ref"`
		ApprovalID string `json:"approval_id"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return nil, err
	}
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	info, err := k.describeLocked(ctx, in.Ref)
	if err != nil {
		return nil, err
	}
	label, pattern, spend := k.cls.Match(info.Labels...)
	if spend {
		if err := k.gate(oneLine(label, 80), pattern, in.ApprovalID); err != nil {
			return nil, err
		}
	}
	if info.Disabled {
		return nil, fmt.Errorf("element [%d] %q is disabled", in.Ref, info.label())
	}
	if _, err := k.s.evalLocked(ctx, elementExpr("prepare", in.Ref, "")); err != nil {
		return nil, err
	}
	if err := k.s.doLocked(ctx, 5*time.Second, chromedp.Click(refSel(in.Ref), chromedp.ByQuery, chromedp.NodeVisible)); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Not visible to the mouse (covered, zero-size): fall back to a DOM click.
		k.log.Debug("browser: mouse click failed, using DOM click", "ref", in.Ref, "err", err)
		if _, err := k.s.evalLocked(ctx, elementExpr("click", in.Ref, "")); err != nil {
			return nil, fmt.Errorf("click [%d]: %w", in.Ref, err)
		}
	}
	k.s.settleLocked(ctx)
	st := k.stateLocked(ctx, snapHint)
	st.Checkout = spend
	if err := k.checkHostLocked(ctx, st.URL); err != nil {
		return nil, err
	}
	return st, nil
}

// checkHostLocked is a second line of defence behind request interception.
func (k *toolkit) checkHostLocked(ctx context.Context, u string) error {
	if !k.s.cfg.Domains.Restricted() || u == "" || strings.HasPrefix(u, "about:") || strings.HasPrefix(u, "chrome-error:") {
		return nil
	}
	if h := hostOf(u); h != "" && !k.s.cfg.Domains.AllowHost(h) {
		_ = k.s.doLocked(ctx, k.s.cfg.NavTimeout, chromedp.NavigateBack())
		return fmt.Errorf("the page left the allowed domains (%s); went back", h)
	}
	return nil
}

func (k *toolkit) typeText(ctx context.Context, c kernel.ToolCall) (any, error) {
	var in struct {
		Ref        int    `json:"ref"`
		Text       string `json:"text"`
		Submit     bool   `json:"submit"`
		Append     bool   `json:"append"`
		ApprovalID string `json:"approval_id"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return nil, err
	}
	if LooksLikeCardNumber(in.Text) {
		return nil, errors.New("refused: the text looks like a payment card number; agents never type card data (the saved payment method in the account is used)")
	}
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	info, err := k.describeLocked(ctx, in.Ref)
	if err != nil {
		return nil, err
	}
	if why := SensitiveField(info.Type, info.Autocomplete, info.NameAttr, info.ID, strings.Join(info.Labels, " ")); why != "" {
		return nil, fmt.Errorf("refused: element [%d] is a %s; agents never type passwords or card data. If a login is needed, ask_user to log in once interactively (see the browser agent notes)", in.Ref, why)
	}
	spend := false
	if in.Submit {
		if label, pattern, ok := k.cls.Match(info.SubmitLabels...); ok {
			if err := k.gate(oneLine(label, 80), pattern, in.ApprovalID); err != nil {
				return nil, err
			}
			spend = true
		}
	}
	if info.Disabled {
		return nil, fmt.Errorf("element [%d] is disabled", in.Ref)
	}
	if _, err := k.s.evalLocked(ctx, elementExpr("prepare", in.Ref, "")); err != nil {
		return nil, err
	}
	mode := ""
	if in.Append {
		mode = "keep"
	}
	if _, err := k.s.evalLocked(ctx, elementExpr("focus", in.Ref, mode)); err != nil {
		return nil, err
	}
	acts := []chromedp.Action{}
	if in.Text == "" && !in.Append {
		acts = append(acts, chromedp.KeyEvent(kb.Delete))
	} else {
		acts = append(acts, chromedp.KeyEvent(in.Text))
	}
	if in.Submit {
		acts = append(acts, chromedp.KeyEvent(kb.Enter))
	}
	if err := k.s.doLocked(ctx, 0, acts...); err != nil {
		return nil, fmt.Errorf("type into [%d]: %w", in.Ref, err)
	}
	hint := ""
	if in.Submit {
		k.s.settleLocked(ctx)
		hint = snapHint
	}
	st := k.stateLocked(ctx, hint)
	st.Checkout = spend
	if err := k.checkHostLocked(ctx, st.URL); err != nil {
		return nil, err
	}
	return st, nil
}

func (k *toolkit) selectOpt(ctx context.Context, c kernel.ToolCall) (any, error) {
	var in struct {
		Ref   int    `json:"ref"`
		Value string `json:"value"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return nil, err
	}
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	if _, err := k.describeLocked(ctx, in.Ref); err != nil {
		return nil, err
	}
	raw, err := k.s.evalLocked(ctx, elementExpr("select", in.Ref, in.Value))
	if err != nil {
		return nil, err
	}
	var out struct {
		Error    string   `json:"error"`
		Selected string   `json:"selected"`
		Value    string   `json:"value"`
		Options  []string `json:"options"`
	}
	_ = json.Unmarshal([]byte(raw), &out)
	if out.Error != "" {
		if len(out.Options) > 0 {
			return nil, fmt.Errorf("%s; options: %s", out.Error, strings.Join(out.Options, " | "))
		}
		return nil, errors.New(out.Error)
	}
	k.s.settleLocked(ctx)
	st := k.stateLocked(ctx, "")
	return map[string]any{"ok": true, "selected": out.Selected, "value": out.Value, "url": st.URL, "dialogs": st.Dialogs}, nil
}

func (k *toolkit) scroll(ctx context.Context, c kernel.ToolCall) (any, error) {
	var in struct {
		Direction string `json:"direction"`
		Amount    int    `json:"amount"`
		Ref       int    `json:"ref"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return nil, err
	}
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	if in.Ref > 0 {
		if _, err := k.describeLocked(ctx, in.Ref); err != nil {
			return nil, err
		}
		if _, err := k.s.evalLocked(ctx, elementExpr("prepare", in.Ref, "")); err != nil {
			return nil, err
		}
	} else {
		amount := in.Amount
		var js string
		switch strings.ToLower(in.Direction) {
		case "", "down":
			js = fmt.Sprintf("window.scrollBy(0, %d)", scrollAmount(amount))
		case "up":
			js = fmt.Sprintf("window.scrollBy(0, -%d)", scrollAmount(amount))
		case "top":
			js = "window.scrollTo(0, 0)"
		case "bottom":
			js = "window.scrollTo(0, document.documentElement.scrollHeight)"
		default:
			return nil, fmt.Errorf("direction must be up, down, top or bottom")
		}
		if err := k.s.doLocked(ctx, 0, chromedp.Evaluate(js, nil)); err != nil {
			return nil, err
		}
	}
	var pos struct {
		Y      int `json:"y"`
		Height int `json:"height"`
	}
	_ = k.s.doLocked(ctx, 0, chromedp.Evaluate(`({y: Math.round(scrollY), height: document.documentElement.scrollHeight})`, &pos))
	return map[string]any{"ok": true, "scroll_y": pos.Y, "scroll_height": pos.Height, "hint": snapHint}, nil
}

func scrollAmount(a int) int {
	if a <= 0 {
		return 700
	}
	return min(a, 20000)
}

func (k *toolkit) wait(ctx context.Context, c kernel.ToolCall) (any, error) {
	var in struct {
		MS        int    `json:"ms"`
		Text      string `json:"text"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Text) == "" {
		d := time.Duration(in.MS) * time.Millisecond
		if d <= 0 {
			d = time.Second
		}
		d = min(d, maxWait)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d):
		}
		return map[string]any{"ok": true, "waited_ms": d.Milliseconds()}, nil
	}
	timeout := 10 * time.Second
	if in.TimeoutMS > 0 {
		timeout = min(time.Duration(in.TimeoutMS)*time.Millisecond, maxWaitText)
	}
	needle, _ := json.Marshal(strings.ToLower(in.Text))
	js := fmt.Sprintf(`(document.body ? document.body.innerText : '').toLowerCase().includes(%s)`, needle)
	deadline := time.Now().Add(timeout)
	for {
		k.s.mu.Lock()
		var found bool
		err := k.s.doLocked(ctx, 3*time.Second, chromedp.Evaluate(js, &found))
		k.s.mu.Unlock()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, errClosed) {
			return nil, err
		}
		if err == nil && found {
			return map[string]any{"ok": true, "found": true}, nil
		}
		if time.Now().After(deadline) {
			return map[string]any{"ok": false, "found": false, "error": fmt.Sprintf("text %q did not appear within %s", in.Text, timeout)}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (k *toolkit) screenshot(ctx context.Context, c kernel.ToolCall) (any, error) {
	var in struct {
		FullPage bool `json:"full_page"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return nil, err
	}
	var buf []byte
	k.s.mu.Lock()
	var act chromedp.Action = chromedp.CaptureScreenshot(&buf)
	if in.FullPage {
		act = chromedp.FullScreenshot(&buf, 100) // quality 100 = PNG
	}
	err := k.s.doLocked(ctx, 0, act)
	u, title := k.s.locationLocked(ctx)
	k.s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	k.mu.Lock()
	k.shots++
	n := k.shots
	k.mu.Unlock()
	att := &nexusv1.Attachment{
		Id:        "att_" + ulid.MustNew(ulid.Now(), rand.Reader).String(),
		Name:      fmt.Sprintf("screenshot-%d.png", n),
		MimeType:  "image/png",
		SizeBytes: int64(len(buf)),
	}
	if k.workdir != "" {
		dir := filepath.Join(k.workdir, "screenshots")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			p := filepath.Join(dir, att.Name)
			if err := os.WriteFile(p, buf, 0o600); err == nil {
				att.Url = "file://" + p
			}
		}
	}
	if len(buf) <= k.inlineMax || att.Url == "" {
		att.Data = buf
	}
	out, err := kernel.ToolOutput(map[string]any{
		"attachment_id": att.Id, "name": att.Name, "size_bytes": att.SizeBytes,
		"url": u, "title": title, "full_page": in.FullPage,
		"note": "screenshot attached to the task; reference attachment_id in request_approval",
	})
	if err != nil {
		return nil, err
	}
	out.Artifacts = []*nexusv1.Attachment{att}
	return out, nil
}

func (k *toolkit) back(ctx context.Context, _ kernel.ToolCall) (any, error) {
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	if err := k.s.doLocked(ctx, k.s.cfg.NavTimeout, chromedp.NavigateBack()); err != nil {
		return nil, fmt.Errorf("back: %w", err)
	}
	k.s.settleLocked(ctx)
	return k.stateLocked(ctx, snapHint), nil
}

func (k *toolkit) extract(ctx context.Context, c kernel.ToolCall) (any, error) {
	var in struct {
		Ref      int `json:"ref"`
		MaxChars int `json:"max_chars"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return nil, err
	}
	max := in.MaxChars
	if max <= 0 {
		max = defaultExtractMax
	}
	max = min(max, hardExtractMax)
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	if in.Ref > 0 {
		if _, err := k.describeLocked(ctx, in.Ref); err != nil {
			return nil, err
		}
	}
	raw, err := k.s.evalLocked(ctx, elementExpr("extract", max0(in.Ref), ""))
	if err != nil {
		return nil, err
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	truncated := len(out.Text) > max
	if truncated {
		out.Text = cutUTF8(out.Text, max)
	}
	return map[string]any{"text": out.Text, "truncated": truncated, "untrusted": true}, nil
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
