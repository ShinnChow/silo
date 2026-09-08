// Copyright (c) 2015-2025 MinIO, Inc.
// Copyright (c) 2025-2026 PGSTY
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
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/minio/minio-go/v7/pkg/set"
	"github.com/minio/minio/internal/auth"
	"github.com/minio/minio/internal/config/dns"
	"github.com/minio/minio/internal/hash"
	xhttp "github.com/minio/minio/internal/http"
)

// federationRemoteCapture records the exact request headers the remote
// deployment receives on each forwarded request, so a test can assert what
// actually crossed the wire rather than what the destination ends up storing.
type federationRemoteCapture struct {
	mu      sync.Mutex
	headers []http.Header
}

func (c *federationRemoteCapture) record(h http.Header) {
	c.mu.Lock()
	c.headers = append(c.headers, h.Clone())
	c.mu.Unlock()
}

// reservedKeys returns every reserved-prefix header key seen across all
// forwarded requests.
func (c *federationRemoteCapture) reservedKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var keys []string
	for _, h := range c.headers {
		for k := range h {
			if stringsHasPrefixFold(k, ReservedMetadataPrefix) {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// setupCopyObjectFederation makes the current process play both federation
// roles for a whole-object CopyObject. The destination bucket really exists in
// the shared backend, but the proxy's own bucket lookup is told it lives on a
// remote deployment, so CopyObjectHandler takes the legacy etcd federation
// branch and forwards the write through getRemoteInstanceClient and minio-go
// into a second HTTP endpoint that serves the real PutObjectHandler. That
// endpoint records the inbound headers first so a caller can inspect the wire.
//
// GetBucketLocation is deliberately left unregistered on that endpoint: the
// remoteBucketObjectLayer reports remoteBucket as missing (so the proxy takes
// the federation branch), which would also fail a real GetBucketLocation the
// minio-go client probes for. Leaving it unregistered makes the probe fall back
// to the default region, exactly as TestAPIFederatedCopyObjectPartChecksum does.
func setupCopyObjectFederation(t *testing.T, objectAPI ObjectLayer, apiRouter http.Handler,
	instanceType, srcBucket string,
) (remoteBucket string, capture *federationRemoteCapture, cleanup func()) {
	t.Helper()
	remoteBucket = getRandomBucketName()
	if err := objectAPI.MakeBucket(t.Context(), remoteBucket, MakeBucketOptions{}); err != nil {
		t.Fatalf("%s: unable to create the remote bucket: %v", instanceType, err)
	}

	capture = &federationRemoteCapture{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.record(r.Header)
		apiRouter.ServeHTTP(w, r)
	}))
	host, port, _ := strings.Cut(remote.Listener.Addr().String(), ":")

	globalObjLayerMutex.Lock()
	previousLayer := globalObjectAPI
	globalObjectAPI = remoteBucketObjectLayer{ObjectLayer: previousLayer, remoteBucket: remoteBucket}
	globalObjLayerMutex.Unlock()
	previousDNS, previousFederation, previousIPs := globalDNSConfig, globalBucketFederation, globalDomainIPs
	globalDNSConfig = federationTestDNS{records: map[string][]dns.SrvRecord{
		srcBucket:    {{Host: host, Port: json.Number(port)}},
		remoteBucket: {{Host: host, Port: json.Number(port)}},
	}}
	// Every DNS record resolves to this process, so the bucket forwarding
	// middleware always serves locally and only the handler proxies.
	globalDomainIPs = set.CreateStringSet(remote.Listener.Addr().String())
	globalBucketFederation = true

	cleanup = func() {
		remote.Close()
		globalObjLayerMutex.Lock()
		globalObjectAPI = previousLayer
		globalObjLayerMutex.Unlock()
		globalDNSConfig, globalBucketFederation, globalDomainIPs = previousDNS, previousFederation, previousIPs
	}
	return remoteBucket, capture, cleanup
}

// federatedCopyRequest drives a whole-object CopyObject at the proxy that
// forwards across deployments, and returns the recorded response.
func federatedCopyRequest(t *testing.T, apiRouter http.Handler, credentials auth.Credentials,
	srcBucket, srcObject, dstBucket, dstObject string, headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	req, err := newTestSignedRequestV4(http.MethodPut, getCopyObjectURL("", dstBucket, dstObject),
		0, nil, credentials.AccessKey, credentials.SecretKey, headers)
	if err != nil {
		t.Fatalf("failed to build federated CopyObject request: %v", err)
	}
	req.Header.Set(xhttp.AmzCopySource, SlashSeparator+pathJoin(srcBucket, srcObject))
	rec := httptest.NewRecorder()
	apiRouter.ServeHTTP(rec, req)
	return rec
}

// TestAPIFederatedCopyObjectInlineSource drives the legacy etcd federation
// branch of CopyObjectHandler end to end for a source object stored inline.
// Such a source carries x-minio-internal-inline-data in its stored metadata,
// which the remote deployment rejects as a reserved-prefix header. Before the
// fix the forwarded write failed with 400 InvalidArgument; the copy must now
// succeed and forward no reserved-prefix metadata at all.
func TestAPIFederatedCopyObjectInlineSource(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testAPIFederatedCopyObjectInlineSource,
		endpoints:  []string{"CopyObject", "PutObject", "HeadObject", "GetObject"},
	})
}

