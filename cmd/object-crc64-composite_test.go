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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/minio/minio/internal/auth"
	"github.com/minio/minio/internal/hash"
	xhttp "github.com/minio/minio/internal/http"
)

func TestAPIPutObjectRejectsCRC64Composite(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testAPIPutObjectRejectsCRC64Composite,
		endpoints:  []string{"PutObject"},
	})
}

func testAPIPutObjectRejectsCRC64Composite(obj ObjectLayer, instanceType, bucketName string,
	apiRouter http.Handler, credentials auth.Credentials, t *testing.T,
) {
	data := []byte("crc64-composite")
	object := "checksums/crc64-composite"
	req, err := newTestSignedRequestV4(http.MethodPut, getPutObjectURL("", bucketName, object),
		int64(len(data)), bytes.NewReader(data), credentials.AccessKey, credentials.SecretKey, map[string]string{
			xhttp.AmzChecksumCRC64NVME: mustChecksum(t, hash.ChecksumCRC64NVME, data),
			xhttp.AmzChecksumType:      xhttp.AmzChecksumTypeComposite,
		})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || apiErrorCode(t, rec) != "InvalidArgument" {
		t.Fatalf("%s: CRC64NVME/COMPOSITE PutObject returned %d %s", instanceType, rec.Code, rec.Body.String())
	}
	if _, err := obj.GetObjectInfo(t.Context(), bucketName, object, ObjectOptions{}); !isErrObjectNotFound(err) {
		t.Fatalf("%s: rejected PutObject stored an object: %v", instanceType, err)
	}

	trailerObject := "checksums/crc64-composite-trailer"
	req, err = newTestSignedRequestV4(http.MethodPut, getPutObjectURL("", bucketName, trailerObject),
		int64(len(data)), bytes.NewReader(data), credentials.AccessKey, credentials.SecretKey, map[string]string{
			xhttp.AmzTrailer:      xhttp.AmzChecksumCRC64NVME,
			xhttp.AmzChecksumType: xhttp.AmzChecksumTypeComposite,
		})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || apiErrorCode(t, rec) != "InvalidArgument" {
		t.Fatalf("%s: trailing CRC64NVME/COMPOSITE PutObject returned %d %s", instanceType, rec.Code, rec.Body.String())
	}
	if _, err := obj.GetObjectInfo(t.Context(), bucketName, trailerObject, ObjectOptions{}); !isErrObjectNotFound(err) {
		t.Fatalf("%s: rejected trailing PutObject stored an object: %v", instanceType, err)
	}
}
