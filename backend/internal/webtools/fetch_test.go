package webtools

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testFetchConfig() FetchConfig {
	c := Config{}
	c.defaults()
	c.Fetch.Timeout = Duration(5 * time.Second)
	return c.Fetch
}

func portOfServer(t *testing.T, s *httptest.Server) int {
	t.Helper()
	u, _ := url.Parse(s.URL)
	p, _ := strconv.Atoi(u.Port())
	return p
}

func TestIsPublicAddr(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.5.4": false, "192.168.1.10": false,
		"169.254.169.254": false, "100.100.1.1": false, "0.0.0.0": false, "::1": false,
		"fe80::1": false, "fd00::1": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false,
		"224.0.0.1": false, "255.255.255.255": false, "64:ff9b::a00:1": false, "64:ff9b::808:808": true,
		"2002:c0a8:0101::1": false, "198.18.0.1": false,
	}
	for s, want := range cases {
		if got := IsPublicAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("IsPublicAddr(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestSSRFBlocked(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "<p>secret</p>")
	}))
	defer srv.Close()
	f := NewFetcher(testFetchConfig(), nil)
	ctx := context.Background()
	for _, u := range []string{
		srv.URL + "/", // 127.0.0.1 via dial check
		"http://localhost:" + strconv.Itoa(portOfServer(t, srv)) + "/", // name resolving to loopback
		"http://10.1.2.3/", "http://169.254.169.254/latest/meta-data/", "http://[::1]/", "http://[::ffff:127.0.0.1]/",
		"http://0.0.0.0/", "file:///etc/passwd", "ftp://example.com/", "http://user:pw@example.com/",
	} {
		_, err := f.Fetch(ctx, u, 0)
		if !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: err = %v, want blocked", u, err)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("blocked server was reached %d times", hits.Load())
	}
	// allow_private and allow_hosts open it up.
	cfg := testFetchConfig()
	cfg.AllowPrivate = true
	if _, err := NewFetcher(cfg, nil).Fetch(ctx, srv.URL, 0); err != nil {
		t.Errorf("allow_private: %v", err)
	}
	cfg = testFetchConfig()
	cfg.AllowHosts = []string{"127.0.0.0/8"}
	if _, err := NewFetcher(cfg, nil).Fetch(ctx, srv.URL, 0); err != nil {
		t.Errorf("allow_hosts cidr: %v", err)
	}
	cfg.AllowHosts = []string{"localhost"}
	if _, err := NewFetcher(cfg, nil).Fetch(ctx, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), 0); err != nil {
		t.Errorf("allow_hosts name: %v", err)
	}
	cfg = testFetchConfig()
	cfg.AllowPrivate = true
	cfg.DenyHosts = []string{"127.0.0.1"}
	if _, err := NewFetcher(cfg, nil).Fetch(ctx, srv.URL, 0); !errors.Is(err, ErrBlocked) {
		t.Errorf("deny_hosts: %v", err)
	}
}

func TestSSRFRedirects(t *testing.T) {
	var privateHits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateHits.Add(1)
		fmt.Fprint(w, "<p>internal admin</p>")
	}))
	defer private.Close()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-private":
			http.Redirect(w, r, private.URL+"/admin", http.StatusFound)
		case "/to-10":
			http.Redirect(w, r, "http://10.0.0.5/", http.StatusFound)
		case "/to-metadata":
			http.Redirect(w, r, "http://169.254.169.254/", http.StatusMovedPermanently)
		case "/to-file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/ok":
			http.Redirect(w, r, "/final", http.StatusFound)
		default:
			fmt.Fprint(w, "<html><head><title>Final page</title></head><body><p>public content here</p></body></html>")
		}
	}))
	defer public.Close()
	publicPort := portOfServer(t, public)
	f := NewFetcher(testFetchConfig(), nil)
	// Pretend the "public" test server is on the internet.
	f.guard.AddrOK = func(ip netip.Addr, port int) bool { return IsPublicAddr(ip) || port == publicPort }
	ctx := context.Background()
	res, err := f.Fetch(ctx, public.URL+"/ok", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.FinalURL, "/final") || res.Title != "Final page" {
		t.Errorf("res = %+v", res)
	}
	for _, p := range []string{"/to-private", "/to-10", "/to-metadata"} {
		if _, err := f.Fetch(ctx, public.URL+p, 0); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: err = %v, want blocked", p, err)
		}
	}
	if _, err := f.Fetch(ctx, public.URL+"/to-file", 0); err == nil {
		t.Error("redirect to file:// should fail")
	}
	if _, err := f.Fetch(ctx, public.URL+"/loop", 0); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Errorf("loop: %v", err)
	}
	if privateHits.Load() != 0 {
		t.Errorf("private server reached %d times", privateHits.Load())
	}
}

