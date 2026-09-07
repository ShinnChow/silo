// Copyright (c) 2015-2026 MinIO, Inc.
// Copyright (c) 2026 PGSTY
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
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio/internal/auth"
)

// ---------------------------------------------------------------------------
// Target 1 (issue #105): a higher-level lifecycle XML merge racing another
// bucket-metadata transition outside BucketMetadataSys.Delete.
//
// Before the fix, PeerBucketLCConfigHandler / healBucketILMExpiry read the
// current lifecycle document without metadata.lock, merged the replicated expiry
// rules with the local transition rules, and then persisted the merged blob with
// BucketMetadataSys.Update. Update re-read the record under metadata.lock but
// overwrote LifecycleConfigXML wholesale with the pre-computed blob, so any
// lifecycle transition change committed between the merge read and the merge
// write was silently lost on disk. BucketMetadataSys.UpdateExpiryLCConfig now
// performs the read, merge, and save under a single metadata.lock. This test
// drives PeerBucketLCConfigHandler and asserts the concurrent change survives.
// ---------------------------------------------------------------------------

type lcMergeWriterKey struct{}

// lcMergeBarrier pauses the merge writer (context value "M") exactly when it
// tries to take metadata.lock for its persisting Update. By that point the
// merge has already read the stale lifecycle document, so the test can commit a
// concurrent transition change before releasing the merge write.
type lcMergeBarrier struct {
	ObjectLayer
	bucket   string
	mAtLock  chan struct{}
	mProceed chan struct{}
	mOnce    sync.Once
}

func (o *lcMergeBarrier) metadataLock() string {
	return pathJoin(bucketMetaPrefix, o.bucket, "metadata.lock")
}

func (o *lcMergeBarrier) NewNSLock(bucket string, objects ...string) RWLocker {
	lock := o.ObjectLayer.NewNSLock(bucket, objects...)
	if bucket != minioMetaBucket || len(objects) != 1 || objects[0] != o.metadataLock() {
		return lock
	}
	return metadataObservedRWLocker{RWLocker: lock, onLock: func(ctx context.Context) {
		if ctx.Value(lcMergeWriterKey{}) != "M" {
			return
		}
		o.mOnce.Do(func() { close(o.mAtLock) })
		select {
		case <-o.mProceed:
		case <-ctx.Done():
		}
	}}
}

func TestLifecycleExpiryMergeRaceLosesConcurrentTransition(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testLifecycleExpiryMergeRaceLosesConcurrentTransition,
	})
}

