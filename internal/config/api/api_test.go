// Copyright (c) 2026 Ruohang Feng
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"testing"

	"github.com/minio/minio/internal/config"
)

func TestMultipartListingMigrationMode(t *testing.T) {
	for _, tc := range []struct {
		name, stored, override, want string
		invalid                      bool
	}{
		{name: "default", want: "strict"},
		{name: "explicit-migration", stored: "legacy", want: "legacy"},
		{name: "environment-override", stored: "strict", override: "legacy", want: "legacy"},
		{name: "invalid-stored", stored: "automatic", invalid: true},
		{name: "invalid-environment", stored: "strict", override: "automatic", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvAPIMultipartListing, tc.override)
			var kvs config.KVS
			if tc.stored != "" {
				kvs = config.KVS{{Key: apiMultipartListing, Value: tc.stored}}
			}
			cfg, err := LookupConfig(kvs)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid migration mode silently accepted")
				}
				return
			}
			if err != nil || cfg.MultipartListing != tc.want {
				t.Fatalf("mode=%q err=%v, want %q", cfg.MultipartListing, err, tc.want)
			}
		})
	}
}
