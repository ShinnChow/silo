// Copyright (c) 2015-2021 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/minio/minio/internal/auth"
)

func TestPutBucketCorsWireValidation(t *testing.T) {
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testPutBucketCorsWireValidation,
		endpoints:  []string{"PutBucketCors"},
	})
}

func testPutBucketCorsWireValidation(_ ObjectLayer, _ string, bucketName string, apiRouter http.Handler,
	creds auth.Credentials, t *testing.T,
) {
	valid := `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`
	rule := `<CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule>`
	tests := []struct {
		name     string
		body     string
		want     int
		wantCode string
	}{
		{
			name:     "second XML root",
			body:     valid + `<Extra/>`,
			want:     http.StatusBadRequest,
			wantCode: "MalformedXML",
		},
		{
			name: "255 Unicode character ID",
			body: `<CORSConfiguration><CORSRule><ID>` + strings.Repeat("界", 255) + `</ID><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
			want: http.StatusOK,
		},
		{
			name:     "256 Unicode character ID",
			body:     `<CORSConfiguration><CORSRule><ID>` + strings.Repeat("界", 256) + `</ID><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
			want:     http.StatusBadRequest,
			wantCode: "MalformedXML",
		},
		{
			name:     "lowercase method",
			body:     `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>get</AllowedMethod></CORSRule></CORSConfiguration>`,
			want:     http.StatusBadRequest,
			wantCode: "MalformedXML",
		},
		{
			name:     "empty origin",
			body:     `<CORSConfiguration><CORSRule><AllowedOrigin/><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
			want:     http.StatusBadRequest,
			wantCode: "MalformedXML",
		},
		{
			name:     "question mark origin wildcard",
			body:     `<CORSConfiguration><CORSRule><AllowedOrigin>https://?.example.com</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`,
			want:     http.StatusBadRequest,
			wantCode: "MalformedXML",
		},
		{
			name:     "question mark header wildcard",
			body:     `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><AllowedHeader>x-amz-?</AllowedHeader></CORSRule></CORSConfiguration>`,
			want:     http.StatusBadRequest,
			wantCode: "MalformedXML",
		},
		{
			name:     "unknown element",
			body:     `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><Unknown/></CORSRule></CORSConfiguration>`,
			want:     http.StatusBadRequest,
			wantCode: "MalformedXML",
		},
		{
			name:     "empty max age",
			body:     `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><MaxAgeSeconds/></CORSRule></CORSConfiguration>`,
			want:     http.StatusBadRequest,
			wantCode: "MalformedXML",
		},
		{
			name: "zero max age",
			body: `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><MaxAgeSeconds>0</MaxAgeSeconds></CORSRule></CORSConfiguration>`,
			want: http.StatusOK,
		},
		{
			name:     "max age int32 overflow",
			body:     `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><MaxAgeSeconds>2147483648</MaxAgeSeconds></CORSRule></CORSConfiguration>`,
			want:     http.StatusBadRequest,
			wantCode: "MalformedXML",
		},
		{
			name: "100 rules",
			body: `<CORSConfiguration>` + strings.Repeat(rule, 100) + `</CORSConfiguration>`,
			want: http.StatusOK,
		},
		{
			name:     "101 rules",
			body:     `<CORSConfiguration>` + strings.Repeat(rule, 101) + `</CORSConfiguration>`,
			want:     http.StatusBadRequest,
			wantCode: "MalformedXML",
		},
		{
			name: "exactly 64 KiB",
			body: sizedCORSConfig(maxBucketCorsSize),
			want: http.StatusOK,
		},
		{
			name:     "over 64 KiB",
			body:     sizedCORSConfig(maxBucketCorsSize + 1),
			want:     http.StatusBadRequest,
			wantCode: "EntityTooLarge",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := newTestSignedRequestV4(http.MethodPut, getBucketCorsURL("", bucketName),
				int64(len(tt.body)), bytes.NewReader([]byte(tt.body)), creds.AccessKey, creds.SecretKey, nil)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			apiRouter.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("expected status %d, got %d: %s", tt.want, rec.Code, rec.Body.String())
			}
			if tt.wantCode != "" && !bytes.Contains(rec.Body.Bytes(), []byte("<Code>"+tt.wantCode+"</Code>")) {
				t.Fatalf("expected error code %s, got: %s", tt.wantCode, rec.Body.String())
			}
		})
	}
}

func sizedCORSConfig(size int) string {
	prefix := `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod>`
	suffix := `</CORSRule></CORSConfiguration>`
	return prefix + strings.Repeat(" ", size-len(prefix)-len(suffix)) + suffix
}

func TestPutBucketCorsChecksumValidation(t *testing.T) {
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testPutBucketCorsChecksumValidation,
		endpoints:  []string{"PutBucketCors"},
	})
}

func testPutBucketCorsChecksumValidation(_ ObjectLayer, _ string, bucketName string, apiRouter http.Handler,
	creds auth.Credentials, t *testing.T,
) {
	body := []byte(`<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`)
	tests := []struct {
		name      string
		configure func(*http.Request)
		want      int
		wantCode  string
	}{
		{
			name: "missing checksum",
			configure: func(req *http.Request) {
				req.Header.Del("Content-Md5")
			},
			want:     http.StatusBadRequest,
			wantCode: "MissingContentMD5",
		},
		{
			name: "bad content md5",
			configure: func(req *http.Request) {
				req.Header.Set("Content-Md5", getMD5HashBase64([]byte("different body")))
			},
			want:     http.StatusBadRequest,
			wantCode: "BadDigest",
		},
		{
			name: "valid sdk crc32",
			configure: func(req *http.Request) {
				req.Header.Del("Content-Md5")
				req.Header.Set("X-Amz-Sdk-Checksum-Algorithm", "CRC32")
				req.Header.Set("X-Amz-Checksum-Crc32", corsCRC32Base64(body))
			},
			want: http.StatusOK,
		},
		{
			name: "bad sdk crc32",
			configure: func(req *http.Request) {
				req.Header.Del("Content-Md5")
				req.Header.Set("X-Amz-Sdk-Checksum-Algorithm", "CRC32")
				req.Header.Set("X-Amz-Checksum-Crc32", corsCRC32Base64([]byte("different body")))
			},
			want:     http.StatusBadRequest,
			wantCode: "BadDigest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := newTestRequest(http.MethodPut, getBucketCorsURL("", bucketName), int64(len(body)), bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			tt.configure(req)
			if err = signRequestV4(req, creds.AccessKey, creds.SecretKey); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			apiRouter.ServeHTTP(rec, req)
			if rec.Code != tt.want || (tt.wantCode != "" && !bytes.Contains(rec.Body.Bytes(), []byte("<Code>"+tt.wantCode+"</Code>"))) {
				t.Fatalf("expected status %d and code %s, got %d: %s", tt.want, tt.wantCode, rec.Code, rec.Body.String())
			}
		})
	}
}

func corsCRC32Base64(data []byte) string {
	var checksum [4]byte
	binary.BigEndian.PutUint32(checksum[:], crc32.ChecksumIEEE(data))
	return base64.StdEncoding.EncodeToString(checksum[:])
}
