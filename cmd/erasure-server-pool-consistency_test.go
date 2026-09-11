// Copyright (c) 2026 Feng Ruohang
//
// This file is part of Silo Object Storage stack
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
	"context"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	madmin "github.com/minio/madmin-go/v3"
	xhttp "github.com/minio/minio/internal/http"
)

func consistencyPools(t *testing.T) (*erasureServerPools, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	obj, dirs, err := prepareErasurePoolsWithContext(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	z := obj.(*erasureServerPools)
	previous := newObjectLayerFn()
	setObjectLayer(z)
	t.Cleanup(func() { cancel(); z.Shutdown(context.Background()); removeRoots(dirs); setObjectLayer(previous) })
	bucket := "pool-consistency"
	if err := z.MakeBucket(t.Context(), bucket, MakeBucketOptions{}); err != nil {
		t.Fatal(err)
	}
	return z, bucket
}

func putConsistencyObject(t *testing.T, z *erasureServerPools, bucket, object string, pool int, body string, opts ObjectOptions) ObjectInfo {
	t.Helper()
	oi, err := z.serverPools[pool].PutObject(t.Context(), bucket, object,
		mustGetPutObjReader(t, bytes.NewBufferString(body), int64(len(body)), "", ""), opts)
	if err != nil {
		t.Fatal(err)
	}
	return oi
}

func TestPoolsConditionalDeleteVersionSelection(t *testing.T) {
	z, bucket := consistencyPools(t)
	const object = "split-versions"
	old := putConsistencyObject(t, z, bucket, object, 1, "old", ObjectOptions{Versioned: true, MTime: time.Now().Add(-time.Hour)})
	latest := putConsistencyObject(t, z, bucket, object, 0, "latest", ObjectOptions{Versioned: true})
	_, err := z.DeleteObject(t.Context(), bucket, object, ObjectOptions{
		Versioned: true, VersionID: old.VersionID, HasIfMatch: true,
		CheckPrecondFn: func(oi ObjectInfo) bool { return oi.ETag != old.ETag },
	})
	if err != nil {
		t.Fatalf("delete addressed version in pool 1: %v", err)
	}
	if _, err := z.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{VersionID: old.VersionID}); !isErrVersionNotFound(err) {
		t.Errorf("addressed version survived: %v", err)
	}
	if oi, err := z.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{}); err != nil || oi.VersionID != latest.VersionID {
		t.Errorf("delete changed the latest version: %+v, %v", oi, err)
	}
}