func privateFetcher(t *testing.T, cache *diskCache) *Fetcher {
	t.Helper()
	cfg := testFetchConfig()
	cfg.AllowPrivate = true
	return NewFetcher(cfg, cache)
}

func TestFetchSizeCaps(t *testing.T) {
	big := "<html><head><title>Big</title></head><body><article>" +
		strings.Repeat("<p>This is a long paragraph of article text, repeated many times to exceed caps.</p>\n", 2000) +
		"</article></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, big)
	}))
	defer srv.Close()
	cfg := testFetchConfig()
	cfg.AllowPrivate = true
	cfg.MaxDownloadBytes = 20000
	f := NewFetcher(cfg, nil)
	res, err := f.Fetch(context.Background(), srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Text) > 20000 || !strings.Contains(res.Text, "long paragraph") {
		t.Errorf("download cap: truncated=%v len=%d", res.Truncated, len(res.Text))
	}
	f = privateFetcher(t, nil)
	res, err = f.Fetch(context.Background(), srv.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len([]rune(res.Text)) > 1000+20 || res.TotalChars < 100000 {
		t.Errorf("char cap: truncated=%v len=%d total=%d", res.Truncated, len(res.Text), res.TotalChars)
	}
	// Default cap.
	res, _ = f.Fetch(context.Background(), srv.URL, 0)
	if len([]rune(res.Text)) > DefaultMaxChars+20 {
		t.Errorf("default cap: %d", len(res.Text))
	}
}

func TestFetchCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprintf(w, "<html><body><p>Version %d of the page has enough text to be content.</p></body></html>", hits.Load())
	}))
	defer srv.Close()
	cache, err := newDiskCache(t.TempDir(), time.Hour, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	f := privateFetcher(t, cache)
	ctx := context.Background()
	r1, err := f.Fetch(ctx, srv.URL+"/p#frag", 0)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := f.Fetch(ctx, srv.URL+"/p", 0)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 || r1.Cached || !r2.Cached || r2.Text != r1.Text {
		t.Errorf("hits=%d r1.cached=%v r2.cached=%v", hits.Load(), r1.Cached, r2.Cached)
	}
	// Expired entries are refetched.
	cache.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	r3, err := f.Fetch(ctx, srv.URL+"/p", 0)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 || r3.Cached || !strings.Contains(r3.Text, "Version 2") {
		t.Errorf("after ttl: hits=%d cached=%v text=%q", hits.Load(), r3.Cached, r3.Text)
	}
	// Prune removes expired entries.
	cache.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	if n, err := cache.Prune(); err != nil || n != 1 {
		t.Errorf("prune = %d, %v", n, err)
	}
	// Size-based pruning.
	cache.now = time.Now
	cache.maxBytes = 10
	_ = cache.Put(&cachedPage{URL: "u1", FetchedAt: time.Now(), Body: []byte("0123456789abcdef")})
	if n, _ := cache.Prune(); n != 1 {
		t.Errorf("size prune = %d", n)
	}
	// Errors are not cached.
	var fails atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fails.Add(1)
		http.NotFound(w, r)
	}))
	defer bad.Close()
	for i := 0; i < 2; i++ {
		if _, err := f.Fetch(ctx, bad.URL, 0); err == nil || !strings.Contains(err.Error(), "404") {
			t.Errorf("404: %v", err)
		}
	}
	if fails.Load() != 2 {
		t.Errorf("404 cached: %d", fails.Load())
	}
}

