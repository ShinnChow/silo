// Copyright (c) 2015-2021 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cors

import (
	"strings"
	"testing"
)

const sampleCORS = `<CORSConfiguration>
  <CORSRule>
    <ID>rule1</ID>
    <AllowedOrigin>http://www.example.com</AllowedOrigin>
    <AllowedOrigin>https://*.example.org</AllowedOrigin>
    <AllowedMethod>GET</AllowedMethod>
    <AllowedMethod>PUT</AllowedMethod>
    <AllowedHeader>x-amz-*</AllowedHeader>
    <ExposeHeader>ETag</ExposeHeader>
    <MaxAgeSeconds>3000</MaxAgeSeconds>
  </CORSRule>
</CORSConfiguration>`

func TestParseAndValidate(t *testing.T) {
	c, err := ParseBucketCorsConfig(strings.NewReader(sampleCORS))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if len(c.CORSRules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(c.CORSRules))
	}
	if c.CORSRules[0].MaxAgeSeconds != 3000 {
		t.Fatalf("MaxAgeSeconds mismatch: %d", c.CORSRules[0].MaxAgeSeconds)
	}
}

func TestValidateRejections(t *testing.T) {
	cases := map[string]string{
		"bad method":            `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>TRACE</AllowedMethod></CORSRule></CORSConfiguration>`,
		"no origin":             `<CORSConfiguration><CORSRule><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
		"empty origin":          `<CORSConfiguration><CORSRule><AllowedOrigin></AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
		"no method":             `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin></CORSRule></CORSConfiguration>`,
		"negative age":          `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><MaxAgeSeconds>-1</MaxAgeSeconds></CORSRule></CORSConfiguration>`,
		"multi wildcard origin": `<CORSConfiguration><CORSRule><AllowedOrigin>https://*.*.example.com</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
		"multi wildcard header": `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><AllowedHeader>x-*-*</AllowedHeader></CORSRule></CORSConfiguration>`,
		"question mark origin":  `<CORSConfiguration><CORSRule><AllowedOrigin>https://?.example.com</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
		"question mark header":  `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><AllowedHeader>x-amz-?</AllowedHeader></CORSRule></CORSConfiguration>`,
		"empty allowed header":  `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><AllowedHeader/></CORSRule></CORSConfiguration>`,
		"empty expose header":   `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><ExposeHeader/></CORSRule></CORSConfiguration>`,
		"overlong id":           `<CORSConfiguration><CORSRule><ID>` + strings.Repeat("a", 256) + `</ID><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
	}
	for name, doc := range cases {
		c, err := ParseBucketCorsConfig(strings.NewReader(doc))
		if err != nil {
			continue // parse-level rejection is acceptable
		}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

func TestMatching(t *testing.T) {
	c, _ := ParseBucketCorsConfig(strings.NewReader(sampleCORS))
	rule, _, ok := c.MatchRule("https://api.example.org", "GET")
	if !ok {
		t.Fatal("expected origin+method to match")
	}
	if _, _, ok := c.MatchRule("http://evil.com", "GET"); ok {
		t.Fatal("did not expect match for disallowed origin")
	}
	if _, _, ok := c.MatchRule("http://www.example.com", "DELETE"); ok {
		t.Fatal("did not expect match for disallowed method")
	}
	allowed, ok := rule.FilterAllowedHeaders([]string{"x-amz-date", "x-amz-content-sha256"})
	if !ok || len(allowed) != 2 {
		t.Fatalf("expected both headers allowed via wildcard, got %v ok=%v", allowed, ok)
	}
	if _, ok := rule.FilterAllowedHeaders([]string{"authorization"}); ok {
		t.Fatal("did not expect authorization to be allowed")
	}
}

func TestMatchPreflightFallsThroughToLaterRule(t *testing.T) {
	// Rule A matches origin+method but only allows a restrictive header set.
	// Rule B, listed after A, matches the same origin+method and allows any
	// header. A preflight requesting a header only B permits must not be
	// rejected just because A was tried first.
	const doc = `<CORSConfiguration>
  <CORSRule>
    <ID>A-restrictive</ID>
    <AllowedOrigin>https://app.example.com</AllowedOrigin>
    <AllowedMethod>GET</AllowedMethod>
    <AllowedHeader>x-amz-date</AllowedHeader>
  </CORSRule>
  <CORSRule>
    <ID>B-permissive</ID>
    <AllowedOrigin>https://app.example.com</AllowedOrigin>
    <AllowedMethod>GET</AllowedMethod>
    <AllowedHeader>*</AllowedHeader>
  </CORSRule>
</CORSConfiguration>`

	c, err := ParseBucketCorsConfig(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	rule, _, allowed, _, ok := c.MatchPreflight("https://app.example.com", "GET", []string{"x-custom-header"})
	if !ok {
		t.Fatal("expected MatchPreflight to succeed via the later, permissive rule")
	}
	if rule.ID != "B-permissive" {
		t.Fatalf("expected rule B-permissive to be selected, got %q", rule.ID)
	}
	if len(allowed) != 1 || allowed[0] != "x-custom-header" {
		t.Fatalf("unexpected allowed headers: %v", allowed)
	}
}

func TestMatchAllowedOriginReturnsFirstMatchingPattern(t *testing.T) {
	rule := Rule{AllowedOrigins: []string{"https://app.example.com", "https://*", "*"}}

	tests := []struct {
		origin string
		want   string
	}{
		{"https://app.example.com", "https://app.example.com"},
		{"https://other.example.com", "https://*"},
		{"http://other.example.com", "*"},
	}

	for _, tt := range tests {
		got, ok := rule.matchAllowedOrigin(tt.origin)
		if !ok {
			t.Fatalf("expected %q to match", tt.origin)
		}
		if got != tt.want {
			t.Fatalf("origin %q matched %q, want %q", tt.origin, got, tt.want)
		}
	}
}

func TestFilterAllowedHeadersPreservesRequestedNames(t *testing.T) {
	rule := Rule{AllowedHeaders: []string{"x-amz-*"}}
	allowed, ok := rule.FilterAllowedHeaders([]string{"X-Amz-Date", " X-AMZ-Meta-Test "})
	if !ok {
		t.Fatal("expected both request headers to match")
	}
	if got := strings.Join(allowed, ","); got != "X-Amz-Date,X-AMZ-Meta-Test" {
		t.Fatalf("allowed headers = %q", got)
	}
}
