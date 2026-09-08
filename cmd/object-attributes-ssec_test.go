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
	"crypto/md5"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/minio/minio/internal/auth"
	xhttp "github.com/minio/minio/internal/http"
)

func TestAPIGetObjectAttributesAuthenticatesSSECKey(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testAPIGetObjectAttributesAuthenticatesSSECKey,
	})
}

func testAPIGetObjectAttributesAuthenticatesSSECKey(_ ObjectLayer, instanceType, bucketName string,
	apiRouter http.Handler, credentials auth.Credentials, t *testing.T,
) {
	previousTLS := globalIsTLS
	globalIsTLS = true
	defer func() { globalIsTLS = previousTLS }()

	key := bytes.Repeat([]byte{0x11}, 32)
	keyMD5 := md5.Sum(key)
	wrongKey := bytes.Repeat([]byte{0x22}, 32)
	wrongMD5 := md5.Sum(wrongKey)
	correctHeaders := map[string]string{
		xhttp.AmzServerSideEncryptionCustomerAlgorithm: xhttp.AmzEncryptionAES,
		xhttp.AmzServerSideEncryptionCustomerKey:       base64.StdEncoding.EncodeToString(key),
		xhttp.AmzServerSideEncryptionCustomerKeyMD5:    base64.StdEncoding.EncodeToString(keyMD5[:]),
	}
	wrongHeaders := map[string]string{
		xhttp.AmzServerSideEncryptionCustomerAlgorithm: xhttp.AmzEncryptionAES,
		xhttp.AmzServerSideEncryptionCustomerKey:       base64.StdEncoding.EncodeToString(wrongKey),
		xhttp.AmzServerSideEncryptionCustomerKeyMD5:    base64.StdEncoding.EncodeToString(wrongMD5[:]),
	}

	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "zero", data: nil},
		{name: "nonzero", data: []byte("secret")},
	} {
		object := "attributes/ssec-" + test.name
		putCopyChecksumSource(t, apiRouter, credentials, bucketName, object, test.data, correctHeaders)
		if rec := objectAttributesSSECRequest(t, apiRouter, credentials, bucketName, object, correctHeaders); rec.Code != http.StatusOK {
			t.Fatalf("%s/%s: correct key returned %d: %s", instanceType, test.name, rec.Code, rec.Body.String())
		}
		if rec := objectAttributesSSECRequest(t, apiRouter, credentials, bucketName, object, wrongHeaders); rec.Code != http.StatusForbidden {
			t.Fatalf("%s/%s: wrong key returned %d, want %d: %s", instanceType, test.name, rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if rec := objectAttributesSSECRequest(t, apiRouter, credentials, bucketName, object, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s/%s: missing key returned %d, want %d: %s", instanceType, test.name, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		if rec := objectAttributesSSECRequest(t, apiRouter, credentials, bucketName, object, map[string]string{
			xhttp.MinIOSourceReplicationRequest: "true",
		}); rec.Code != http.StatusOK {
			t.Fatalf("%s/%s: replication request returned %d: %s", instanceType, test.name, rec.Code, rec.Body.String())
		}
	}
}

func objectAttributesSSECRequest(t *testing.T, apiRouter http.Handler, credentials auth.Credentials,
	bucket, object string, encryptionHeaders map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	headers := map[string]string{xhttp.AmzObjectAttributes: "ObjectSize,ETag,ObjectParts,Checksum"}
	for key, value := range encryptionHeaders {
		headers[key] = value
	}
	req, err := newTestSignedRequestV4(http.MethodGet, getGetObjectURL("", bucket, object)+"?attributes",
		0, nil, credentials.AccessKey, credentials.SecretKey, headers)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	return rec
}
