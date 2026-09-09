//go:build ignore

// Loopback-only synthetic OIDC/TLS fixture. Only disposable lab identities are used.
// Rejection modes model hypotheses; they are not evidence about the user's IdP.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type observedConn struct {
	net.Conn
	readBytes int
}

func (c *observedConn) Read(p []byte) (int, error) {
	n, e := c.Conn.Read(p)
	c.readBytes += n
	return n, e
}
func (c *observedConn) reset() { _ = c.Conn.(*net.TCPConn).SetLinger(0); _ = c.Conn.Close() }

type observedListener struct{ net.Listener }

func (l observedListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	return &observedConn{Conn: c}, nil
}

func main() {
	dir := flag.String("dir", "", "isolated output directory for public CA, URL, and mode file")
	tls13 := flag.Bool("tls13", false, "allow TLS 1.3 in addition to the TLS 1.2 baseline")
	flag.Parse()
	if *dir == "" {
		panic("-dir required")
	}
	must(os.MkdirAll(*dir, 0700))
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(err)
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "issue-154 local CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	must(err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: root.NotBefore, NotAfter: root.NotAfter, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, key)
	must(err)
	must(os.WriteFile(filepath.Join(*dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}), 0600))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	base := "https://" + ln.Addr().String()
	must(os.WriteFile(filepath.Join(*dir, "url"), []byte(base), 0600))
	mode := func() string { b, _ := os.ReadFile(filepath.Join(*dir, "mode")); return strings.TrimSpace(string(b)) }
	var mu sync.Mutex
	log := func(v any) { mu.Lock(); defer mu.Unlock(); _ = json.NewEncoder(os.Stdout).Encode(v) }
	tc := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, rootDER}, PrivateKey: key}}, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, CurvePreferences: []tls.CurveID{tls.CurveP256}, CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}}
	if *tls13 {
		tc.MaxVersion = tls.VersionTLS13
	}
	peerConfig := tc.Clone()
	peerConfig.NextProtos = []string{"h2", "http/1.1"}
	// net/http validates HTTP/2 support before GetConfigForClient; the fixture
	// then deliberately selects only the reported AES-256 suite.
	tc.CipherSuites = append(tc.CipherSuites, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256)
	tc.GetConfigForClient = func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
		m := mode()
		c := chi.Conn.(*observedConn)
		log(map[string]any{"event": "hello", "mode": m, "bytes_read": c.readBytes, "curves": chi.SupportedCurves, "signatures": chi.SignatureSchemes, "alpn": chi.SupportedProtos, "versions": chi.SupportedVersions})
		reject := m == "reset" || m == "reject-mlkem" && slices.Contains(chi.SupportedCurves, tls.CurveID(4588)) || m == "reject-mldsa" && slices.Contains(chi.SignatureSchemes, tls.SignatureScheme(0x0904)) || m == "require-h2" && !slices.Contains(chi.SupportedProtos, "h2")
		if reject {
			c.reset()
			return nil, errors.New("synthetic ClientHello rejection")
		}
		return peerConfig, nil
	}
	server := &http.Server{TLSConfig: tc, ReadHeaderTimeout: 5 * time.Second}
	var codes sync.Map
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log(map[string]any{"event": "request", "path": r.URL.Path, "protocol": r.Proto, "tls": r.TLS.Version, "cipher": r.TLS.CipherSuite, "resumed": r.TLS.DidResume, "ua": r.UserAgent()})
		if mode() == "reject-silo-ua" && strings.HasPrefix(r.UserAgent(), "Silo") {
			c, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				c.(*tls.Conn).NetConn().(*observedConn).reset()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/authorize":
			q := r.URL.Query()
			redirect, err := url.Parse(q.Get("redirect_uri"))
			if err != nil || redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Path != "/oauth_callback" || q.Get("client_id") != "local154" {
				http.Error(w, "loopback lab authorization only", 400)
				return
			}
			codeBytes := make([]byte, 18)
			_, err = rand.Read(codeBytes)
			must(err)
			code := base64.RawURLEncoding.EncodeToString(codeBytes)
			codes.Store(code, q.Get("nonce"))
			values := redirect.Query()
			values.Set("code", code)
			values.Set("state", q.Get("state"))
			redirect.RawQuery = values.Encode()
			http.Redirect(w, r, redirect.String(), http.StatusFound)
		case "/token":
			if r.Method != http.MethodPost || r.ParseForm() != nil {
				http.Error(w, "bad token request", 400)
				return
			}
			id, secret, ok := r.BasicAuth()
			if !ok {
				id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
			}
			nonce, found := codes.LoadAndDelete(r.Form.Get("code"))
			if id != "local154" || secret != "local154-placeholder" || !found || r.Form.Get("grant_type") != "authorization_code" {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			audience := id
			if mode() == "bad-audience" {
				audience = "different-lab-client"
			}
			claims, _ := json.Marshal(map[string]any{"iss": base, "sub": "local154-user", "aud": audience, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "policy": "readwrite", "nonce": nonce})
			header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"local-154","typ":"JWT"}`))
			payload := header + "." + base64.RawURLEncoding.EncodeToString(claims)
			hash := sha256.Sum256([]byte(payload))
			signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
			must(err)
			if mode() == "bad-signature" {
				signature[0] ^= 1
			}
			token := payload + "." + base64.RawURLEncoding.EncodeToString(signature)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "id_token": token, "token_type": "Bearer", "expires_in": 3600})
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": base, "jwks_uri": base + "/jwks", "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "scopes_supported": []string{"openid"}})
		case "/jwks":
			if mode() == "bad-jwks" {
				http.Error(w, "synthetic JWKS outage", 503)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "local-154", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
		default:
			http.NotFound(w, r)
		}
	})
	fmt.Fprintln(os.Stderr, base)
	must(server.ServeTLS(observedListener{ln}, "", ""))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