func testLifecycleExpiryMergeRaceLosesConcurrentTransition(obj ObjectLayer, instanceType, bucket string,
	_ http.Handler, _ auth.Credentials, t *testing.T,
) {
	// Revision N: a single transition-only rule.
	baseXML := []byte(`<LifecycleConfiguration><Rule><ID>keep</ID><Filter><Prefix>data/</Prefix></Filter><Status>Enabled</Status><Transition><Days>30</Days><StorageClass>WARM</StorageClass></Transition></Rule></LifecycleConfiguration>`)
	if _, err := globalBucketMetadataSys.Update(t.Context(), bucket, bucketLifecycleConfig, baseXML); err != nil {
		t.Fatalf("%s: seed lifecycle: %v", instanceType, err)
	}

	// A replicated expiry-only rule arriving from a peer site.
	expXML := `<LifecycleConfiguration><Rule><ID>expire</ID><Filter><Prefix>tmp/</Prefix></Filter><Status>Enabled</Status><Expiration><Days>7</Days></Expiration></Rule></LifecycleConfiguration>`
	expLCConfig := base64.StdEncoding.EncodeToString([]byte(expXML))

	previousObjectAPI := newObjectLayerFn()
	barrier := &lcMergeBarrier{
		ObjectLayer: obj,
		bucket:      bucket,
		mAtLock:     make(chan struct{}),
		mProceed:    make(chan struct{}),
	}
	setObjectLayer(barrier)
	defer setObjectLayer(previousObjectAPI)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	mCtx := context.WithValue(ctx, lcMergeWriterKey{}, "M")

	mDone := make(chan error, 1)
	go func() {
		mDone <- globalSiteReplicationSys.PeerBucketLCConfigHandler(mCtx, bucket, &expLCConfig, UTCNow())
	}()

	// Wait until the merge writer has read revision N and is about to persist.
	select {
	case <-barrier.mAtLock:
	case err := <-mDone:
		t.Fatalf("%s: merge writer finished before persisting: %v", instanceType, err)
	case <-ctx.Done():
		t.Fatalf("%s: merge writer never reached metadata.lock: %v", instanceType, ctx.Err())
	}

	// Concurrent local lifecycle transition change commits revision N+1.
	concurrentXML := []byte(`<LifecycleConfiguration><Rule><ID>keep</ID><Filter><Prefix>data/</Prefix></Filter><Status>Enabled</Status><Transition><Days>10</Days><StorageClass>COLD</StorageClass></Transition></Rule></LifecycleConfiguration>`)
	if _, err := globalBucketMetadataSys.Update(ctx, bucket, bucketLifecycleConfig, concurrentXML); err != nil {
		t.Fatalf("%s: concurrent transition update: %v", instanceType, err)
	}

	// Release the merge write so it lands after the concurrent commit.
	close(barrier.mProceed)
	if err := <-mDone; err != nil {
		t.Fatalf("%s: merge writer failed: %v", instanceType, err)
	}

	// The persisted lifecycle must contain both the replicated expiry rule and
	// the concurrent transition change.
	cfg, _, err := globalBucketMetadataSys.GetLifecycleConfig(bucket)
	if err != nil {
		t.Fatalf("%s: read merged lifecycle: %v", instanceType, err)
	}
	var keep, expire bool
	for i := range cfg.Rules {
		switch cfg.Rules[i].ID {
		case "keep":
			keep = true
			if cfg.Rules[i].Transition.Days != 10 || cfg.Rules[i].Transition.StorageClass != "COLD" {
				t.Fatalf("%s: lifecycle merge overwrote the concurrent transition change: got Days=%d StorageClass=%q, want Days=10 StorageClass=COLD",
					instanceType, cfg.Rules[i].Transition.Days, cfg.Rules[i].Transition.StorageClass)
			}
		case "expire":
			expire = true
		}
	}
	if !expire {
		t.Fatalf("%s: merged lifecycle dropped the replicated expiry rule: %+v", instanceType, cfg.Rules)
	}
	if !keep {
		t.Fatalf("%s: merged lifecycle dropped the transition rule entirely: %+v", instanceType, cfg.Rules)
	}
}

// ---------------------------------------------------------------------------
// Target 2 (issue #105): DeleteBucket racing an already in-flight metadata
// writer and resurrecting a ghost .metadata.bin record.
//
// erasureServerPools.DeleteBucket takes only <bucket>.lck and purges the whole
// metadata prefix with deleteAll. Config writers (updateAndParse) take only
// metadata.lock. The two locks do not exclude each other, so a writer that is
// past its read and holding metadata.lock can persist .metadata.bin AFTER the
// delete has purged the prefix, resurrecting a record for a deleted bucket.
// ---------------------------------------------------------------------------

func TestDeleteBucketResurrectsGhostMetadata(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testDeleteBucketResurrectsGhostMetadata,
	})
}

