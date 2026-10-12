package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/danielpina1/nexus/backend/kernel"
)

// CheckStatus is the outcome of one diagnostic step.
type CheckStatus int

const (
	CheckOK CheckStatus = iota
	CheckWarn
	CheckFail
	CheckSkip
)

func (s CheckStatus) Glyph() string {
	switch s {
	case CheckOK:
		return "✓"
	case CheckWarn:
		return "!"
	case CheckFail:
		return "✗"
	}
	return "-"
}

// CheckRow is one line of a check report.
type CheckRow struct {
	Status  CheckStatus
	Check   string // e.g. "ollama: models", "alias smart", "chat"
	Target  string // provider/model
	Latency time.Duration
	Detail  string
	Hint    string
}

// CheckReport is the result of Check.
type CheckReport struct {
	Providers []ProviderStatus
	Auth      map[string]string // provider -> auth description
	Rows      []CheckRow
}

// Failed reports whether any step failed.
func (r *CheckReport) Failed() bool {
	for _, row := range r.Rows {
		if row.Status == CheckFail {
			return true
		}
	}
	return false
}

// CheckOptions tunes Check.
type CheckOptions struct {
	// Alias limits the probes to one alias.
	Alias string
	// Quick runs only the chat probe (no tools/JSON/stream).
	Quick bool
	// Timeout per probe (default 90s; local models may need to load).
	Timeout time.Duration
}

const (
	probeTool   = "nexus.probe"
	listTimeout = 15 * time.Second
)

