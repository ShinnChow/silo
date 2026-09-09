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

import (
	"net/http"
	"strings"
	"testing"

	"github.com/minio/minio/internal/auth"
	objectlock "github.com/minio/minio/internal/bucket/object/lock"
	xhttp "github.com/minio/minio/internal/http"
)

// enableBucketObjectLock puts a lock-enabled configuration on an existing
// bucket so checkPutObjectLockAllowed accepts a legal-hold header instead of
// rejecting the request with ErrInvalidBucketObjectLockConfiguration.
func enableBucketObjectLock(t *testing.T, bucket string) {
	t.Helper()
	meta, err := globalBucketMetadataSys.Get(bucket)
	if err != nil {
		t.Fatalf("unable to read bucket metadata for %s: %v", bucket, err)
	}
	updated := meta
	updated.ObjectLockConfigXML = enabledBucketObjectLockConfig
	updated.VersioningConfigXML = enabledBucketVersioningConfig
	// The XML alone is not enough: BucketMetadata keeps a parsed copy that the
	// lookups actually read, and it is only populated by parseAllConfigs.
	if err := updated.parseAllConfigs(t.Context(), newObjectLayerFn()); err != nil {
		t.Fatalf("unable to parse bucket metadata for %s: %v", bucket, err)
	}
	globalBucketMetadataSys.Set(bucket, updated)
}

// legalHoldHeaders returns the forwarded legal-hold headers the remote saw,
// separated into the real Object Lock header and the user-metadata spelling
// minio-go produces for an unrecognised UserMetadata key.
func (c *federationRemoteCapture) legalHoldHeaders() (typed, asMetadata []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	metaKey := "X-Amz-Meta-" + xhttp.AmzObjectLockLegalHold
	for _, h := range c.headers {
		for k, v := range h {
			switch {
			case strings.EqualFold(k, xhttp.AmzObjectLockLegalHold):
				typed = append(typed, strings.Join(v, ","))
			case strings.EqualFold(k, metaKey):
				asMetadata = append(asMetadata, strings.Join(v, ","))
			}
		}
	}
	return typed, asMetadata
}

// TestAPIFederatedCopyObjectLegalHold drives the legacy etcd federation branch
// of CopyObjectHandler with an explicit legal hold on the copy.
//
// Before the fix the resolved hold was forwarded inside
// PutObjectOptions.UserMetadata. minio-go's Header() prefixes every
// UserMetadata key it does not recognise with "x-amz-meta-", and
// x-amz-object-lock-legal-hold is in neither supportedHeaders nor isAmzHeader,
// so the hold reached the remote as X-Amz-Meta-X-Amz-Object-Lock-Legal-Hold.
// The destination stored no hold and the copy still answered 200 -- a silent
// loss of a WORM control (#166). Retention requested on the same copy survived,
// which is what made it easy to miss.
func TestAPIFederatedCopyObjectLegalHold(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testAPIFederatedCopyObjectLegalHold,
		endpoints:  []string{"CopyObject", "PutObject", "HeadObject", "GetObject"},
	})
}

func testAPIFederatedCopyObjectLegalHold(objectAPI ObjectLayer, instanceType, bucketName string,
	apiRouter http.Handler, credentials auth.Credentials, t *testing.T,
) {
	data := []byte("federated copy with a legal hold")
	srcObject := "federation/legal-hold-source"
	putCopyChecksumSource(t, apiRouter, credentials, bucketName, srcObject, data, nil)

	remoteBucket, capture, cleanup := setupCopyObjectFederation(t, objectAPI, apiRouter, instanceType, bucketName)
	defer cleanup()

	// Both roles read bucket metadata from the shared backend in this fixture,
	// so one configuration covers the proxy's own check and the remote's.
	enableBucketObjectLock(t, bucketName)
	enableBucketObjectLock(t, remoteBucket)

	dstObject := "federation/legal-hold-destination"
	rec := federatedCopyRequest(t, apiRouter, credentials, bucketName, srcObject, remoteBucket, dstObject,
		map[string]string{xhttp.AmzObjectLockLegalHold: string(objectlock.LegalHoldOn)})
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: federated CopyObject with a legal hold failed: %d %s",
			instanceType, rec.Code, rec.Body.String())
	}

	// The wire is the point: the hold must arrive as the Object Lock header,
	// never as user metadata. A 200 with the metadata spelling is exactly the
	// silent loss this test exists for.
	typed, asMetadata := capture.legalHoldHeaders()
	if len(asMetadata) != 0 {
		t.Fatalf("%s: legal hold forwarded as user metadata %v; the destination stores no hold",
			instanceType, asMetadata)
	}
	if len(typed) == 0 {
		t.Fatalf("%s: no %s header reached the remote deployment", instanceType, xhttp.AmzObjectLockLegalHold)
	}
	for _, got := range typed {
		if !strings.EqualFold(got, string(objectlock.LegalHoldOn)) {
			t.Fatalf("%s: forwarded legal hold = %q, want %q", instanceType, got, objectlock.LegalHoldOn)
		}
	}

	// And it must actually be stored on the destination version.
	oi, err := objectAPI.GetObjectInfo(t.Context(), remoteBucket, dstObject, ObjectOptions{})
	if err != nil {
		t.Fatalf("%s: unable to stat the federated copy destination: %v", instanceType, err)
	}
	if hold := objectlock.GetObjectLegalHoldMeta(oi.UserDefined); hold.Status != objectlock.LegalHoldOn {
		t.Fatalf("%s: destination legal hold = %q, want %q (metadata: %v)",
			instanceType, hold.Status, objectlock.LegalHoldOn, oi.UserDefined)
	}
}
