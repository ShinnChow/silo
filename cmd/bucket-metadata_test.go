// Copyright (c) 2015-2026 MinIO, Inc.
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

package cmd

import "testing"

func TestBucketMetadataCorsRoundTrip(t *testing.T) {
	meta := newBucketMetadata("test-cors")
	meta.CorsConfigXML = []byte(`<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`)
	meta.CorsConfigUpdatedAt = UTCNow()

	buf, err := meta.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var got BucketMetadata
	if _, err := got.UnmarshalMsg(buf); err != nil {
		t.Fatal(err)
	}
	if string(got.CorsConfigXML) != string(meta.CorsConfigXML) {
		t.Fatalf("CorsConfigXML not preserved: %q", string(got.CorsConfigXML))
	}
	if !got.CorsConfigUpdatedAt.Equal(meta.CorsConfigUpdatedAt) {
		t.Fatalf("CorsConfigUpdatedAt not preserved")
	}
}
