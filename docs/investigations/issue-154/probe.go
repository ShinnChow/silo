//go:build ignore

// Diagnostic GET using the Server's actual transport constructor.
// Build explicitly from the SILO module root; see ../issue-154.md.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio/cmd"
	xhttp "github.com/minio/minio/internal/http"
	"github.com/pgsty/silo-pkg/v3/certs"
)

func main() {
	endpoint := flag.String("url", os.Getenv("OIDC_URL"), "discovery URL; no credentials or query string")
	ca := flag.String("ca", "", "same CA file or certs/CAs directory as Server")
	h2 := flag.Bool("h2", false, "diagnostic: opt in to HTTP/2")
	classical := flag.Bool("classical", false, "diagnostic: omit hybrid key exchange only")
	defaultCurves := flag.Bool("default-curves", false, "diagnostic: let Go choose curves and honor its GODEBUG defaults")
	tls12 := flag.Bool("tls12", false, "diagnostic: TLS 1.2 only; keeps certificate verification")
	direct := flag.Bool("direct", false, "diagnostic: bypass environment proxy")
	ip := flag.String("ip", "", "diagnostic: pin destination IP, preserving Host/SNI; requires -direct")
	fresh := flag.Bool("fresh", false, "diagnostic: close idle connections between requests")
	ua := flag.String("ua", "issue-154-probe", "HTTP User-Agent; supply actual Server UA to investigate a WAF")
	n := flag.Int("n", 1, "number of GETs (1 to 3)")
	flag.Parse()
	u, err := url.Parse(*endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || *n < 1 || *n > 3 {
		fmt.Fprintln(os.Stderr, "require an HTTPS URL without credentials/query/fragment and -n between 1 and 3")
		os.Exit(2)
	}
	if *ip != "" && (!*direct || net.ParseIP(*ip) == nil) {
		fmt.Fprintln(os.Stderr, "-ip requires a literal IP and -direct")
		os.Exit(2)
	}
	if *classical && *defaultCurves {
		fmt.Fprintln(os.Stderr, "choose at most one of -classical and -default-curves")
		os.Exit(2)
	}
	tr := cmd.NewHTTPTransport()
	tr.TLSClientConfig.RootCAs, err = certs.GetRootCAs(*ca)
	if err != nil {
		fmt.Fprintln(os.Stderr, "CA loading failed; check the local CA path")
		os.Exit(2)
	}
	if *h2 {
		tr.ForceAttemptHTTP2 = true
	}
	if *classical {
		tr.TLSClientConfig.CurvePreferences = []tls.CurveID{tls.CurveP256, tls.X25519, tls.CurveP384, tls.CurveP521}
	}
	if *defaultCurves {
		tr.TLSClientConfig.CurvePreferences = nil
	}
	if *tls12 {
		tr.TLSClientConfig.MinVersion = tls.VersionTLS12
		tr.TLSClientConfig.MaxVersion = tls.VersionTLS12
	}
	if *direct {
		tr.Proxy = nil
	}
	if *ip != "" {
		base := tr.DialContext
		tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, e := net.SplitHostPort(address)
			if e != nil {
				return nil, e
			}
			if host == u.Hostname() {
				address = net.JoinHostPort(*ip, port)
			}
			return base(ctx, network, address)
		}
	}
	defer tr.CloseIdleConnections()
	var mu sync.Mutex
	log := func(format string, args ...any) { mu.Lock(); defer mu.Unlock(); fmt.Printf(format+"\n", args...) }
	log("go=%s os=%s arch=%s h2=%v classical=%v default_curves=%v tls12=%v", runtime.Version(), runtime.GOOS, runtime.GOARCH, *h2, *classical, *defaultCurves, *tls12)
	// Deliberately print no URL, headers, body, client ID, secret, or token.
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	proxy := "direct"
	if tr.Proxy != nil {
		p, e := tr.Proxy(req)
		if e != nil {
			log("proxy_selection_error=%T", e)
			os.Exit(2)
		}
		if p != nil {
			proxy = p.Scheme + " proxy (address omitted)"
		}
	}
	log("route=%s curves=%v", proxy, tr.TLSClientConfig.CurvePreferences)
	client := &http.Client{Transport: xhttp.WithUserAgent(tr, func() string { return *ua }), Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	failed := false
	for i := 0; i < *n; i++ {
		if *fresh {
			tr.CloseIdleConnections()
		}
		log("request=%d", i+1)
		trace := &httptrace.ClientTrace{
			DNSDone:           func(d httptrace.DNSDoneInfo) { log("dns_addresses=%v err=%s", d.Addrs, errorClass(d.Err)) },
			ConnectStart:      func(network, addr string) { log("connect=%s %s", network, addr) },
			ConnectDone:       func(_, addr string, e error) { log("connected=%s err=%s", addr, errorClass(e)) },
			TLSHandshakeStart: func() { log("tls_start") },
			TLSHandshakeDone: func(s tls.ConnectionState, e error) {
				log("tls_done=0x%x cipher=%s alpn=%q resumed=%v verified_chains=%d err=%s", s.Version, tls.CipherSuiteName(s.CipherSuite), s.NegotiatedProtocol, s.DidResume, len(s.VerifiedChains), errorClass(e))
			},
			GotConn:              func(c httptrace.GotConnInfo) { log("got_conn=%s reused=%v", c.Conn.RemoteAddr(), c.Reused) },
			WroteRequest:         func(w httptrace.WroteRequestInfo) { log("wrote_request err=%s", errorClass(w.Err)) },
			GotFirstResponseByte: func() { log("first_response_byte") },
		}
		r := req.Clone(httptrace.WithClientTrace(context.Background(), trace))
		resp, e := client.Do(r)
		if e != nil {
			log("get_error=%s", errorClass(e))
			failed = true
			continue
		}
		log("status=%d protocol=%s", resp.StatusCode, resp.Proto)
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if e != nil || resp.StatusCode != http.StatusOK {
			failed = true
			log("body_error=%s", errorClass(e))
		}
	}
	if failed {
		os.Exit(1)
	}
}

func errorClass(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	// Error text may contain a private URL. Emit only category and concrete type.
	category := "other"
	s := err.Error()
	for _, k := range []string{"connection reset by peer", "x509:", "TLS handshake timeout", "connection refused", "EOF"} {
		if strings.Contains(s, k) {
			category = k
			break
		}
	}
	return fmt.Sprintf("%s (%T)", category, err)
}
