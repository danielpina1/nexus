package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
)

// transport returns the HTTP transport for the hub. With caFile the hub's
// private CA is the only trusted root (TLS 1.3); without it the system
// roots are used.
func transport(caFile string) (http.RoundTripper, error) {
	if caFile == "" {
		return http.DefaultTransport, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("ca file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("ca file %s: no PEM certificates", caFile)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool}
	tr.ForceAttemptHTTP2 = true
	return tr, nil
}
