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

package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/minio/minio/internal/auth"
)

const testCORSDoc = `<CORSConfiguration><CORSRule><AllowedOrigin>http://example.com</AllowedOrigin><AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod><ExposeHeader>ETag</ExposeHeader><MaxAgeSeconds>3000</MaxAgeSeconds></CORSRule></CORSConfiguration>`

func TestBucketCorsHandlers(t *testing.T) {
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{t: t, objAPITest: testBucketCorsHandlers, endpoints: []string{"PutBucketCors", "GetBucketCors", "DeleteBucketCors"}})
}

func testBucketCorsHandlers(obj ObjectLayer, instanceType, bucketName string, apiRouter http.Handler,
	creds auth.Credentials, t *testing.T,
) {
	// PUT
	req, err := newTestSignedRequestV4(http.MethodPut, getBucketCorsURL("", bucketName),
		int64(len(testCORSDoc)), bytes.NewReader([]byte(testCORSDoc)), creds.AccessKey, creds.SecretKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT cors: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// GET returns what we stored
	req, err = newTestSignedRequestV4(http.MethodGet, getBucketCorsURL("", bucketName),
		0, nil, creds.AccessKey, creds.SecretKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET cors: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("http://example.com")) {
		t.Fatalf("GET cors: body missing origin: %s", rec.Body.String())
	}

	// DELETE
	req, err = newTestSignedRequestV4(http.MethodDelete, getBucketCorsURL("", bucketName),
		0, nil, creds.AccessKey, creds.SecretKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE cors: expected 204, got %d", rec.Code)
	}

	// GET after delete → 404 NoSuchCORSConfiguration
	req, err = newTestSignedRequestV4(http.MethodGet, getBucketCorsURL("", bucketName),
		0, nil, creds.AccessKey, creds.SecretKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET cors after delete: expected 404, got %d", rec.Code)
	}

	// Malformed XML → 400
	req, err = newTestSignedRequestV4(http.MethodPut, getBucketCorsURL("", bucketName),
		int64(len("<bad>")), bytes.NewReader([]byte("<bad>")), creds.AccessKey, creds.SecretKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT malformed cors: expected 400, got %d", rec.Code)
	}

	// Missing Content-MD5 is rejected before the body is parsed.
	req, err = newTestRequest(http.MethodPut, getBucketCorsURL("", bucketName),
		int64(len(testCORSDoc)), bytes.NewReader([]byte(testCORSDoc)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Del("Content-Md5")
	if err = signRequestV4(req, creds.AccessKey, creds.SecretKey); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("<Code>MissingContentMD5</Code>")) {
		t.Fatalf("PUT cors without Content-MD5: expected MissingContentMD5, got %d: %s", rec.Code, rec.Body.String())
	}

	// A signed but incorrect Content-MD5 is rejected while reading the body.
	req, err = newTestRequest(http.MethodPut, getBucketCorsURL("", bucketName),
		int64(len(testCORSDoc)), bytes.NewReader([]byte(testCORSDoc)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Md5", getMD5HashBase64([]byte("different body")))
	if err = signRequestV4(req, creds.AccessKey, creds.SecretKey); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("<Code>BadDigest</Code>")) {
		t.Fatalf("PUT cors with bad Content-MD5: expected BadDigest, got %d: %s", rec.Code, rec.Body.String())
	}

	// Re-PUT the config so the store→GetCorsConfig→enforce seam below has
	// something to enforce (the earlier DELETE removed it).
	req, err = newTestSignedRequestV4(http.MethodPut, getBucketCorsURL("", bucketName),
		int64(len(testCORSDoc)), bytes.NewReader([]byte(testCORSDoc)), creds.AccessKey, creds.SecretKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT cors (re-put): expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// End-to-end enforcement: drive an OPTIONS preflight through the real
	// corsHandler wrapper (not applyBucketCors in isolation), exercising the
	// full store -> globalBucketMetadataSys.GetCorsConfig -> enforce seam.
	wrapped := corsHandler(apiRouter)

	preflightURL := getBucketCorsURL("", bucketName)
	preflightReq := httptest.NewRequest(http.MethodOptions, preflightURL, nil)
	preflightReq.Header.Set("Origin", "http://example.com")
	preflightReq.Header.Set("Access-Control-Request-Method", http.MethodGet)

	rec = httptest.NewRecorder()
	wrapped.ServeHTTP(rec, preflightReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("OPTIONS preflight via corsHandler: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://example.com" {
		t.Fatalf("OPTIONS preflight via corsHandler: expected Access-Control-Allow-Origin echoed, got %q", got)
	}
}