func TestFetchContentTypes(t *testing.T) {
	latin1 := []byte("<html><head><meta charset=\"iso-8859-1\"><title>Cora\xe7\xe3o</title></head><body><p>A p\xe1gina tem texto suficiente em portugu\xeas para ser conte\xfado.</p></body></html>")
	pdf := makePDF(t, "Hello PDF world. This is a small test document with enough words to pass the text check.")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latin1":
			w.Header().Set("Content-Type", "text/html")
			w.Write(latin1)
		case "/plain":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, "just text\n")
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"a":1}`)
		case "/doc.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			w.Write(pdf)
		case "/garbage.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			w.Write([]byte("%PDF-1.4\n1 0 obj\n<< /Length 10 /Filter /DCTDecode >>\nstream\n\x01\x02\x03\nendstream\nendobj\n"))
		case "/img":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("\x89PNG\r\n\x1a\n...."))
		}
	}))
	defer srv.Close()
	f := privateFetcher(t, nil)
	ctx := context.Background()
	res, err := f.Fetch(ctx, srv.URL+"/latin1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "Coração" || !strings.Contains(res.Text, "página tem texto suficiente em português") {
		t.Errorf("latin1: %q %q", res.Title, res.Text)
	}
	if res, err := f.Fetch(ctx, srv.URL+"/plain", 0); err != nil || res.Text != "just text" || res.ContentType != "text/plain" {
		t.Errorf("plain: %+v %v", res, err)
	}
	if res, err := f.Fetch(ctx, srv.URL+"/json", 0); err != nil || res.Text != `{"a":1}` {
		t.Errorf("json: %+v %v", res, err)
	}
	res, err = f.Fetch(ctx, srv.URL+"/doc.pdf", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentType != "application/pdf" || !strings.Contains(res.Text, "Hello PDF world.") || res.Note != pdfNoteOK || res.Title != "Test Title" {
		t.Errorf("pdf: %+v", res)
	}
	res, err = f.Fetch(ctx, srv.URL+"/garbage.pdf", 0)
	if err != nil || res.Text != "" || res.Note != pdfNoteFail || res.Title != "garbage.pdf" {
		t.Errorf("garbage pdf: %+v %v", res, err)
	}
	if _, err := f.Fetch(ctx, srv.URL+"/img", 0); err == nil || !strings.Contains(err.Error(), "unsupported content type") {
		t.Errorf("img: %v", err)
	}
}

// makePDF builds a minimal PDF with one Flate-compressed and one plain
// content stream.
func makePDF(t *testing.T, text string) []byte {
	t.Helper()
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	fmt.Fprintf(zw, "BT /F1 12 Tf 72 712 Td (%s) Tj ET", strings.ReplaceAll(text, "(", `\(`))
	zw.Close()
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	b.WriteString("1 0 obj\n<< /Title (Test Title) /Producer (x) >>\nendobj\n")
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d /Filter /FlateDecode >>\nstream\n", z.Len())
	b.Write(z.Bytes())
	b.WriteString("\nendstream\nendobj\n")
	plain := "BT 72 700 Td [(Second) -300 (line) 120 (s)] TJ 0 -14 Td <48656C6C6F> Tj ET"
	fmt.Fprintf(&b, "5 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n%%%%EOF\n", len(plain), plain)
	return b.Bytes()
}

func TestExtractPDFText(t *testing.T) {
	text, note := ExtractPDFText(makePDF(t, "Alpha beta gamma delta epsilon zeta eta theta iota kappa lambda."))
	if note != pdfNoteOK {
		t.Fatalf("note = %q", note)
	}
	if !strings.Contains(text, "Alpha beta gamma") || !strings.Contains(text, "Second lines") || !strings.Contains(text, "Hello") {
		t.Errorf("text = %q", text)
	}
	if text, note := ExtractPDFText([]byte("%PDF-1.7\n/Encrypt 5 0 R\n")); text != "" || note != pdfNoteFail {
		t.Errorf("encrypted: %q %q", text, note)
	}
	s, n := readLiteral([]byte(`(a\(b\) \101 (nested) \\ end) tail`))
	if s != `a(b) A (nested) \ end` || n != len(`(a\(b\) \101 (nested) \\ end)`) {
		t.Errorf("readLiteral = %q, %d", s, n)
	}
	if got := bytesToText([]byte{0xFE, 0xFF, 0, 'H', 0, 'i'}); got != "Hi" {
		t.Errorf("utf16 = %q", got)
	}
}

func TestNormalizeURL(t *testing.T) {
	u, err := normalizeURL("example.com/a#x")
	if err != nil || u.String() != "https://example.com/a" {
		t.Errorf("u = %v %v", u, err)
	}
	if _, err := normalizeURL(" "); err == nil {
		t.Error("empty url accepted")
	}
}
