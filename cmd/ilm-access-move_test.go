// Copyright 2026 PGSTY contributors.
// SPDX-License-Identifier: AGPL-3.0-or-later

package cmd

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio/internal/dsync"
	"github.com/minio/minio/internal/hash"
	xhttp "github.com/minio/minio/internal/http"
)

func accessMovePools(t *testing.T) (*erasureServerPools, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	dirs, err := getRandomDisks(32)
	if err != nil {
		t.Fatal(err)
	}
	endpoints := mustGetPoolEndpoints(0, dirs[:16]...)
	endpoints = append(endpoints, mustGetPoolEndpoints(1, dirs[16:]...)...)
	obj, _, err := initObjectLayer(ctx, endpoints)
	if err != nil {
		cancel()
		removeRoots(dirs)
		t.Fatal(err)
	}
	z := obj.(*erasureServerPools)
	previous := newObjectLayerFn()
	setObjectLayer(z)
	t.Cleanup(func() { cancel(); z.Shutdown(context.Background()); removeRoots(dirs); setObjectLayer(previous) })
	bucket := "access-move-test"
	if err := z.MakeBucket(ctx, bucket, MakeBucketOptions{VersioningEnabled: true}); err != nil {
		t.Fatal(err)
	}
	return z, bucket
}

func putAccessMoveVersion(t *testing.T, z *erasureServerPools, bucket, object string, pool int, body string, moved bool) ObjectInfo {
	t.Helper()
	metadata := map[string]string{"test-value": body}
	if moved {
		metadata[accessTierMetadataKey] = accessTierStamp(pool, time.Now().UnixNano())
	}
	cs := hash.NewChecksumFromData(hash.ChecksumCRC32C, []byte(body))
	oi, err := z.serverPools[pool].PutObject(t.Context(), bucket, object,
		mustGetPutObjReader(t, bytes.NewBufferString(body), int64(len(body)), "", ""),
		ObjectOptions{Versioned: true, UserDefined: metadata, WantChecksum: cs})
	if err != nil {
		t.Fatal(err)
	}
	return oi
}

func assertAccessMoveVersion(t *testing.T, z *erasureServerPools, bucket, object string, pool int, want ObjectInfo, body string) {
	t.Helper()
	gr, err := z.serverPools[pool].GetObjectNInfo(t.Context(), bucket, object, nil, http.Header{}, ObjectOptions{VersionID: want.VersionID})
	if err != nil {
		t.Errorf("pool %d version %s: %v", pool, want.VersionID, err)
		return
	}
	got, err := io.ReadAll(gr)
	gr.Close()
	if err != nil || string(got) != body {
		t.Errorf("pool %d payload = %q, err = %v, want %q", pool, got, err, body)
	}
	if gr.ObjInfo.ETag != want.ETag || !gr.ObjInfo.ModTime.Equal(want.ModTime) {
		t.Error("move changed ETag or modification time")
	}
	if len(want.Checksum) == 0 {
		t.Fatal("fixture has no checksum")
	}
	if !bytes.Equal(gr.ObjInfo.Checksum, want.Checksum) {
		t.Error("move changed or dropped the stored checksum")
	}
	if gr.ObjInfo.UserDefined["test-value"] != body {
		t.Error("move changed user metadata")
	}
}

func TestAccessMoveVersionStack(t *testing.T) {
	z, bucket := accessMovePools(t)
	const object = "versions"
	old := putAccessMoveVersion(t, z, bucket, object, 1, "old", false)
	dm, err := z.serverPools[1].DeleteObject(t.Context(), bucket, object, ObjectOptions{Versioned: true})
	if err != nil {
		t.Fatal(err)
	}
	latest := putAccessMoveVersion(t, z, bucket, object, 1, "new", false)
	for _, pair := range [][2]int{{1, 0}, {0, 1}} {
		if _, err := moveObjectPool(t.Context(), z, bucket, object, pair[0], pair[1], nil); err != nil {
			t.Fatal(err)
		}
		assertAccessMoveVersion(t, z, bucket, object, pair[1], old, "old")
		assertAccessMoveVersion(t, z, bucket, object, pair[1], latest, "new")
		versions, err := accessObjectVersions(t.Context(), z, pair[1], bucket, object)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, version := range versions {
			if version.VersionID == dm.VersionID && version.Deleted {
				found = true
			}
		}
		if !found || len(versions) != 3 {
			t.Errorf("move lost a version or delete marker: %+v", versions)
		}
		if _, err := z.serverPools[pair[0]].GetObjectInfo(t.Context(), bucket, object, ObjectOptions{}); !isErrObjectNotFound(err) {
			t.Errorf("source remains after completed move: %v", err)
		}
	}
}