// Check diagnoses every provider and alias: reachability, auth, model
// listing, alias resolution, and chat / tool-call / JSON / streaming /
// embeddings probes. It never panics on provider misbehaviour; every
// problem becomes a row with an actionable hint.
func Check(ctx context.Context, c *Client, opts CheckOptions) *CheckReport {
	if opts.Timeout <= 0 {
		opts.Timeout = 90 * time.Second
	}
	rep := &CheckReport{Providers: c.Status(), Auth: map[string]string{}}
	add := func(r CheckRow) { rep.Rows = append(rep.Rows, r) }
	down := map[string]string{} // provider -> why probes are skipped
	listed := map[string]map[string]bool{}

	for _, p := range c.providerOrder() {
		pc := c.cfg.Providers[p]
		mode, header := pc.authMode()
		switch {
		case mode == AuthNone:
			rep.Auth[p] = "none"
		case pc.APIKeyEnv == "":
			rep.Auth[p] = mode + " (no api_key_env)"
		case c.getenv(pc.APIKeyEnv) == "":
			rep.Auth[p] = fmt.Sprintf("%s $%s (EMPTY)", authLabel(mode, header), pc.APIKeyEnv)
		default:
			rep.Auth[p] = fmt.Sprintf("%s $%s", authLabel(mode, header), pc.APIKeyEnv)
		}

		lctx, cancel := context.WithTimeout(ctx, listTimeout)
		start := time.Now()
		models, err := c.ListModels(lctx, p)
		lat := time.Since(start)
		cancel()
		row := CheckRow{Check: p + ": models", Target: c.baseURL(p) + "/models", Latency: lat}
		if err != nil {
			kind, detail, hint := diagnose(c, p, "", err)
			row.Detail, row.Hint = detail, hint
			if kind == diagNotFound {
				// Some servers have no model listing; probes will tell.
				row.Status = CheckWarn
			} else {
				row.Status = CheckFail
				switch kind {
				case diagUnreachable, diagTimeout:
					down[p] = "provider unreachable"
				case diagTLS:
					down[p] = "TLS failed"
				case diagAuth:
					down[p] = "authentication failed"
				}
			}
		} else {
			row.Status = CheckOK
			row.Detail = fmt.Sprintf("%d models", len(models))
			if len(models) == 0 {
				row.Status = CheckWarn
				row.Hint = "the provider lists no models; pull or enable one (e.g. `ollama pull qwen2.5:7b`)"
			}
			set := map[string]bool{}
			for _, m := range models {
				set[m.ID] = true
			}
			listed[p] = set
		}
		add(row)
	}

	if c.cfg.AliasesAuto {
		dctx, cancel := context.WithTimeout(ctx, listTimeout)
		_, _ = c.Discover(dctx)
		cancel()
	}
	aliases := c.AliasTable(ctx)
	var chatTargets, embedTargets []Target
	seen := map[Target]bool{}
	for _, a := range aliases {
		row := CheckRow{Check: "alias " + a.Name}
		if a.Err != nil {
			row.Status, row.Detail = CheckFail, a.Err.Error()
			if c.cfg.AliasesAuto {
				row.Hint = fmt.Sprintf("set llm.aliases.%s.model explicitly, or add a substring of an available model to alias_preferences.%s", a.Name, a.Name)
			} else {
				row.Hint = fmt.Sprintf("set llm.aliases.%s.model to a model from `llm models`, or set aliases_auto: true", a.Name)
			}
			add(row)
			continue
		}
		parts := make([]string, len(a.Targets))
		row.Status = CheckOK
		for i, t := range a.Targets {
			parts[i] = t.String()
			if set, ok := listed[t.Provider]; ok && len(set) > 0 && !set[t.Model] {
				row.Status = CheckWarn
				row.Hint = fmt.Sprintf("%s does not list model %q; check the name with `llm models`", t.Provider, t.Model)
			}
		}
		row.Target = strings.Join(parts, " → ")
		if a.Auto {
			row.Detail = "auto"
		} else {
			row.Detail = "configured"
		}
		if len(a.Targets) > 1 {
			row.Detail += fmt.Sprintf(", %d fallback(s)", len(a.Targets)-1)
		}
		add(row)
		if opts.Alias != "" && opts.Alias != a.Name {
			continue
		}
		for _, t := range a.Targets {
			if seen[t] {
				continue
			}
			seen[t] = true
			if strings.Contains(strings.ToLower(a.Name), "embed") {
				embedTargets = append(embedTargets, t)
			} else {
				chatTargets = append(chatTargets, t)
			}
		}
	}
	if opts.Alias != "" {
		found := false
		for _, a := range aliases {
			found = found || a.Name == opts.Alias
		}
		if !found {
			add(CheckRow{Status: CheckFail, Check: "alias " + opts.Alias, Detail: "alias is not configured"})
		}
	}

	for _, t := range chatTargets {
		if down[t.Provider] != "" {
			add(CheckRow{Status: CheckSkip, Check: "chat", Target: t.String(), Detail: "skipped: " + down[t.Provider]})
			continue
		}
		ok := probeChat(ctx, c, t, opts, add)
		if !ok || opts.Quick {
			continue
		}
		probeTools(ctx, c, t, opts, add)
		probeJSON(ctx, c, t, opts, add)
		probeStream(ctx, c, t, opts, add)
	}
	for _, t := range embedTargets {
		if down[t.Provider] != "" {
			add(CheckRow{Status: CheckSkip, Check: "embed", Target: t.String(), Detail: "skipped: " + down[t.Provider]})
			continue
		}
		probeEmbed(ctx, c, t, opts, add)
	}
	rep.Providers = c.Status()
	return rep
}

func authLabel(mode, header string) string {
	if mode == AuthHeader {
		return header
	}
	return mode
}

func probeCtx(ctx context.Context, opts CheckOptions) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, opts.Timeout)
}

func failRow(c *Client, check string, t Target, lat time.Duration, err error) CheckRow {
	_, detail, hint := diagnose(c, t.Provider, t.Model, err)
	return CheckRow{Status: CheckFail, Check: check, Target: t.String(), Latency: lat, Detail: detail, Hint: hint}
}

