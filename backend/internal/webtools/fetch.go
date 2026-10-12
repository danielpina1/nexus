package webtools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

// FetchResult is the web.fetch output.
type FetchResult struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Title       string `json:"title"`
	Byline      string `json:"byline,omitempty"`
	Published   string `json:"published,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	Description string `json:"description,omitempty"`
	Lang        string `json:"lang,omitempty"`
	Text        string `json:"text"`
	// Truncated: Text was cut at max_chars (TotalChars is the full length)
	// or the download hit the size cap.
	Truncated  bool   `json:"truncated,omitempty"`
	TotalChars int    `json:"total_chars"`
	Links      []Link `json:"links"`
	Cached     bool   `json:"cached,omitempty"`
	FetchedAt  string `json:"fetched_at"`
	Note       string `json:"note,omitempty"`
}

// Fetcher downloads pages through the SSRF guard and extracts their text.
type Fetcher struct {
	cfg    FetchConfig
	guard  *Guard
	client *http.Client
	cache  *diskCache
	robots *robotsCache
}

// NewFetcher builds a fetcher; cache may be nil.
func NewFetcher(cfg FetchConfig, cache *diskCache) *Fetcher {
	g := NewGuard(cfg.AllowPrivate, cfg.AllowHosts, cfg.DenyHosts)
	f := &Fetcher{cfg: cfg, guard: g, cache: cache}
	f.client = g.NewClient(time.Duration(cfg.Timeout), cfg.UseEnvProxy)
	if cfg.RespectRobots {
		f.robots = newRobotsCache()
	}
	return f
}

// normalizeURL adds a scheme to bare hosts and drops fragments.
func normalizeURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("url required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + strings.TrimPrefix(raw, "//")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	u.Fragment, u.RawFragment = "", ""
	u.Scheme = strings.ToLower(u.Scheme)
	return u, nil
}

// get returns the raw page, from the cache when fresh.
func (f *Fetcher) get(ctx context.Context, raw string) (*cachedPage, bool, error) {
	u, err := normalizeURL(raw)
	if err != nil {
		return nil, false, err
	}
	if err := f.guard.CheckURL(u); err != nil {
		return nil, false, err
	}
	key := u.String()
	if p, ok := f.cache.Get(key); ok {
		return p, true, nil
	}
	if f.cfg.UseEnvProxy {
		if err := f.guard.CheckResolved(ctx, nil, u); err != nil {
			return nil, false, err
		}
	}
	if f.robots != nil {
		ok, err := f.robots.allowed(ctx, f, u)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, fmt.Errorf("robots.txt of %s disallows %s", u.Host, u.Path)
		}
	}
	resp, body, truncated, err := f.download(ctx, u.String(), "text/html,application/xhtml+xml,application/pdf;q=0.9,text/plain;q=0.8,*/*;q=0.5")
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode >= 400 {
		return nil, false, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, u)
	}
	p := &cachedPage{URL: key, FinalURL: resp.Request.URL.String(), Status: resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"), Truncated: truncated, FetchedAt: time.Now().UTC(), Body: body}
	if p.ContentType == "" {
		p.ContentType = http.DetectContentType(body)
	}
	_ = f.cache.Put(p)
	return p, false, nil
}

// download performs one GET with the size cap. The response body is closed.
func (f *Fetcher) download(ctx context.Context, u, accept string) (*http.Response, []byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, false, err
	}
	req.Header.Set("User-Agent", f.cfg.UserAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", f.cfg.AcceptLanguage)
	resp, err := f.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && errors.Is(ue.Err, ErrBlocked) {
			return nil, nil, false, ue.Err
		}
		if errors.Is(err, ErrBlocked) {
			return nil, nil, false, unwrapBlocked(err)
		}
		return nil, nil, false, err
	}
	defer resp.Body.Close()
	max := f.cfg.MaxDownloadBytes
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "pdf") && resp.ContentLength > max {
		return nil, nil, false, fmt.Errorf("PDF is %d bytes, over the %d byte download cap", resp.ContentLength, max)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, nil, false, err
	}
	truncated := int64(len(b)) > max
	if truncated {
		b = b[:max]
	}
	return resp, b, truncated, nil
}

func unwrapBlocked(err error) error {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.HasPrefix(e.Error(), ErrBlocked.Error()) {
			return e
		}
	}
	return err
}

// Fetch returns the readable content of a URL, capped at maxChars (<=0:
// the configured default).
func (f *Fetcher) Fetch(ctx context.Context, raw string, maxChars int) (*FetchResult, error) {
	if maxChars <= 0 {
		maxChars = f.cfg.MaxChars
	}
	maxChars = min(maxChars, HardMaxChars)
	p, cached, err := f.get(ctx, raw)
	if err != nil {
		return nil, err
	}
	res, err := f.render(p)
	if err != nil {
		return nil, err
	}
	res.Cached = cached
	res.TotalChars = utf8.RuneCountInString(res.Text)
	if res.TotalChars > maxChars {
		res.Text = truncateText(res.Text, maxChars)
		res.Truncated = true
	}
	return res, nil
}

// render converts a raw page into a FetchResult (full text).
func (f *Fetcher) render(p *cachedPage) (*FetchResult, error) {
	final, _ := url.Parse(p.FinalURL)
	res := &FetchResult{URL: p.URL, FinalURL: p.FinalURL, Status: p.Status, Links: []Link{},
		FetchedAt: p.FetchedAt.Format(time.RFC3339), Truncated: p.Truncated}
	mt, _, _ := mime.ParseMediaType(p.ContentType)
	mt = strings.ToLower(mt)
	if mt == "" || mt == "application/octet-stream" {
		mt, _, _ = mime.ParseMediaType(http.DetectContentType(p.Body))
	}
	if bytes.HasPrefix(p.Body, []byte("%PDF-")) {
		mt = "application/pdf"
	}
	res.ContentType = mt
	switch {
	case mt == "application/pdf":
		text, note := ExtractPDFText(p.Body)
		res.Text, res.Note = text, note
		if p.Truncated {
			res.Note = strings.TrimSpace(res.Note + " The PDF was cut at the download cap; text may be incomplete.")
		}
		res.Title = pdfTitle(p.Body, final)
		return res, nil
	case mt == "text/html" || mt == "application/xhtml+xml":
		body := decodeCharset(p.Body, p.ContentType)
		a, err := ExtractHTML(body, final, f.cfg.MaxLinks)
		if err != nil {
			return nil, fmt.Errorf("parse html: %w", err)
		}
		res.Title, res.Byline, res.Published, res.SiteName = a.Title, a.Byline, a.Published, a.SiteName
		res.Description, res.Lang, res.Text, res.Links = a.Description, a.Lang, a.Text, a.Links
		if res.Text == "" {
			res.Note = "No readable text found; the page may need JavaScript (use the browser agent)."
		}
		return res, nil
	case strings.HasPrefix(mt, "text/") || mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		mt == "application/xml" || strings.HasSuffix(mt, "+xml") || mt == "application/javascript":
		res.Text = strings.TrimSpace(string(decodeCharset(p.Body, p.ContentType)))
		if final != nil {
			res.Title = final.Path
		}
		return res, nil
	}
	return nil, fmt.Errorf("unsupported content type %q (web.fetch reads HTML, text and PDF)", mt)
}

func decodeCharset(b []byte, contentType string) []byte {
	r, err := charset.NewReader(bytes.NewReader(b), contentType)
	if err != nil {
		return b
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return b
	}
	return out
}

// truncateText cuts s to at most n runes, preferring a paragraph or line
// boundary in the last fifth.
func truncateText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i, count := 0, 0
	for i < len(s) && count < n {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		count++
	}
	cut := s[:i]
	floor := len(cut) * 4 / 5
	if j := strings.LastIndex(cut, "\n\n"); j > floor {
		cut = cut[:j]
	} else if j := strings.LastIndex(cut, "\n"); j > floor {
		cut = cut[:j]
	}
	return strings.TrimRight(cut, " \n") + "\n\n…[truncated]"
}
