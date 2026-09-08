// Copyright (c) 2015-2026 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/minio/minio/internal/auth"
	xhttp "github.com/minio/minio/internal/http"
)

func TestAPIRejectsUnsupportedChecksumHeaders(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testAPIRejectsUnsupportedChecksumHeaders,
		endpoints:  []string{"CopyObject", "NewMultipart", "PutObject", "PutObjectPart"},
	})
}

func testAPIRejectsUnsupportedChecksumHeaders(obj ObjectLayer, instanceType, bucketName string,
	apiRouter http.Handler, credentials auth.Credentials, t *testing.T,
) {
	data := []byte("unsupported-checksum")
	unsupportedValue := base64.StdEncoding.EncodeToString(make([]byte, 64))

	put := func(object string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req, err := newTestSignedRequestV4(http.MethodPut, getPutObjectURL("", bucketName, object),
			int64(len(data)), bytes.NewReader(data), credentials.AccessKey, credentials.SecretKey, headers)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		apiRouter.ServeHTTP(rec, req)
		return rec
	}
	assertRejected := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "<Code>InvalidArgument</Code>") {
			t.Fatalf("%s: %s returned %d, want InvalidArgument: %s", instanceType, name, rec.Code, rec.Body.String())
		}
	}

	for _, algorithm := range []string{"md5", "sha512", "xxhash64", "xxhash3", "xxhash128", "future"} {
		object := "checksums/unsupported-" + algorithm
		assertRejected(algorithm, put(object, map[string]string{
			"x-amz-sdk-checksum-algorithm": "SHA512",
			"x-amz-checksum-" + algorithm:  unsupportedValue,
		}))
		if _, err := obj.GetObjectInfo(t.Context(), bucketName, object, ObjectOptions{}); !isErrObjectNotFound(err) {
			t.Fatalf("%s: rejected %s checksum stored an object: %v", instanceType, algorithm, err)
		}
	}

	assertRejected("unsupported trailer", put("checksums/unsupported-trailer", map[string]string{
		xhttp.AmzTrailer: "x-amz-checksum-sha512",
	}))

	newMultipart := func(name string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req, err := newTestSignedRequestV4(http.MethodPost, getNewMultipartURL("", bucketName, name),
			0, nil, credentials.AccessKey, credentials.SecretKey, headers)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		apiRouter.ServeHTTP(rec, req)
		return rec
	}
	assertRejected("NewMultipartUpload value header", newMultipart("checksums/mp-value", map[string]string{
		"x-amz-checksum-sha512": unsupportedValue,
	}))
	assertRejected("NewMultipartUpload trailer", newMultipart("checksums/mp-trailer", map[string]string{
		xhttp.AmzTrailer: "x-amz-checksum-sha512",
	}))

	rec := newMultipart("checksums/mp-part", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: NewMultipartUpload setup returned %d: %s", instanceType, rec.Code, rec.Body.String())
	}
	var initiated InitiateMultipartUploadResponse
	if err := xml.Unmarshal(rec.Body.Bytes(), &initiated); err != nil {
		t.Fatal(err)
	}
	req, err := newTestSignedRequestV4(http.MethodPut,
		getPutObjectPartURL("", bucketName, "checksums/mp-part", initiated.UploadID, "1"),
		int64(len(data)), bytes.NewReader(data), credentials.AccessKey, credentials.SecretKey,
		map[string]string{"x-amz-checksum-sha512": unsupportedValue})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	assertRejected("UploadPart", rec)
	parts, err := obj.ListObjectParts(t.Context(), bucketName, "checksums/mp-part", initiated.UploadID, 0, 1000, ObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts.Parts) != 0 {
		t.Fatalf("%s: rejected UploadPart stored %d parts", instanceType, len(parts.Parts))
	}
	if err := obj.AbortMultipartUpload(t.Context(), bucketName, "checksums/mp-part", initiated.UploadID, ObjectOptions{}); err != nil {
		t.Fatal(err)
	}

	source := "checksums/source"
	putCopyChecksumSource(t, apiRouter, credentials, bucketName, source, data, nil)
	rec = copyChecksumRequest(t, apiRouter, credentials, bucketName, source, "checksums/copy", map[string]string{
		"x-amz-checksum-sha512": unsupportedValue,
	})
	assertRejected("CopyObject", rec)
	if _, err := obj.GetObjectInfo(t.Context(), bucketName, "checksums/copy", ObjectOptions{}); !isErrObjectNotFound(err) {
		t.Fatalf("%s: rejected CopyObject stored a destination: %v", instanceType, err)
	}
}