func probeChat(ctx context.Context, c *Client, t Target, opts CheckOptions, add func(CheckRow)) bool {
	pctx, cancel := probeCtx(ctx, opts)
	defer cancel()
	res, err := c.ChatTarget(pctx, t, kernel.ChatRequest{Messages: []kernel.ChatMessage{
		{Role: "system", Content: "You are a connectivity check."},
		{Role: "user", Content: "Reply with exactly: OK"},
	}}, false, nil)
	if err != nil {
		add(failRow(c, "chat", t, 0, err))
		return false
	}
	txt := strings.TrimSpace(res.Response.Message.Content)
	row := CheckRow{Status: CheckOK, Check: "chat", Target: t.String(), Latency: res.Latency,
		Detail: fmt.Sprintf("%q", truncate(txt, 40))}
	if res.Response.Cost != nil {
		row.Detail += fmt.Sprintf(", %d+%d tokens", res.Response.Cost.InputTokens, res.Response.Cost.OutputTokens)
	}
	switch {
	case txt == "":
		row.Status = CheckWarn
		row.Detail = "empty reply (stop: " + res.StopReason + ")"
		row.Hint = "the model returned no text; reasoning models may need a larger max_tokens"
	case !strings.Contains(strings.ToUpper(txt), "OK"):
		row.Status = CheckWarn
		row.Hint = "the model answered but did not follow a trivial instruction"
	}
	add(row)
	return true
}

func probeToolSpec() kernel.ToolSpec {
	return kernel.ToolSpec{
		Name:        probeTool,
		Description: "Connectivity check tool. Call it whenever the user asks you to.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"word":{"type":"string","description":"the word to echo"}},"required":["word"]}`),
	}
}

func probeTools(ctx context.Context, c *Client, t Target, opts CheckOptions, add func(CheckRow)) {
	pctx, cancel := probeCtx(ctx, opts)
	defer cancel()
	res, err := c.ChatTarget(pctx, t, kernel.ChatRequest{
		Messages: []kernel.ChatMessage{{Role: "user", Content: `Call the nexus_probe tool with word set to "ping". Do not reply with text.`}},
		Tools:    []kernel.ToolSpec{probeToolSpec()},
	}, false, nil)
	if err != nil {
		row := failRow(c, "tools", t, 0, err)
		if row.Hint == "" {
			row.Hint = "the server rejected a request with tools; the model or server may not support function calling"
		}
		add(row)
		return
	}
	row := CheckRow{Check: "tools", Target: t.String(), Latency: res.Latency}
	calls := res.Response.Message.ToolCalls
	switch {
	case len(calls) == 0:
		row.Status = CheckWarn
		row.Detail = "answered without calling the tool (stop: " + res.StopReason + ")"
		row.Hint = "native agents need tool calling; pick a tool-capable model for this alias (e.g. qwen2.5, llama3.1+, claude, gpt-4.1)"
	case calls[0].Name != probeTool:
		row.Status = CheckWarn
		row.Detail = "called unknown tool " + calls[0].Name
	case !json.Valid([]byte(calls[0].Arguments)):
		row.Status = CheckWarn
		row.Detail = "tool arguments are not valid JSON: " + truncate(calls[0].Arguments, 40)
	default:
		row.Status = CheckOK
		row.Detail = fmt.Sprintf("%s(%s), stop: %s", calls[0].Name, truncate(calls[0].Arguments, 40), res.StopReason)
	}
	add(row)
}

func probeJSON(ctx context.Context, c *Client, t Target, opts CheckOptions, add func(CheckRow)) {
	pctx, cancel := probeCtx(ctx, opts)
	defer cancel()
	res, err := c.ChatTarget(pctx, t, kernel.ChatRequest{
		Messages: []kernel.ChatMessage{{Role: "user", Content: `Return a JSON object with keys "ok" (boolean true) and "n" (the number 3).`}},
		JSON:     true,
	}, false, nil)
	if err != nil {
		row := failRow(c, "json", t, 0, err)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
			row.Hint = "the server rejected response_format=json_object; the router/memory pipeline need JSON mode on their alias"
		}
		add(row)
		return
	}
	var obj map[string]any
	txt := strings.TrimSpace(res.Response.Message.Content)
	row := CheckRow{Check: "json", Target: t.String(), Latency: res.Latency}
	if err := json.Unmarshal([]byte(txt), &obj); err != nil {
		row.Status = CheckWarn
		row.Detail = "not a JSON object: " + truncate(txt, 40)
		row.Hint = "JSON mode is not honoured; use a different model for the router classifier alias"
	} else {
		row.Status = CheckOK
		row.Detail = truncate(txt, 40)
	}
	add(row)
}

