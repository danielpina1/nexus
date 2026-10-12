package webtools

import (
	"net/url"
	"strings"
)

// trackingParams are removed from result and link URLs.
var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "gclsrc": true, "dclid": true, "msclkid": true, "yclid": true,
	"igshid": true, "mc_cid": true, "mc_eid": true, "_hsenc": true, "_hsmi": true, "mkt_tok": true,
	"ref_src": true, "ref_url": true, "spm": true, "scid": true, "twclid": true, "ttclid": true,
	"wt.mc_id": true, "wt_mc_id": true, "oly_anon_id": true, "oly_enc_id": true, "vero_id": true,
	"__s": true, "_ga": true, "_gl": true, "srsltid": true, "rut": true, "ocid": true, "cmpid": true,
}

func isTrackingParam(k string) bool {
	k = strings.ToLower(k)
	return trackingParams[k] || strings.HasPrefix(k, "utm_") || strings.HasPrefix(k, "pk_") ||
		strings.HasPrefix(k, "hsa_") || strings.HasPrefix(k, "itm_")
}

// CleanURL strips tracking parameters and the fragment, lower-cases scheme
// and host and drops default ports. Unparseable input is returned trimmed.
func CleanURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) || (u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) {
		u.Host = u.Host[:strings.LastIndexByte(u.Host, ':')]
	}
	u.Fragment, u.RawFragment = "", ""
	if u.RawQuery != "" {
		parts := strings.Split(u.RawQuery, "&")
		kept := parts[:0]
		for _, p := range parts {
			if p == "" {
				continue
			}
			k := p
			if i := strings.IndexByte(p, '='); i >= 0 {
				k = p[:i]
			}
			if uk, err := url.QueryUnescape(k); err == nil {
				k = uk
			}
			if !isTrackingParam(k) {
				kept = append(kept, p)
			}
		}
		u.RawQuery = strings.Join(kept, "&")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// dedupKey identifies a URL for de-duplication: clean URL without scheme,
// "www." and trailing slash.
func dedupKey(raw string) string {
	c := CleanURL(raw)
	u, err := url.Parse(c)
	if err != nil || u.Host == "" {
		return strings.ToLower(c)
	}
	host := strings.TrimPrefix(u.Host, "www.")
	host = strings.TrimPrefix(host, "m.")
	p := strings.TrimSuffix(u.EscapedPath(), "/")
	k := host + p
	if u.RawQuery != "" {
		k += "?" + u.RawQuery
	}
	return k
}