func TestPoolsConditionalDeleteDuplicateVersion(t *testing.T) {
	z, bucket := consistencyPools(t)
	const object = "duplicate-version"
	oi := putConsistencyObject(t, z, bucket, object, 0, "payload", ObjectOptions{Versioned: true})
	putConsistencyObject(t, z, bucket, object, 1, "payload", ObjectOptions{Versioned: true, VersionID: oi.VersionID, MTime: oi.ModTime})
	_, err := z.DeleteObject(t.Context(), bucket, object, ObjectOptions{
		Versioned: true, VersionID: oi.VersionID, HasIfMatch: true,
		CheckPrecondFn: func(current ObjectInfo) bool { return current.ETag != oi.ETag },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := z.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{VersionID: oi.VersionID}); !isErrVersionNotFound(err) {
		t.Fatalf("success left a readable duplicate version: %v", err)
	}
}

func TestPoolsConditionalDeleteReportsOtherPoolFailure(t *testing.T) {
	z, bucket := consistencyPools(t)
	const object = "delete-failure"
	putConsistencyObject(t, z, bucket, object, 1, "older", ObjectOptions{MTime: time.Now().Add(-time.Hour)})
	oi := putConsistencyObject(t, z, bucket, object, 0, "latest", ObjectOptions{})
	set := z.serverPools[1].getHashedSet(object)
	getDisks := set.getDisks
	faulty := append([]StorageAPI(nil), getDisks()...)
	for i := range faulty {
		faulty[i] = accessMoveDeleteFaultDisk{StorageAPI: faulty[i], bucket: bucket, object: object}
	}
	set.getDisks = func() []StorageAPI { return faulty }
	defer func() { set.getDisks = getDisks }()
	_, err := z.DeleteObject(t.Context(), bucket, object, ObjectOptions{
		HasIfMatch: true, CheckPrecondFn: func(current ObjectInfo) bool { return current.ETag != oi.ETag },
	})
	if err == nil {
		t.Fatal("delete reported success despite the other pool's write failure")
	}
}

func TestPoolsConditionalDeleteSerializesPut(t *testing.T) {
	testPoolsConditionalDeleteWriter(t, false)
}

func TestPoolsConditionalDeleteSerializesCompletion(t *testing.T) {
	testPoolsConditionalDeleteWriter(t, true)
}

func testPoolsConditionalDeleteWriter(t *testing.T, multipart bool) {
	z, bucket := consistencyPools(t)
	const object = "concurrent-put"
	initial := putConsistencyObject(t, z, bucket, object, 1, "before", ObjectOptions{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	reader := mustGetPutObjReader(t, bytes.NewBufferString("after"), 5, "", "")
	destination := 1
	write := func() error {
		_, err := z.PutObject(ctx, bucket, object, reader, ObjectOptions{DataMovement: true, SrcPoolIdx: 0, DstPoolIdx: &destination})
		return err
	}
	if multipart {
		mp, err := z.serverPools[1].NewMultipartUpload(ctx, bucket, object, ObjectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		part, err := z.serverPools[1].PutObjectPart(ctx, bucket, object, mp.UploadID, 1, reader, ObjectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		write = func() error {
			_, err := z.CompleteMultipartUpload(ctx, bucket, object, mp.UploadID,
				[]CompletePart{{PartNumber: 1, ETag: part.ETag}}, ObjectOptions{})
			return err
		}
	}
	checked, resume := make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	defer release()
	deleted := make(chan error, 1)
	go func() {
		_, err := z.DeleteObject(ctx, bucket, object, ObjectOptions{
			HasIfMatch: true,
			CheckPrecondFn: func(oi ObjectInfo) bool {
				close(checked)
				<-resume
				return oi.ETag != initial.ETag
			},
		})
		deleted <- err
	}()
	select {
	case <-checked:
	case err := <-deleted:
		t.Fatalf("delete did not evaluate the precondition: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	written := make(chan error, 1)
	go func() { written <- write() }()
	var early bool
	select {
	case err := <-written:
		early = true
		if err != nil {
			t.Errorf("concurrent PUT: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
	}
	release()
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if !early {
		if err := <-written; err != nil {
			t.Fatal(err)
		}
	}
	if early {
		t.Error("PUT committed while DELETE was between its comparison and removal")
	}
	if _, err := z.GetObjectInfo(ctx, bucket, object, ObjectOptions{}); err != nil {
		t.Errorf("matching the old ETag removed the concurrent replacement: %v", err)
	}
}

type consistencyGateReader struct {
	io.Reader
	entered, resume chan struct{}
	once            sync.Once
	release         sync.Once
}

func (r *consistencyGateReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.entered); <-r.resume })
	return r.Reader.Read(p)
}

func TestPoolsReplicaSerializesMetadataAndHealing(t *testing.T) {
	for _, heal := range []bool{false, true} {
		t.Run(fmt.Sprintf("heal=%t", heal), func(t *testing.T) {
			z, bucket := consistencyPools(t)
			const object = "metadata-race"
			old, recent := "2026-09-09T09:00:00Z", "2026-09-09T10:00:00Z"
			meta := poolLockMetadata("GOVERNANCE", "OFF", old, old)
			oi := putConsistencyObject(t, z, bucket, object, 1, "data", ObjectOptions{Versioned: true, UserDefined: meta})
			z.poolMetaMutex.Lock()
			z.poolMeta.Pools[0].Decommission = &PoolDecommissionInfo{}
			z.poolMetaMutex.Unlock()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			gate := &consistencyGateReader{Reader: strings.NewReader("data"), entered: make(chan struct{}), resume: make(chan struct{})}
			release := func() { gate.release.Do(func() { close(gate.resume) }) }
			defer release()
			reader := mustGetPutObjReader(t, gate, 4, "", "")
			written := make(chan error, 1)
			go func() {
				_, err := z.PutObject(ctx, bucket, object, reader, ObjectOptions{
					Versioned: true, VersionID: oi.VersionID, MTime: oi.ModTime,
					ReplicaLockReconcile: true, UserDefined: maps.Clone(meta),
				})
				written <- err
			}()
			select {
			case <-gate.entered:
			case err := <-written:
				t.Fatalf("write failed before consuming the body: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			mutated := make(chan error, 1)
			go func() {
				var err error
				if heal {
					_, err = z.healObjectInPool(ctx, z.serverPools[1].getHashedSet(object), bucket, object, oi.VersionID, madmin.HealOpts{})
				} else {
					_, err = z.PutObjectMetadata(ctx, bucket, object, ObjectOptions{
						VersionID: oi.VersionID, MTime: oi.ModTime,
						EvalMetadataFn: func(current *ObjectInfo, _ error) (ReplicateDecision, error) {
							current.UserDefined[strings.ToLower(xhttp.AmzObjectLockLegalHold)] = "ON"
							current.UserDefined[ReservedMetadataPrefixLower+ObjectLockLegalHoldTimestamp] = recent
							return ReplicateDecision{}, nil
						},
					})
				}
				mutated <- err
			}()
			var early bool
			select {
			case err := <-mutated:
				early = true
				t.Errorf("mutation completed during the paused replica write: %v", err)
			case <-time.After(250 * time.Millisecond):
			}
			release()
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			if !early {
				if err := <-mutated; err != nil {
					t.Fatal(err)
				}
			}
			got, err := z.GetObjectInfo(ctx, bucket, object, ObjectOptions{VersionID: oi.VersionID})
			if err != nil {
				t.Fatal(err)
			}
			if !heal && storedObjectLockState(got.UserDefined).legalHold != "ON" {
				t.Error("replica write rolled back the newer metadata update")
			}
		})
	}
}

func TestPoolsConditionalDeletePreservesVersionHistory(t *testing.T) {
	z, bucket := consistencyPools(t)
	const object = "conditional-marker"
	older := putConsistencyObject(t, z, bucket, object, 1, "older", ObjectOptions{Versioned: true, MTime: time.Now().Add(-time.Hour)})
	latest := putConsistencyObject(t, z, bucket, object, 0, "latest", ObjectOptions{Versioned: true})
	_, err := z.DeleteObject(t.Context(), bucket, object, ObjectOptions{
		Versioned:      true,
		CheckPrecondFn: func(oi ObjectInfo) bool { return oi.ETag != older.ETag },
	})
	if !isErrPreconditionFailed(err) {
		t.Fatalf("wrong latest ETag: %v", err)
	}
	marker, err := z.DeleteObject(t.Context(), bucket, object, ObjectOptions{
		Versioned:      true,
		CheckPrecondFn: func(oi ObjectInfo) bool { return oi.ETag != latest.ETag },
	})
	if err != nil || !marker.DeleteMarker {
		t.Fatalf("logical delete did not create a marker: %+v, %v", marker, err)
	}
	if got, err := z.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{}); !isErrObjectNotFound(err) || !got.DeleteMarker {
		t.Errorf("delete marker did not hide the split object: %+v, %v", got, err)
	}
	for _, version := range []string{older.VersionID, latest.VersionID} {
		if _, err := z.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{VersionID: version}); err != nil {
			t.Errorf("conditional marker removed history %s: %v", version, err)
		}
	}
}

func poolLockMetadata(retention, hold, retentionTime, holdTime string) map[string]string {
	return map[string]string{
		strings.ToLower(xhttp.AmzObjectLockMode):                   retention,
		strings.ToLower(xhttp.AmzObjectLockRetainUntilDate):        "2030-01-01T00:00:00Z",
		strings.ToLower(xhttp.AmzObjectLockLegalHold):              hold,
		ReservedMetadataPrefixLower + ObjectLockRetentionTimestamp: retentionTime,
		ReservedMetadataPrefixLower + ObjectLockLegalHoldTimestamp: holdTime,
	}
}

func TestPoolsReplicaIndependentLockWinners(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		multipart, rebalance bool
	}{
		{"put-decommission", false, false},
		{"put-rebalance", false, true},
		{"multipart-decommission", true, false},
		{"multipart-rebalance", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			z, bucket := consistencyPools(t)
			const object = "split-lock-state"
			old, recent := "2026-09-09T09:00:00Z", "2026-09-09T10:00:00Z"
			first := poolLockMetadata("GOVERNANCE", "OFF", recent, old)
			second := poolLockMetadata("", "ON", old, recent)
			oi := putConsistencyObject(t, z, bucket, object, 0, "data", ObjectOptions{Versioned: true, UserDefined: first})
			putConsistencyObject(t, z, bucket, object, 1, "data", ObjectOptions{
				Versioned: true, VersionID: oi.VersionID, MTime: oi.ModTime, UserDefined: second,
			})
			opts := ObjectOptions{
				Versioned: true, VersionID: oi.VersionID, MTime: oi.ModTime,
				ReplicaLockReconcile: true, UserDefined: poolLockMetadata("", "OFF", old, old),
			}
			var uploadID string
			var parts []CompletePart
			if tc.multipart {
				mp, err := z.serverPools[1].NewMultipartUpload(t.Context(), bucket, object, opts)
				if err != nil {
					t.Fatal(err)
				}
				uploadID = mp.UploadID
				part, err := z.serverPools[1].PutObjectPart(t.Context(), bucket, object, uploadID, 1,
					mustGetPutObjReader(t, bytes.NewBufferString("data"), 4, "", ""), ObjectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				parts = []CompletePart{{PartNumber: 1, ETag: part.ETag}}
			}
			// The upload and both versions predate the routing change. Retention's
			// winner remains in the draining pool; legal hold's winner is in pool 1.
			if tc.rebalance {
				z.rebalMu.Lock()
				z.rebalMeta = &rebalanceMeta{PoolStats: []*rebalanceStats{
					{Participating: true, Info: rebalanceInfo{Status: rebalStarted}}, {},
				}}
				z.rebalMu.Unlock()
			} else {
				z.poolMetaMutex.Lock()
				z.poolMeta.Pools[0].Decommission = &PoolDecommissionInfo{}
				z.poolMetaMutex.Unlock()
			}
			var err error
			if tc.multipart {
				_, err = z.CompleteMultipartUpload(t.Context(), bucket, object, uploadID, parts,
					ObjectOptions{Versioned: true, MTime: oi.ModTime, ReplicaLockReconcile: true})
			} else {
				_, err = z.PutObject(t.Context(), bucket, object,
					mustGetPutObjReader(t, bytes.NewBufferString("data"), 4, "", ""), opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := z.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{VersionID: oi.VersionID})
			if err != nil {
				t.Fatal(err)
			}
			state := storedObjectLockState(got.UserDefined)
			if state.mode != "GOVERNANCE" || state.retentionTimestamp != recent || state.legalHold != "ON" || state.legalHoldTimestamp != recent {
				t.Errorf("pooled read lost independently ordered lock state: %+v", state)
			}
			if _, err := z.serverPools[0].GetObjectInfo(t.Context(), bucket, object, ObjectOptions{VersionID: oi.VersionID}); !isErrVersionNotFound(err) {
				t.Errorf("successful replacement left its stale competing copy: %v", err)
			}
		})
	}
}

func TestPoolsReplicaSoleDrainingOwner(t *testing.T) {
	for _, rebalance := range []bool{false, true} {
		for _, null := range []bool{false, true} {
			t.Run(fmt.Sprintf("rebalance=%t/null=%t", rebalance, null), func(t *testing.T) {
				z, bucket := consistencyPools(t)
				const object = "sole-owner"
				old, recent := "2026-09-09T09:00:00Z", "2026-09-09T10:00:00Z"
				meta := poolLockMetadata("", "ON", recent, recent)
				delete(meta, strings.ToLower(xhttp.AmzObjectLockRetainUntilDate))
				oi := putConsistencyObject(t, z, bucket, object, 0, "data", ObjectOptions{Versioned: !null, UserDefined: meta})
				versionID := oi.VersionID
				if null {
					versionID = nullVersionID
					// A different latest version must not donate its lock to the null version.
					putConsistencyObject(t, z, bucket, object, 1, "other-version", ObjectOptions{Versioned: true})
				}
				if rebalance {
					z.rebalMu.Lock()
					z.rebalMeta = &rebalanceMeta{PoolStats: []*rebalanceStats{
						{Participating: true, Info: rebalanceInfo{Status: rebalStarted}}, {},
					}}
					z.rebalMu.Unlock()
				} else {
					z.poolMetaMutex.Lock()
					z.poolMeta.Pools[0].Decommission = &PoolDecommissionInfo{}
					z.poolMetaMutex.Unlock()
				}
				_, err := z.PutObject(t.Context(), bucket, object,
					mustGetPutObjReader(t, bytes.NewBufferString("data"), 4, "", ""), ObjectOptions{
						Versioned: !null, VersionSuspended: null, VersionID: versionID, MTime: oi.ModTime,
						ReplicaLockReconcile: true, UserDefined: poolLockMetadata("GOVERNANCE", "OFF", old, old),
					})
				if err != nil {
					t.Fatal(err)
				}
				got, err := z.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{VersionID: versionID})
				if err != nil {
					t.Fatal(err)
				}
				state := storedObjectLockState(got.UserDefined)
				if state.mode != "" || state.retainUntil != "" || state.retentionTimestamp != recent || state.legalHold != "ON" {
					t.Errorf("lost a removal or legal hold from the sole draining owner: %+v", state)
				}
			})
		}
	}
}

func TestPoolsMetadataUpdateUsesMergedVersion(t *testing.T) {
	z, bucket := consistencyPools(t)
	const object = "metadata-update"
	old, recent := "2026-09-09T09:00:00Z", "2026-09-09T10:00:00Z"
	oi := putConsistencyObject(t, z, bucket, object, 0, "data", ObjectOptions{
		Versioned: true, UserDefined: poolLockMetadata("GOVERNANCE", "OFF", recent, old),
	})
	putConsistencyObject(t, z, bucket, object, 1, "data", ObjectOptions{
		Versioned: true, VersionID: oi.VersionID, MTime: oi.ModTime, UserDefined: poolLockMetadata("", "ON", old, recent),
	})
	latest := putConsistencyObject(t, z, bucket, object, 1, "latest", ObjectOptions{Versioned: true})
	called := 0
	_, err := z.PutObjectMetadata(t.Context(), bucket, object, ObjectOptions{
		VersionID: oi.VersionID, MTime: oi.ModTime,
		EvalMetadataFn: func(current *ObjectInfo, _ error) (ReplicateDecision, error) {
			called++
			state := storedObjectLockState(current.UserDefined)
			if state.mode != "GOVERNANCE" || state.legalHold != "ON" {
				return ReplicateDecision{}, fmt.Errorf("metadata policy evaluated stale state: %+v", state)
			}
			current.UserDefined["custom-update"] = "preserved"
			return ReplicateDecision{}, nil
		},
	})
	if err != nil || called != 1 {
		t.Fatalf("metadata update: %v; callback count %d", err, called)
	}
	for i, pool := range z.serverPools {
		got, err := pool.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{VersionID: oi.VersionID})
		if err != nil {
			t.Fatal(err)
		}
		state := storedObjectLockState(got.UserDefined)
		if state.mode != "GOVERNANCE" || state.legalHold != "ON" || got.UserDefined["custom-update"] != "preserved" {
			t.Errorf("pool %d did not receive the merged update: %+v", i, got.UserDefined)
		}
	}
	if current, err := z.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{}); err != nil || current.VersionID != latest.VersionID {
		t.Errorf("updating an older version changed the latest: %+v, %v", current, err)
	}
}

func TestPoolsReplicaMetadataCopyReconcilesLockAndTags(t *testing.T) {
	z, bucket := consistencyPools(t)
	const object = "replica-metadata-copy"
	old, recent := "2026-09-09T09:00:00Z", "2026-09-09T10:00:00Z"
	meta := poolLockMetadata("GOVERNANCE", "OFF", recent, old)
	meta[xhttp.AmzObjectTagging] = "key=new"
	meta[ReservedMetadataPrefixLower+TaggingTimestamp] = recent
	oi := putConsistencyObject(t, z, bucket, object, 0, "data", ObjectOptions{Versioned: true, UserDefined: meta})
	putConsistencyObject(t, z, bucket, object, 1, "data", ObjectOptions{
		Versioned: true, VersionID: oi.VersionID, MTime: oi.ModTime, UserDefined: poolLockMetadata("", "ON", old, recent),
	})
	stale := oi
	stale.metadataOnly = true
	stale.UserDefined = maps.Clone(oi.UserDefined)
	stale.UserDefined[xhttp.AmzObjectTagging] = "key=old"
	stale.UserDefined[ReservedMetadataPrefixLower+TaggingTimestamp] = old
	_, err := z.CopyObject(t.Context(), bucket, object, bucket, object, stale,
		ObjectOptions{VersionID: oi.VersionID}, ObjectOptions{
			Versioned: true, VersionID: oi.VersionID, MTime: oi.ModTime, ReplicaLockReconcile: true,
		})
	if err != nil {
		t.Fatal(err)
	}
	got, err := z.GetObjectInfo(t.Context(), bucket, object, ObjectOptions{VersionID: oi.VersionID})
	if err != nil {
		t.Fatal(err)
	}
	state := storedObjectLockState(got.UserDefined)
	if state.mode != "GOVERNANCE" || state.legalHold != "ON" || got.UserTags != "key=new" {
		t.Errorf("metadata replication rolled back a newer field: %+v", got.UserDefined)
	}
}

func TestPoolsReplicaCleanupFailureCanRetry(t *testing.T) {
	z, bucket := consistencyPools(t)
	const object = "replica-cleanup-failure"
	old, recent := "2026-09-09T09:00:00Z", "2026-09-09T10:00:00Z"
	oi := putConsistencyObject(t, z, bucket, object, 0, "data", ObjectOptions{
		Versioned: true, UserDefined: poolLockMetadata("GOVERNANCE", "ON", recent, recent),
	})
	z.poolMetaMutex.Lock()
	z.poolMeta.Pools[0].Decommission = &PoolDecommissionInfo{}
	z.poolMetaMutex.Unlock()
	set := z.serverPools[0].getHashedSet(object)
	getDisks := set.getDisks
	faulty := append([]StorageAPI(nil), getDisks()...)
	for i := range faulty {
		faulty[i] = accessMoveDeleteFaultDisk{StorageAPI: faulty[i], bucket: bucket, object: object, version: oi.VersionID}
	}
	set.getDisks = func() []StorageAPI { return faulty }
	defer func() { set.getDisks = getDisks }()
	write := func() error {
		_, err := z.PutObject(t.Context(), bucket, object,
			mustGetPutObjReader(t, bytes.NewBufferString("data"), 4, "", ""), ObjectOptions{
				Versioned: true, VersionID: oi.VersionID, MTime: oi.ModTime, ReplicaLockReconcile: true,
				UserDefined: poolLockMetadata("", "OFF", old, old),
			})
		return err
	}
	if err := write(); err == nil {
		t.Fatal("replica replacement hid a competing-copy cleanup failure")
	}
	if got, err := z.serverPools[1].GetObjectInfo(t.Context(), bucket, object, ObjectOptions{VersionID: oi.VersionID}); err != nil || storedObjectLockState(got.UserDefined).legalHold != "ON" {
		t.Fatalf("cleanup failure lost the committed reconciled version: %+v, %v", got, err)
	}
	set.getDisks = getDisks
	if err := write(); err != nil {
		t.Fatalf("retry could not finish cleanup: %v", err)
	}
	if _, err := z.serverPools[0].GetObjectInfo(t.Context(), bucket, object, ObjectOptions{VersionID: oi.VersionID}); !isErrVersionNotFound(err) {
		t.Errorf("retry left the competing version: %v", err)
	}
}