func probeStream(ctx context.Context, c *Client, t Target, opts CheckOptions, add func(CheckRow)) {
	pctx, cancel := probeCtx(ctx, opts)
	defer cancel()
	start := time.Now()
	var first time.Duration
	res, err := c.ChatTarget(pctx, t, kernel.ChatRequest{
		Messages: []kernel.ChatMessage{{Role: "user", Content: "Count from 1 to 5, separated by spaces."}},
	}, true, func(string) {
		if first == 0 {
			first = time.Since(start)
		}
	})
	if err != nil {
		add(failRow(c, "stream", t, 0, err))
		return
	}
	row := CheckRow{Status: CheckOK, Check: "stream", Target: t.String(), Latency: res.Latency,
		Detail: fmt.Sprintf("%d deltas, first after %s", res.Deltas, round(first))}
	switch {
	case strings.TrimSpace(res.Response.Message.Content) == "":
		row.Status = CheckWarn
		row.Detail = "stream produced no text"
	case res.Deltas <= 1:
		row.Status = CheckWarn
		row.Detail = "reply arrived in one piece (server does not stream)"
		row.Hint = "live output in the dashboard will appear all at once; check the gateway's streaming support or proxy buffering"
	}
	add(row)
}

func probeEmbed(ctx context.Context, c *Client, t Target, opts CheckOptions, add func(CheckRow)) {
	pctx, cancel := probeCtx(ctx, opts)
	defer cancel()
	start := time.Now()
	vecs, err := c.EmbedTarget(pctx, t, []string{"nexus connectivity check"})
	lat := time.Since(start)
	if err != nil {
		row := failRow(c, "embed", t, lat, err)
		if errors.Is(err, ErrNoEmbeddings) {
			row.Hint = "point the embed alias at an openai-compatible provider (e.g. ollama with nomic-embed-text)"
		}
		add(row)
		return
	}
	row := CheckRow{Status: CheckOK, Check: "embed", Target: t.String(), Latency: lat}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		row.Status = CheckFail
		row.Detail = "empty vector"
	} else {
		row.Detail = fmt.Sprintf("dim %d", len(vecs[0]))
	}
	add(row)
}

type diagKind int

const (
	diagOther diagKind = iota
	diagUnreachable
	diagTLS
	diagAuth
	diagNotFound
	diagTimeout
)