func testDeleteBucketResurrectsGhostMetadata(obj ObjectLayer, instanceType, bucket string,
	_ http.Handler, _ auth.Credentials, t *testing.T,
) {
	previousObjectAPI := newObjectLayerFn()
	barrier := &metadataRMWBarrierObjectLayer{
		ObjectLayer:  obj,
		bucket:       bucket,
		aReady:       make(chan struct{}),
		aRelease:     make(chan struct{}),
		bLockAttempt: make(chan struct{}),
	}
	setObjectLayer(barrier)
	defer setObjectLayer(previousObjectAPI)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	aCtx := context.WithValue(ctx, metadataRMWWriterKey{}, "A")

	policyJSON := []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::` + bucket + `/*"}]}`)
	aDone := make(chan error, 1)
	// Writer A holds metadata.lock and pauses at the .metadata.bin PutObject.
	go func() {
		_, err := globalBucketMetadataSys.Update(aCtx, bucket, bucketPolicyConfig, policyJSON)
		aDone <- err
	}()

	select {
	case <-barrier.aReady:
	case err := <-aDone:
		t.Fatalf("%s: writer A finished before persisting: %v", instanceType, err)
	case <-ctx.Done():
		t.Fatalf("%s: writer A never reached metadata save: %v", instanceType, ctx.Err())
	}

	// Delete the bucket while writer A is paused mid-save.
	delDone := make(chan error, 1)
	go func() {
		delDone <- obj.DeleteBucket(ctx, bucket, DeleteBucketOptions{Force: true})
	}()

	select {
	case err := <-delDone:
		// Unfixed path: DeleteBucket does not serialize on metadata.lock, so it
		// purges the prefix immediately. Release A so its save recreates the file.
		if err != nil {
			t.Fatalf("%s: delete bucket: %v", instanceType, err)
		}
		close(barrier.aRelease)
		if err := <-aDone; err != nil {
			t.Fatalf("%s: writer A failed: %v", instanceType, err)
		}
	case <-time.After(3 * time.Second):
		// Fixed path: DeleteBucket is blocked waiting for metadata.lock held by
		// A. Release A so it finishes, then the purge runs after the save.
		close(barrier.aRelease)
		if err := <-aDone; err != nil {
			t.Fatalf("%s: writer A failed: %v", instanceType, err)
		}
		if err := <-delDone; err != nil {
			t.Fatalf("%s: delete bucket: %v", instanceType, err)
		}
	}

	// After DeleteBucket, no .metadata.bin record may remain on disk.
	if meta, err := readBucketMetadata(ctx, obj, bucket); err == nil {
		t.Fatalf("%s: ghost .metadata.bin resurrected after DeleteBucket: name=%q created=%s policyLen=%d",
			instanceType, meta.Name, meta.Created, len(meta.PolicyConfigJSON))
	} else if !errors.Is(err, errConfigNotFound) && !isErrBucketNotFound(err) && !errors.Is(err, errVolumeNotFound) {
		t.Fatalf("%s: unexpected error reading deleted bucket metadata: %v", instanceType, err)
	}
}

// ---------------------------------------------------------------------------
// Target 3 (issue #105): overlapping peer reloads publishing an older cache
// record after a newer save, leaving the resident cache one revision behind
// until refresh.
//
// LoadBucketMetadataHandler does loadBucketMetadata (read) followed by a publish
// into the resident cache. Before the fix that publish was an unconditional
// BucketMetadataSys.Set, so a reload that read an older on-disk revision could
// overwrite a newer resident record when it published late (unlike
// refreshBucketsMetadataLoop, which guards on lastUpdate()). The publish is now
// BucketMetadataSys.setReloaded, which refuses to regress a newer resident copy.
//
// The peer handler hardcodes context.Background(), so its reload core is mirrored
// here to inject deterministic ordering. This is a resident-cache freshness
// defect only: the persisted record always stays correct.
// ---------------------------------------------------------------------------

type reloadWriterKey struct{}

// reloadStaleBarrier captures the old on-disk metadata for the reload tagged
// "R1" and holds R1's read open until released, so a newer revision can be
// written and published before R1 finishes publishing the stale copy.
type reloadStaleBarrier struct {
	ObjectLayer
	bucket    string
	r1Reading chan struct{}
	r1Release chan struct{}
	once      sync.Once
}

func (o *reloadStaleBarrier) metadataObject() string {
	return pathJoin(bucketMetaPrefix, o.bucket, bucketMetadataFile)
}

func (o *reloadStaleBarrier) GetObjectNInfo(ctx context.Context, bucket, object string, rs *HTTPRangeSpec, h http.Header, opts ObjectOptions) (*GetObjectReader, error) {
	if bucket == minioMetaBucket && object == o.metadataObject() && ctx.Value(reloadWriterKey{}) == "R1" {
		gr, err := o.ObjectLayer.GetObjectNInfo(ctx, bucket, object, rs, h, opts)
		if err != nil {
			return nil, err
		}
		data, rerr := io.ReadAll(gr)
		oi := gr.ObjInfo
		gr.Close()
		if rerr != nil {
			return nil, rerr
		}
		o.once.Do(func() { close(o.r1Reading) })
		select {
		case <-o.r1Release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return NewGetObjectReaderFromReader(bytes.NewReader(data), oi, opts)
	}
	return o.ObjectLayer.GetObjectNInfo(ctx, bucket, object, rs, h, opts)
}

func TestOverlappingReloadPublishesStaleResidentCache(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{
		t:          t,
		objAPITest: testOverlappingReloadPublishesStaleResidentCache,
	})
}

func residentTagValue(t *testing.T, instanceType, bucket string) string {
	t.Helper()
	cfg, _, err := globalBucketMetadataSys.GetTaggingConfig(bucket)
	if err != nil {
		t.Fatalf("%s: read resident tagging: %v", instanceType, err)
	}
	return cfg.ToMap()["rev"]
}

func testOverlappingReloadPublishesStaleResidentCache(obj ObjectLayer, instanceType, bucket string,
	_ http.Handler, _ auth.Credentials, t *testing.T,
) {
	// Revision 0 on disk and in the resident cache.
	tag0 := []byte(`<Tagging><TagSet><Tag><Key>rev</Key><Value>0</Value></Tag></TagSet></Tagging>`)
	if _, err := globalBucketMetadataSys.Update(t.Context(), bucket, bucketTaggingConfig, tag0); err != nil {
		t.Fatalf("%s: seed rev0: %v", instanceType, err)
	}

	previousObjectAPI := newObjectLayerFn()
	barrier := &reloadStaleBarrier{
		ObjectLayer: obj,
		bucket:      bucket,
		r1Reading:   make(chan struct{}),
		r1Release:   make(chan struct{}),
	}
	setObjectLayer(barrier)
	defer setObjectLayer(previousObjectAPI)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	r1Ctx := context.WithValue(ctx, reloadWriterKey{}, "R1")

	// reload mirrors the core of LoadBucketMetadataHandler (which hardcodes
	// context.Background() and cannot take an injected context). The publish
	// uses setReloaded, exactly as the fixed handler does.
	reload := func(rctx context.Context) error {
		meta, err := loadBucketMetadata(rctx, newObjectLayerFn(), bucket)
		if err != nil {
			return err
		}
		globalBucketMetadataSys.setReloaded(bucket, meta)
		return nil
	}

	// R1: an overlapping peer reload that reads rev0 and then stalls before it
	// publishes.
	r1Done := make(chan error, 1)
	go func() { r1Done <- reload(r1Ctx) }()

	select {
	case <-barrier.r1Reading:
	case err := <-r1Done:
		t.Fatalf("%s: R1 finished before publishing: %v", instanceType, err)
	case <-ctx.Done():
		t.Fatalf("%s: R1 never read metadata: %v", instanceType, ctx.Err())
	}

	// A newer save commits revision 1 to disk and the resident cache.
	tag1 := []byte(`<Tagging><TagSet><Tag><Key>rev</Key><Value>1</Value></Tag></TagSet></Tagging>`)
	if _, err := globalBucketMetadataSys.Update(ctx, bucket, bucketTaggingConfig, tag1); err != nil {
		t.Fatalf("%s: commit rev1: %v", instanceType, err)
	}
	// R2: a second overlapping peer reload that reads rev1 and publishes it.
	if err := reload(ctx); err != nil {
		t.Fatalf("%s: R2 reload: %v", instanceType, err)
	}
	if got := residentTagValue(t, instanceType, bucket); got != "1" {
		t.Fatalf("%s: precondition failed, resident cache should be rev1 before R1 publishes, got %q", instanceType, got)
	}

	// Release R1 so its stale publish lands after the newer save.
	close(barrier.r1Release)
	if err := <-r1Done; err != nil {
		t.Fatalf("%s: R1 reload: %v", instanceType, err)
	}

	// The persisted record must stay at rev1 (persistent correctness).
	if dcfg, perr := globalBucketMetadataSys.GetConfigFromDisk(ctx, bucket); perr != nil {
		t.Fatalf("%s: reload disk metadata: %v", instanceType, perr)
	} else if got := dcfg.taggingConfig.ToMap()["rev"]; got != "1" {
		t.Fatalf("%s: persisted record regressed to rev %q, want rev1", instanceType, got)
	}

	// The resident cache must not be left behind at rev0.
	if got := residentTagValue(t, instanceType, bucket); got != "1" {
		t.Fatalf("%s: overlapping reload left resident cache at rev %q, want rev1", instanceType, got)
	}
}
