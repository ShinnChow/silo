// Copyright (c) 2015-2021 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package cors

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseStandardS3Namespace(t *testing.T) {
	doc := `<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><CORSRule><AllowedOrigin>https://app.example.com</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`
	cfg, err := ParseBucketCorsConfig(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := cfg.MatchRule("https://app.example.com", "GET"); !ok {
		t.Fatal("standard S3 namespace document did not produce a matching rule")
	}
}

const minimalCORSConfig = `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`

func TestParseRejectsTrailingXMLRoot(t *testing.T) {
	for name, suffix := range map[string]string{
		"second root":    `<Extra/>`,
		"text":           `junk`,
		"dangling close": `</Extra>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBucketCorsConfig(strings.NewReader(minimalCORSConfig + suffix)); err == nil {
				t.Fatalf("expected trailing %s to be rejected", name)
			}
		})
	}
}

func TestParseAllowsXMLMiscAfterRoot(t *testing.T) {
	for name, suffix := range map[string]string{
		"whitespace":             " \n\t",
		"comment":                `<!-- trailing comment -->`,
		"processing instruction": `<?cors-test done?>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBucketCorsConfig(strings.NewReader(minimalCORSConfig + suffix)); err != nil {
				t.Fatalf("valid trailing XML misc was rejected: %v", err)
			}
		})
	}
}

func TestValidateCORSRuleIDCountsCharacters(t *testing.T) {
	doc := `<CORSConfiguration><CORSRule><ID>` + strings.Repeat("界", 255) + `</ID><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`
	cfg, err := ParseBucketCorsConfig(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if err = cfg.Validate(); err != nil {
		t.Fatalf("255-character rule ID must be accepted: %v", err)
	}

	cfg.CORSRules[0].ID += "界"
	if err = cfg.Validate(); err == nil {
		t.Fatal("256-character rule ID must be rejected")
	}
}

func TestValidateRejectsNonCanonicalAllowedMethod(t *testing.T) {
	doc := `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>get</AllowedMethod></CORSRule></CORSConfiguration>`
	cfg, err := ParseBucketCorsConfig(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if err = cfg.Validate(); err == nil {
		t.Fatal("expected lowercase AllowedMethod to be rejected")
	}
}

func TestAllowedMethodMatchingIsCaseSensitive(t *testing.T) {
	rule := Rule{AllowedMethods: []string{"GET"}}
	if !rule.HasAllowedMethod("GET") {
		t.Fatal("expected canonical GET to match")
	}
	if rule.HasAllowedMethod("get") {
		t.Fatal("lowercase request method must not match canonical GET")
	}
}

func TestParseRejectsElementsOutsideCORSShape(t *testing.T) {
	tests := map[string]string{
		"unknown root child":  `<CORSConfiguration><Unknown/><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
		"unknown rule child":  `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><Unknown/></CORSRule></CORSConfiguration>`,
		"nested origin child": `<CORSConfiguration><CORSRule><AllowedOrigin><Unknown/></AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
		"duplicate id":        `<CORSConfiguration><CORSRule><ID>a</ID><ID>b</ID><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
		"duplicate max age":   `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><MaxAgeSeconds>1</MaxAgeSeconds><MaxAgeSeconds>2</MaxAgeSeconds></CORSRule></CORSConfiguration>`,
		"empty max age":       `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><MaxAgeSeconds/></CORSRule></CORSConfiguration>`,
		"overflow max age":    `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><MaxAgeSeconds>2147483648</MaxAgeSeconds></CORSRule></CORSConfiguration>`,
	}

	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBucketCorsConfig(strings.NewReader(doc)); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestMaxAgeSecondsPresence(t *testing.T) {
	tests := []struct {
		name    string
		element string
		value   int
		present bool
	}{
		{name: "absent"},
		{name: "zero", element: `<MaxAgeSeconds>0</MaxAgeSeconds>`, present: true},
		{name: "positive", element: `<MaxAgeSeconds>3000</MaxAgeSeconds>`, value: 3000, present: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod>` + tt.element + `</CORSRule></CORSConfiguration>`
			cfg, err := ParseBucketCorsConfig(strings.NewReader(doc))
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			rule := cfg.CORSRules[0]
			_, _, _, maxAgeSeconds, ok := cfg.MatchPreflight("https://example.com", "GET", nil)
			if !ok {
				t.Fatal("expected rule to match")
			}
			present := maxAgeSeconds != nil
			if rule.MaxAgeSeconds != tt.value || present != tt.present {
				t.Fatalf("MaxAgeSeconds = %d, present = %v", rule.MaxAgeSeconds, present)
			}
		})
	}
}

func TestValidateRuleCountBoundary(t *testing.T) {
	rule := Rule{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"GET"}}
	cfg := Config{CORSRules: make([]Rule, 100)}
	for i := range cfg.CORSRules {
		cfg.CORSRules[i] = rule
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("100 rules must be accepted: %v", err)
	}
	cfg.CORSRules = append(cfg.CORSRules, rule)
	if err := cfg.Validate(); err == nil {
		t.Fatal("101 rules must be rejected")
	}
}

func TestValidateMaxAgeSecondsBoundary(t *testing.T) {
	cfg := Config{CORSRules: []Rule{{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET"},
		MaxAgeSeconds:  maxCORSMaxAgeSeconds,
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("MaxAgeSeconds int32 maximum must be accepted: %v", err)
	}
	if strconv.IntSize > 32 {
		overflow := int64(maxCORSMaxAgeSeconds) + 1
		cfg.CORSRules[0].MaxAgeSeconds = int(overflow)
		if err := cfg.Validate(); err == nil {
			t.Fatal("MaxAgeSeconds above int32 maximum must be rejected")
		}
	}
}

func TestSingleWildcardMatching(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"*", "https://example.com", true},
		{"https://*.example.com", "https://api.example.com", true},
		{"https://*.example.com", "https://.example.com", true},
		{"https://*.example.com", "http://api.example.com", false},
		{"https://?.example.com", "https://a.example.com", false},
	}
	for _, tt := range tests {
		if got := matchSingleWildcard(tt.pattern, tt.value); got != tt.want {
			t.Errorf("matchSingleWildcard(%q, %q) = %v, want %v", tt.pattern, tt.value, got, tt.want)
		}
	}
}

func TestMatchRuleReturnsMatchedOriginPattern(t *testing.T) {
	cfg := Config{CORSRules: []Rule{{
		AllowedOrigins: []string{"https://app.example.com", "https://*", "*"},
		AllowedMethods: []string{"GET"},
	}}}
	tests := []struct {
		origin string
		want   string
	}{
		{"https://app.example.com", "https://app.example.com"},
		{"https://other.example.com", "https://*"},
		{"http://other.example.com", "*"},
	}
	for _, tt := range tests {
		_, got, ok := cfg.MatchRule(tt.origin, "GET")
		if !ok || got != tt.want {
			t.Errorf("origin %q matched %q, ok=%v; want %q", tt.origin, got, ok, tt.want)
		}
	}
}