// diagnose turns an error into a short detail and an actionable hint.
func diagnose(c *Client, provider, model string, err error) (diagKind, string, string) {
	pc := c.cfg.Providers[provider]
	base := c.baseURL(provider)
	msg := err.Error()
	short := strings.TrimPrefix(msg, "llm: "+provider+": ")
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		detail := fmt.Sprintf("HTTP %d: %s", apiErr.StatusCode, truncate(apiErr.Message, 80))
		if apiErr.StatusCode == 0 {
			detail = truncate(apiErr.Message, 90)
		}
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			hint := "authentication failed: "
			mode, header := pc.authMode()
			switch {
			case mode == AuthNone:
				hint += "auth is none but the server wants a key; set api_key_env (and auth/auth_header)"
			case pc.APIKeyEnv == "":
				hint += "set api_key_env to the variable holding the key"
			case c.getenv(pc.APIKeyEnv) == "":
				hint += fmt.Sprintf("$%s is empty in this environment; export it", pc.APIKeyEnv)
			default:
				hint += fmt.Sprintf("the key in $%s was rejected (sent as %s); check the key, or try auth: bearer / auth: header with auth_header", pc.APIKeyEnv, authLabel(mode, header))
			}
			return diagAuth, detail, hint
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			low := strings.ToLower(apiErr.Message)
			if model != "" && strings.Contains(low, "model") {
				return diagNotFound, detail, fmt.Sprintf("model %q is not served by %s; list models with `llm models`", model, provider)
			}
			return diagNotFound, detail, fmt.Sprintf("endpoint not found at %s: base_url is probably wrong. "+
				"OpenAI-compatible servers usually want http://HOST:PORT/v1; include any path prefix the gateway uses (api: %s)", apiErr.URL, pc.api())
		case http.StatusTooManyRequests:
			return diagOther, detail, "rate limited; retry later or lower concurrency"
		}
		if apiErr.StatusCode >= 500 {
			return diagOther, detail, "the server failed; check its logs (model not loaded? upstream down?)"
		}
		return diagOther, detail, ""
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return diagTimeout, "timeout", "no answer in time; the host may be unreachable from here or the model is still loading (raise -timeout)"
	}
	if isTLSError(err) {
		if strings.Contains(msg, "first record does not look like a TLS handshake") {
			return diagTLS, truncate(short, 90), fmt.Sprintf("the server speaks plain HTTP; use http:// in base_url (%s)", base)
		}
		if strings.Contains(msg, "certificate is valid for") || strings.Contains(msg, "doesn't contain any IP SANs") {
			return diagTLS, truncate(short, 90), "the certificate does not cover this host name/IP; use a name listed in the certificate in base_url"
		}
		return diagTLS, truncate(short, 90), "TLS verification failed: set ca_file to the PEM of the gateway's CA or self-signed certificate (insecure_skip_verify is not supported)"
	}
	if strings.Contains(msg, "server gave HTTP response to HTTPS client") {
		return diagTLS, truncate(short, 90), "the server speaks plain HTTP; use http:// in base_url"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return diagUnreachable, truncate(short, 90), fmt.Sprintf("cannot resolve %q: check the host name, or use the IP/Tailscale name of the machine", dnsErr.Name)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return diagUnreachable, "connection refused", fmt.Sprintf("nothing listens at %s; is the server running and the port right? (Ollama: OLLAMA_HOST=0.0.0.0 to accept LAN clients)", base)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return diagUnreachable, truncate(short, 90), "network error: is the host reachable from this machine (LAN / VPN / firewall)?"
	}
	if strings.Contains(msg, "decode") || strings.Contains(msg, "unrecognised model list") {
		return diagNotFound, truncate(short, 90), fmt.Sprintf("the response is not the expected JSON: base_url (%s) may point at a web UI instead of the API root", base)
	}
	return diagOther, truncate(short, 90), ""
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func round(d time.Duration) time.Duration {
	switch {
	case d >= time.Second:
		return d.Round(10 * time.Millisecond)
	case d >= 10*time.Millisecond:
		return d.Round(time.Millisecond)
	case d >= time.Microsecond:
		return d.Round(time.Microsecond)
	}
	return d
}

// Print writes the report as tables plus numbered hints.
func (r *CheckReport) Print(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tAPI\tBASE URL\tAUTH")
	for _, p := range r.Providers {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.API, p.BaseURL, r.Auth[p.Name])
	}
	tw.Flush()
	fmt.Fprintln(w)

	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, " \tCHECK\tTARGET\tLATENCY\tDETAIL")
	var hints []string
	counts := map[CheckStatus]int{}
	for _, row := range r.Rows {
		counts[row.Status]++
		lat := "-"
		if row.Latency > 0 {
			lat = round(row.Latency).String()
		}
		detail := row.Detail
		if row.Hint != "" {
			hints = append(hints, row.Hint)
			detail += fmt.Sprintf(" [%d]", len(hints))
		}
		target := row.Target
		if target == "" {
			target = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", row.Status.Glyph(), row.Check, target, lat, detail)
	}
	tw.Flush()
	if len(hints) > 0 {
		fmt.Fprintln(w, "\nhints:")
		for i, h := range hints {
			fmt.Fprintf(w, "  [%d] %s\n", i+1, h)
		}
	}
	fmt.Fprintf(w, "\n%d ok, %d warnings, %d failed, %d skipped\n",
		counts[CheckOK], counts[CheckWarn], counts[CheckFail], counts[CheckSkip])
}