func testAPIFederatedCopyObjectInlineSource(objectAPI ObjectLayer, instanceType, bucketName string,
	apiRouter http.Handler, credentials auth.Credentials, t *testing.T,
) {
	// A small object is stored inline, so its stored metadata carries
	// x-minio-internal-inline-data. A user metadata key rides along to prove
	// the fix strips only the reserved class, never ordinary metadata.
	data := []byte("federated inline copy 26b!")
	srcObject := "federation/inline-source"
	putCopyChecksumSource(t, apiRouter, credentials, bucketName, srcObject, data,
		map[string]string{"X-Amz-Meta-Origin": "inline-source"})

	remoteBucket, capture, cleanup := setupCopyObjectFederation(t, objectAPI, apiRouter, instanceType, bucketName)
	defer cleanup()

	dstObject := "federation/inline-destination"
	rec := federatedCopyRequest(t, apiRouter, credentials, bucketName, srcObject, remoteBucket, dstObject, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: federated CopyObject of an inline source failed: %d %s",
			instanceType, rec.Code, rec.Body.String())
	}

	// No reserved-prefix header may reach the remote deployment on any of the
	// forwarded requests.
	if leaked := capture.reservedKeys(); len(leaked) != 0 {
		t.Fatalf("%s: forwarded reserved metadata to the remote: %v", instanceType, leaked)
	}

	// The destination object must be readable and byte-identical, and it must
	// still carry the copied user metadata.
	gr, err := objectAPI.GetObjectNInfo(t.Context(), remoteBucket, dstObject, nil, nil, ObjectOptions{})
	if err != nil {
		t.Fatalf("%s: unable to read the federated copy destination: %v", instanceType, err)
	}
	got, err := io.ReadAll(gr)
	closeErr := gr.Close()
	if err != nil {
		t.Fatalf("%s: reading the federated copy destination failed: %v", instanceType, err)
	}
	if closeErr != nil {
		t.Fatalf("%s: closing the federated copy destination failed: %v", instanceType, closeErr)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("%s: federated copy destination body = %q, want %q", instanceType, got, data)
	}
	if origin, ok := gr.ObjInfo.UserDefined["X-Amz-Meta-Origin"]; !ok || origin != "inline-source" {
		t.Fatalf("%s: destination lost copied user metadata: %v", instanceType, gr.ObjInfo.UserDefined)
	}
}

// TestAPIFederatedCopyObjectRequestedChecksum drives the legacy etcd federation
// branch of CopyObjectHandler and verifies that a server-side checksum is both
// returned and persisted, matching the local CopyObject path. Before the fix
// the federated copy forwarded the write without asking for a checksum and
// discarded whatever the remote returned, so the response carried an empty
// checksum even when the client requested one (#99).
//
// The no-algorithm case is included deliberately: a checksum-less source gains
// the S3 default CRC-64NVME full-object checksum on the local path, so the
// federated path must return the same. "No requested algorithm" does not mean
// "no checksum".
func TestAPIFederatedCopyObjectRequestedChecksum(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testAPIFederatedCopyObjectRequestedChecksum,
		endpoints:  []string{"CopyObject", "PutObject", "HeadObject", "GetObject"},
	})
}

