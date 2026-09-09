//go:build ignore

// Run only with the public, synthetic CA made by fixture.go.
package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"runtime"

	"github.com/pgsty/silo-pkg/v3/certs"
)

func main() {
	if len(os.Args) != 3 {
		panic("usage: cert-roots synthetic-ca.pem explicit-ca-path-or-empty")
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		panic("expected public certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		panic(err)
	}
	roots, err := certs.GetRootCAs(os.Args[2])
	if err != nil {
		panic(err)
	}
	_, err = certificate.Verify(x509.VerifyOptions{Roots: roots})
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"go": runtime.Version(), "os": runtime.GOOS,
		"explicit_ca": os.Args[2] != "", "trusted": err == nil,
	})
}