func TestAccessMovePreservesNestedObject(t *testing.T) {
	z, bucket := accessMovePools(t)
	parent := putAccessMoveVersion(t, z, bucket, "parent", 1, "parent", false)
	child := putAccessMoveVersion(t, z, bucket, "parent/child", 1, "child", false)
	if _, err := moveObjectPool(t.Context(), z, bucket, "parent", 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	assertAccessMoveVersion(t, z, bucket, "parent", 0, parent, "parent")
	assertAccessMoveVersion(t, z, bucket, "parent/child", 1, child, "child")
}

func TestAccessMoveNullVersion(t *testing.T) {
	z, bucket := accessMovePools(t)
	const object, body = "null-version", "unversioned"
	// A null version can remain in a bucket after versioning is enabled.
	oi, err := z.serverPools[1].PutObject(t.Context(), bucket, object,
		mustGetPutObjReader(t, bytes.NewBufferString(body), int64(len(body)), "", ""), ObjectOptions{
			UserDefined:  map[string]string{"test-value": body},
			WantChecksum: hash.NewChecksumFromData(hash.ChecksumCRC32C, []byte(body)),
		})
	if err != nil || oi.VersionID != "" {
		t.Fatalf("null version fixture failed: %v %q", err, oi.VersionID)
	}
	if _, err := moveObjectPool(t.Context(), z, bucket, object, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	assertAccessMoveVersion(t, z, bucket, object, 0, oi, body)
	if _, err := z.serverPools[1].GetObjectInfo(t.Context(), bucket, object, ObjectOptions{}); !isErrObjectNotFound(err) {
		t.Fatalf("null version source was not removed: %v", err)
	}
}

// Source removal can partially commit before a process or disk fails. The
// destination can therefore hold the only remaining copy of an older version.
func TestAccessMoveRetryPreservesDestinationVersions(t *testing.T) {
	z, bucket := accessMovePools(t)
	const object = "retry"
	onlyAtDestination := putAccessMoveVersion(t, z, bucket, object, 0, "unique-old", true)
	source := putAccessMoveVersion(t, z, bucket, object, 1, "source-new", false)
	if _, err := moveObjectPool(t.Context(), z, bucket, object, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	assertAccessMoveVersion(t, z, bucket, object, 0, onlyAtDestination, "unique-old")
	assertAccessMoveVersion(t, z, bucket, object, 0, source, "source-new")
}

type accessMoveFaultDisk struct {
	StorageAPI
	failVersion string
}

func (d accessMoveFaultDisk) RenameData(ctx context.Context, srcVolume, srcPath string, fi FileInfo, dstVolume, dstPath string, opts RenameOptions) (RenameDataResp, error) {
	if fi.VersionID == d.failVersion {
		return RenameDataResp{}, errDiskFull
	}
	return d.StorageAPI.RenameData(ctx, srcVolume, srcPath, fi, dstVolume, dstPath, opts)
}

func TestAccessMoveWriteFailureCanResume(t *testing.T) {
	z, bucket := accessMovePools(t)
	const object = "write-failure"
	old := putAccessMoveVersion(t, z, bucket, object, 1, "old", false)
	latest := putAccessMoveVersion(t, z, bucket, object, 1, "new", false)
	existing := putAccessMoveVersion(t, z, bucket, object, 0, "unique-destination", true)
	set := z.serverPools[0].getHashedSet(object)
	getDisks := set.getDisks
	disks := getDisks()
	faulty := make([]StorageAPI, len(disks))
	for i, disk := range disks {
		faulty[i] = accessMoveFaultDisk{StorageAPI: disk, failVersion: latest.VersionID}
	}
	set.getDisks = func() []StorageAPI { return faulty }
	_, err := moveObjectPool(t.Context(), z, bucket, object, 1, 0, nil)
	set.getDisks = getDisks
	if err == nil {
		t.Fatal("injected destination write failure was ignored")
	}
	assertAccessMoveVersion(t, z, bucket, object, 1, old, "old")
	assertAccessMoveVersion(t, z, bucket, object, 1, latest, "new")
	assertAccessMoveVersion(t, z, bucket, object, 0, existing, "unique-destination")
	if _, err := moveObjectPool(t.Context(), z, bucket, object, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	assertAccessMoveVersion(t, z, bucket, object, 0, old, "old")
	assertAccessMoveVersion(t, z, bucket, object, 0, latest, "new")
	assertAccessMoveVersion(t, z, bucket, object, 0, existing, "unique-destination")
}

func TestAccessMoveExcludesSourceWriter(t *testing.T) {
	z, bucket := accessMovePools(t)
	const object = "concurrent"
	old := putAccessMoveVersion(t, z, bucket, object, 1, "old", false)
	_, err := moveObjectPool(t.Context(), z, bucket, object, 1, 0, func(ObjectInfo, uint64) error {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		_, err := z.serverPools[1].PutObject(ctx, bucket, object,
			mustGetPutObjReader(t, bytes.NewBufferString("racing-write"), 12, "", ""), ObjectOptions{Versioned: true})
		if err == nil {
			t.Error("source writer committed while move was in progress")
		}
		if ctx.Err() == nil {
			return errors.New("writer did not wait for the move lock")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAccessMoveVersion(t, z, bucket, object, 0, old, "old")
}

func TestAccessMoveResumesPartialCopy(t *testing.T) {
	z, bucket := accessMovePools(t)
	const object = "partial-copy"
	old := putAccessMoveVersion(t, z, bucket, object, 1, "old", false)
	latest := putAccessMoveVersion(t, z, bucket, object, 1, "new", false)
	gr, err := z.serverPools[1].GetObjectNInfo(t.Context(), bucket, object, nil, http.Header{}, ObjectOptions{VersionID: old.VersionID, NoDecryption: true})
	if err != nil {
		t.Fatal(err)
	}
	// Model a process stopping after the first copy commits, before cleanup.
	if err := moveAccessTierVersion(t.Context(), z, 1, 0, bucket, gr, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := moveObjectPool(t.Context(), z, bucket, object, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	assertAccessMoveVersion(t, z, bucket, object, 0, old, "old")
	assertAccessMoveVersion(t, z, bucket, object, 0, latest, "new")
}

func TestAccessMoveRefusesConflictingVersion(t *testing.T) {
	z, bucket := accessMovePools(t)
	const object = "conflicting-version"
	source := putAccessMoveVersion(t, z, bucket, object, 1, "source", false)
	destination, err := z.serverPools[0].PutObject(t.Context(), bucket, object,
		mustGetPutObjReader(t, bytes.NewBufferString("target"), 6, "", ""), ObjectOptions{
			VersionID: source.VersionID, MTime: source.ModTime,
			UserDefined:  map[string]string{"test-value": "target"},
			WantChecksum: hash.NewChecksumFromData(hash.ChecksumCRC32C, []byte("target")),
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := moveObjectPool(t.Context(), z, bucket, object, 1, 0, nil); !errors.Is(err, errAccessTierNotEligible) {
		t.Fatalf("conflicting version should be left intact: %v", err)
	}
	assertAccessMoveVersion(t, z, bucket, object, 1, source, "source")
	assertAccessMoveVersion(t, z, bucket, object, 0, destination, "target")
}

type accessMoveDeleteFaultDisk struct {
	StorageAPI
	bucket, object, version string
}

func (d accessMoveDeleteFaultDisk) DeleteVersion(ctx context.Context, volume, path string, fi FileInfo, forceDelMarker bool, opts DeleteOptions) error {
	if volume == d.bucket && path == d.object && fi.VersionID == d.version {
		return errDiskFull
	}
	return d.StorageAPI.DeleteVersion(ctx, volume, path, fi, forceDelMarker, opts)
}

func TestAccessMoveSourceDeleteFailureCanResume(t *testing.T) {
	z, bucket := accessMovePools(t)
	const object = "source-delete-failure"
	old := putAccessMoveVersion(t, z, bucket, object, 1, "old", false)
	latest := putAccessMoveVersion(t, z, bucket, object, 1, "new", false)
	set := z.serverPools[1].getHashedSet(object)
	getDisks := set.getDisks
	faulty := append([]StorageAPI(nil), getDisks()...)
	// The old version is purged. One disk then completes deletion of the
	// latest version; the remaining disks reject it.
	for i := 1; i < len(faulty); i++ {
		faulty[i] = accessMoveDeleteFaultDisk{StorageAPI: faulty[i], bucket: bucket, object: object, version: latest.VersionID}
	}
	set.getDisks = func() []StorageAPI { return faulty }
	_, err := moveObjectPool(t.Context(), z, bucket, object, 1, 0, nil)
	set.getDisks = getDisks
	if err == nil {
		t.Fatal("injected partial source purge was ignored")
	}
	assertAccessMoveVersion(t, z, bucket, object, 0, old, "old")
	assertAccessMoveVersion(t, z, bucket, object, 0, latest, "new")
	if _, err := moveObjectPool(t.Context(), z, bucket, object, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	assertAccessMoveVersion(t, z, bucket, object, 0, old, "old")
	assertAccessMoveVersion(t, z, bucket, object, 0, latest, "new")
}

// Exercise the public multipart API and compare whole-object and part reads
// across both directions of a real pool move, including raw SSE-C ciphertext.
func TestAccessMoveMultipartChecksums(t *testing.T) {
	z, _ := accessMovePools(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	bucket, router, err := initAPIHandlerTest(ctx, z, nil, MakeBucketOptions{VersioningEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	previousTLS := globalIsTLS
	globalIsTLS = true
	defer func() { globalIsTLS = previousTLS }()
	partData, full := multipartChecksumTestData()
	for _, tc := range []struct {
		typ  hash.ChecksumType
		ssec bool
	}{{hash.ChecksumCRC32, false}, {hash.ChecksumCRC32C, false}, {hash.ChecksumCRC64NVME, false}, {hash.ChecksumCRC32C, true}} {
		t.Run(fmt.Sprintf("%s/ssec=%t", tc.typ, tc.ssec), func(t *testing.T) {
			object := getRandomObjectName()
			headers := map[string]string{}
			if tc.ssec {
				key := bytes.Repeat([]byte{0x42}, 32)
				keyMD5 := md5.Sum(key)
				headers[xhttp.AmzServerSideEncryptionCustomerAlgorithm] = xhttp.AmzEncryptionAES
				headers[xhttp.AmzServerSideEncryptionCustomerKey] = base64.StdEncoding.EncodeToString(key)
				headers[xhttp.AmzServerSideEncryptionCustomerKeyMD5] = base64.StdEncoding.EncodeToString(keyMD5[:])
			}
			do := func(method, url string, body []byte, extra map[string]string) *httptest.ResponseRecorder {
				t.Helper()
				h := maps.Clone(headers)
				maps.Copy(h, extra)
				req, err := newTestSignedRequestV4(method, url, int64(len(body)), bytes.NewReader(body), globalActiveCred.AccessKey, globalActiveCred.SecretKey, h)
				if err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK && rec.Code != http.StatusPartialContent {
					t.Fatalf("%s %s: %d %s", method, url, rec.Code, rec.Body.String())
				}
				return rec
			}
			init := do(http.MethodPost, getNewMultipartURL("", bucket, object), nil, map[string]string{
				xhttp.AmzChecksumAlgo: tc.typ.String(), xhttp.AmzChecksumType: xhttp.AmzChecksumTypeFullObject,
			})
			var upload InitiateMultipartUploadResponse
			if err := xml.Unmarshal(init.Body.Bytes(), &upload); err != nil {
				t.Fatal(err)
			}
			parts := make([]CompletePart, len(partData))
			for i, data := range partData {
				rec := do(http.MethodPut, getPutObjectPartURL("", bucket, object, upload.UploadID, fmt.Sprint(i+1)), data,
					map[string]string{tc.typ.Key(): mustChecksum(t, tc.typ, data)})
				parts[i] = CompletePart{PartNumber: i + 1, ETag: canonicalizeETag(rec.Header()[xhttp.ETag][0])}
			}
			body, err := xml.Marshal(CompleteMultipartUpload{Parts: parts})
			if err != nil {
				t.Fatal(err)
			}
			do(http.MethodPost, getCompleteMultipartUploadURL("", bucket, object, upload.UploadID), body,
				map[string]string{tc.typ.Key(): mustChecksum(t, tc.typ, full), xhttp.AmzChecksumType: xhttp.AmzChecksumTypeFullObject})
			source, err := z.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{})
			if err != nil || len(source.Checksum) == 0 {
				t.Fatalf("source checksum missing: %v", err)
			}
			src := 0
			if _, err := z.serverPools[src].GetObjectInfo(t.Context(), bucket, object, ObjectOptions{}); isErrObjectNotFound(err) {
				src = 1
			}
			for i := 0; i < 2; i++ {
				dst := 1 - src
				if _, err := moveObjectPool(t.Context(), z, bucket, object, src, dst, nil); err != nil {
					t.Fatal(err)
				}
				got, err := z.serverPools[dst].GetObjectInfo(t.Context(), bucket, object, ObjectOptions{})
				if err != nil || !bytes.Equal(got.Checksum, source.Checksum) || got.ETag != source.ETag || got.VersionID != source.VersionID || !got.ModTime.Equal(source.ModTime) {
					t.Fatalf("move changed version metadata: %v checksumEqual=%t ETag=%q/%q version=%q/%q modTimeEqual=%t", err, bytes.Equal(got.Checksum, source.Checksum), got.ETag, source.ETag, got.VersionID, source.VersionID, got.ModTime.Equal(source.ModTime))
				}
				rec := do(http.MethodGet, getGetObjectURL("", bucket, object), nil, map[string]string{xhttp.AmzChecksumMode: "ENABLED"})
				if !bytes.Equal(rec.Body.Bytes(), full) || rec.Header().Get(tc.typ.Key()) != mustChecksum(t, tc.typ, full) {
					t.Fatal("GET payload or checksum changed after move")
				}
				for j, data := range partData {
					rec := do(http.MethodGet, getGetObjectURL("", bucket, object)+fmt.Sprintf("?partNumber=%d", j+1), nil, nil)
					if !bytes.Equal(rec.Body.Bytes(), data) {
						t.Fatalf("part %d changed after move", j+1)
					}
				}
				src = dst
			}
		})
	}
}

type accessMoveNamedLocker struct {
	*localLocker
	address string
}

func (l accessMoveNamedLocker) String() string { return l.address }

func TestAccessMoveSharedDistributedLockers(t *testing.T) {
	// Three overlapping sets of real dsync lock servers. Taking independent
	// quorum locks for the same object would contend with our own earlier lock.
	peers := make([]dsync.NetLocker, 5)
	for i := range peers {
		peers[i] = accessMoveNamedLocker{localLocker: newLocker(), address: fmt.Sprint(i)}
	}
	z := &erasureServerPools{}
	for i := 0; i < 3; i++ {
		setPeers := peers[i : i+3]
		set := &erasureObjects{nsMutex: &nsLockMap{isDistErasure: true}, getLockers: func() ([]dsync.NetLocker, string) {
			return setPeers, "access-move-fixture"
		}}
		z.serverPools = append(z.serverPools, &erasureSets{sets: []*erasureObjects{set}, distributionAlgo: formatErasureVersionV3DistributionAlgoV3})
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, unlock, err := lockAccessTierObject(ctx, z, "bucket", "object", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(unlock)
	defer release()
	for i, pool := range z.serverPools {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		lock := pool.NewNSLock("bucket", "object")
		lc, err := lock.GetLock(ctx, globalOperationTimeout)
		cancel()
		if err == nil {
			lock.Unlock(lc)
			t.Fatalf("pool %d writer acquired its quorum during the move", i)
		}
	}
	release()
	// Every acquired peer lock is released, so ordinary writes can resume.
	for _, peer := range peers {
		// Distributed Unlock sends releases asynchronously.
		lock := (&nsLockMap{isDistErasure: true}).NewNSLock(func() ([]dsync.NetLocker, string) {
			return []dsync.NetLocker{peer}, "access-move-fixture"
		}, "bucket", "object")
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		lc, err := lock.GetLock(ctx, globalOperationTimeout)
		cancel()
		if err != nil {
			t.Fatalf("peer %s leaked a lock: %v", peer.String(), err)
		}
		lock.Unlock(lc)
	}
}

func TestAccessMoveConfiguredPromotionAndDemotion(t *testing.T) {
	z, bucket := accessMovePools(t)
	const object = "configured-move"
	version := putAccessMoveVersion(t, z, bucket, object, 1, "configured", false)
	oldCfg, oldTracker := globalILMConfig.accessCfg(), globalAccessTracker
	defer func() { globalILMConfig.update(oldCfg); globalAccessTracker = oldTracker }()
	cfg := oldCfg
	cfg.AccessTiering, cfg.AccessPools = true, []int{0, 1}
	cfg.AccessMinResidency, cfg.AccessPromoteWatermark = 0, 99
	globalILMConfig.update(cfg)
	globalAccessTracker = newAccessTracker()
	lc := accessLifecycleForTest(t)
	data, err := xml.Marshal(lc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := globalBucketMetadataSys.Update(t.Context(), bucket, bucketLifecycleConfig, data); err != nil {
		t.Fatal(err)
	}
	state := newAccessTierState(t.Context())
	state.z, state.objAPI = z, z
	task := accessTierTask{ctx: t.Context(), bucket: bucket, object: object, src: 1, dst: 0, direction: accessTierPromote, bytes: uint64(version.Size)}
	// A queued move is rechecked when configuration is disabled dynamically.
	cfg.AccessTiering = false
	globalILMConfig.update(cfg)
	if err := state.processTask(task); !errors.Is(err, errAccessTierNotEligible) {
		t.Fatalf("disabled mover = %v", err)
	}
	cfg.AccessTiering = true
	globalILMConfig.update(cfg)
	globalAccessTracker.merged.Store(&mergedAccess{binWidth: 60, entries: map[string]accessEntry{
		accessKey(bucket, object): {Bins: []uint32{100}, LastAt: time.Now().Unix()},
	}})
	if reason, ok := state.reservePromotion(task, cfg, 0); !ok {
		t.Fatalf("promotion reservation failed: %s", reason)
	}
	if err := state.processTask(task); err != nil {
		t.Fatal(err)
	}
	assertAccessMoveVersion(t, z, bucket, object, 0, version, "configured")
	// Advance the counter view beyond the rule's idle period.
	globalAccessTracker.merged.Store(&mergedAccess{binWidth: 60, entries: map[string]accessEntry{
		accessKey(bucket, object): {Bins: []uint32{0}, LastAt: time.Now().Add(-2 * time.Hour).Unix()},
	}})
	task.src, task.dst, task.direction, task.bytes = 0, 1, accessTierDemote, 0
	if err := state.processTask(task); err != nil {
		t.Fatal(err)
	}
	assertAccessMoveVersion(t, z, bucket, object, 1, version, "configured")
	if state.promotions.Load() != 1 || state.demotions.Load() != 1 || state.bytesMoved.Load() != 2*uint64(version.Size) || len(state.pending) != 0 || len(state.reserved) != 0 {
		t.Fatal("promotion/demotion metrics or reservation cleanup are incorrect")
	}
}