func testAPIFederatedCopyObjectRequestedChecksum(objectAPI ObjectLayer, instanceType, bucketName string,
	apiRouter http.Handler, credentials auth.Credentials, t *testing.T,
) {
	data := []byte("federated copy checksum body")
	srcObject := "federation/checksum-source"
	putCopyChecksumSource(t, apiRouter, credentials, bucketName, srcObject, data, nil)

	remoteBucket, _, cleanup := setupCopyObjectFederation(t, objectAPI, apiRouter, instanceType, bucketName)
	defer cleanup()

	cases := []struct {
		name     string
		typ      hash.ChecksumType
		explicit bool
	}{
		{name: "CRC32", typ: hash.ChecksumCRC32, explicit: true},
		{name: "CRC32C", typ: hash.ChecksumCRC32C, explicit: true},
		{name: "SHA256", typ: hash.ChecksumSHA256, explicit: true},
		{name: "CRC64NVME", typ: hash.ChecksumCRC64NVME, explicit: true},
		// Default: no requested algorithm still yields the S3 CRC-64NVME.
		{name: "default", typ: hash.ChecksumCRC64NVME},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var headers map[string]string
			if tc.explicit {
				headers = map[string]string{xhttp.AmzChecksumAlgo: tc.typ.String()}
			}
			dstObject := "federation/checksum-destination-" + tc.name
			rec := federatedCopyRequest(t, apiRouter, credentials, bucketName, srcObject, remoteBucket, dstObject, headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: federated CopyObject failed: %d %s", instanceType, rec.Code, rec.Body.String())
			}
			// The CopyObjectResult must carry the checksum of the copied bytes.
			assertCopyChecksumResponse(t, rec, tc.typ, data)
			// The remote must have persisted that same checksum.
			assertCopyChecksum(t, objectAPI, remoteBucket, dstObject, tc.typ, data, false, nil)
		})
	}
}

// TestAPIFederatedCopyObjectChecksumIsBoundToWrite guards the checksum
// representation: a federated copy that requests one algorithm must return only
// that algorithm, and a copy of a checksum-less source without a requested
// algorithm must never fabricate one other than the S3 default.
func TestAPIFederatedCopyObjectChecksumIsBoundToWrite(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testAPIFederatedCopyObjectChecksumIsBoundToWrite,
		endpoints:  []string{"CopyObject", "PutObject", "HeadObject", "GetObject"},
	})
}

func testAPIFederatedCopyObjectChecksumIsBoundToWrite(objectAPI ObjectLayer, instanceType, bucketName string,
	apiRouter http.Handler, credentials auth.Credentials, t *testing.T,
) {
	data := []byte("federated copy single checksum body")
	srcObject := "federation/single-checksum-source"
	putCopyChecksumSource(t, apiRouter, credentials, bucketName, srcObject, data, nil)

	remoteBucket, _, cleanup := setupCopyObjectFederation(t, objectAPI, apiRouter, instanceType, bucketName)
	defer cleanup()

	dstObject := "federation/single-checksum-destination"
	rec := federatedCopyRequest(t, apiRouter, credentials, bucketName, srcObject, remoteBucket, dstObject,
		map[string]string{xhttp.AmzChecksumAlgo: hash.ChecksumCRC32.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: federated CopyObject failed: %d %s", instanceType, rec.Code, rec.Body.String())
	}

	var response CopyObjectResponse
	if err := xml.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("%s: unable to decode CopyObjectResult: %v", instanceType, err)
	}
	if response.ChecksumCRC32 == "" {
		t.Fatalf("%s: requested CRC32 checksum missing from response: %s", instanceType, rec.Body.String())
	}
	// Only the requested algorithm may be present.
	if response.ChecksumCRC32C != "" || response.ChecksumSHA1 != "" ||
		response.ChecksumSHA256 != "" || response.ChecksumCRC64NVME != "" {
		t.Fatalf("%s: response carried checksums beyond the requested CRC32: %s", instanceType, rec.Body.String())
	}
}
