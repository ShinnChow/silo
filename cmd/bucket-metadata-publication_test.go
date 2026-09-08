// Copyright 2026 PGSTY contributors.
// SPDX-License-Identifier: AGPL-3.0-or-later

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio/internal/auth"
	"github.com/minio/minio/internal/grid"
)

func TestDeleteBucketMetadataLockCancellation(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{t: t, objAPITest: testDeleteBucketMetadataLockCancellation})
}

func testDeleteBucketMetadataLockCancellation(obj ObjectLayer, instanceType, bucket string, _ http.Handler, _ auth.Credentials, t *testing.T) {
	_, unlock, err := lockBucketMetadata(t.Context(), obj, bucket)
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(unlock)
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- obj.DeleteBucket(ctx, bucket, DeleteBucketOptions{Force: true, NoLock: true}) }()
	<-ctx.Done()
	// A metadata-lock failure must happen before the destructive operation.
	if _, err := obj.GetBucketInfo(t.Context(), bucket, BucketOptions{}); err != nil {
		t.Errorf("%s: bucket disappeared while metadata.lock was unavailable: %v", instanceType, err)
	}
	release()
	if err := <-done; err == nil {
		t.Errorf("%s: canceled deletion succeeded", instanceType)
	}
	if _, err := readBucketMetadata(t.Context(), obj, bucket); err != nil {
		t.Errorf("%s: canceled deletion removed metadata: %v", instanceType, err)
	}
}

func TestQueuedMetadataUpdateAfterDelete(t *testing.T) {
	defer DetectTestLeak(t)()
	for _, expiry := range []bool{false, true} {
		t.Run(fmt.Sprintf("expiry=%v", expiry), func(t *testing.T) {
			ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{t: t, objAPITest: func(obj ObjectLayer, instanceType, bucket string, _ http.Handler, _ auth.Credentials, t *testing.T) {
				previous := newObjectLayerFn()
				barrier := &lcMergeBarrier{ObjectLayer: obj, bucket: bucket, mAtLock: make(chan struct{}), mProceed: make(chan struct{})}
				setObjectLayer(barrier)
				defer setObjectLayer(previous)
				release := sync.OnceFunc(func() { close(barrier.mProceed) })
				defer release()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				ctx = context.WithValue(ctx, lcMergeWriterKey{}, "M")
				done := make(chan error, 1)
				go func() {
					if expiry {
						done <- globalBucketMetadataSys.UpdateExpiryLCConfig(ctx, bucket, nil, UTCNow())
						return
					}
					_, err := globalBucketMetadataSys.Update(ctx, bucket, bucketTaggingConfig, []byte(`<Tagging><TagSet/></Tagging>`))
					done <- err
				}()
				select {
				case <-barrier.mAtLock:
				case <-ctx.Done():
					t.Fatal("writer did not reach metadata.lock")
				}
				if err := obj.DeleteBucket(t.Context(), bucket, DeleteBucketOptions{Force: true}); err != nil {
					t.Fatal(err)
				}
				release()
				if err := <-done; !isErrBucketNotFound(err) {
					t.Errorf("%s: queued update should reject a deleted bucket, got %v", instanceType, err)
				}
				if _, err := readBucketMetadata(t.Context(), obj, bucket); !errors.Is(err, errConfigNotFound) && !isErrBucketNotFound(err) {
					t.Errorf("%s: queued update recreated metadata: %v", instanceType, err)
				}
			}})
		})
	}
}

// Pause the first actual peer-handler read, without replacing the handler or
// requiring it to accept a test-only context.
type peerMetadataReadBarrier struct {
	ObjectLayer
	bucket           string
	once             sync.Once
	reading, release chan struct{}
}

func (o *peerMetadataReadBarrier) GetObjectNInfo(ctx context.Context, bucket, object string, rs *HTTPRangeSpec, h http.Header, opts ObjectOptions) (*GetObjectReader, error) {
	first := false
	if bucket == minioMetaBucket && object == pathJoin(bucketMetaPrefix, o.bucket, bucketMetadataFile) {
		o.once.Do(func() { first = true })
	}
	gr, err := o.ObjectLayer.GetObjectNInfo(ctx, bucket, object, rs, h, opts)
	if err != nil || !first {
		return gr, err
	}
	data, err := io.ReadAll(gr)
	oi := gr.ObjInfo
	gr.Close()
	if err != nil {
		return nil, err
	}
	close(o.reading)
	select {
	case <-o.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return NewGetObjectReaderFromReader(bytes.NewReader(data), oi, opts)
}

func TestPeerMetadataReloadPreservesCurrentTargets(t *testing.T) {
	defer DetectTestLeak(t)()
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{t: t, objAPITest: testPeerMetadataReloadPreservesCurrentTargets})
}

func testPeerMetadataReloadPreservesCurrentTargets(obj ObjectLayer, instanceType, bucket string, _ http.Handler, _ auth.Credentials, t *testing.T) {
	seed := func(revision string) {
		t.Helper()
		notification := []byte(`<NotificationConfiguration><QueueConfiguration><Id>` + revision + `</Id><Queue>arn:minio:sqs::` + revision + `:webhook</Queue><Event>s3:ObjectCreated:*</Event></QueueConfiguration></NotificationConfiguration>`)
		targets, err := json.Marshal(madmin.BucketTargets{Targets: []madmin.BucketTarget{{
			SourceBucket: bucket, TargetBucket: bucket, Endpoint: "127.0.0.1:9000", Arn: revision,
			Credentials: &madmin.Credentials{AccessKey: "fixture", SecretKey: "fixture-secret"},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, config := range []struct {
			name string
			data []byte
		}{{bucketNotificationConfig, notification}, {bucketTargetsFile, targets}} {
			if _, err := globalBucketMetadataSys.Update(t.Context(), bucket, config.name, config.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	reload := func() error {
		args := grid.MSS{peerRESTBucket: bucket}
		_, err := (&peerRESTServer{}).LoadBucketMetadataHandler(&args)
		if err != nil {
			return err
		}
		return nil
	}
	seed("old")
	previous := newObjectLayerFn()
	barrier := &peerMetadataReadBarrier{ObjectLayer: obj, bucket: bucket, reading: make(chan struct{}), release: make(chan struct{})}
	setObjectLayer(barrier)
	defer setObjectLayer(previous)
	release := sync.OnceFunc(func() { close(barrier.release) })
	defer release()
	done := make(chan error, 1)
	go func() { done <- reload() }()
	select {
	case <-barrier.reading:
	case <-time.After(10 * time.Second):
		t.Fatal("peer handler did not read metadata")
	}
	seed("new")
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	meta, err := globalBucketMetadataSys.Get(bucket)
	if err != nil {
		t.Fatal(err)
	}
	globalEventNotifier.RLock()
	rules := globalEventNotifier.bucketRulesMap[bucket].Clone()
	globalEventNotifier.RUnlock()
	if !reflect.DeepEqual(rules, meta.notificationConfig.ToRulesMap()) {
		t.Errorf("%s: stale peer reload replaced current notification rules", instanceType)
	}
	globalBucketTargetSys.RLock()
	targets := append([]madmin.BucketTarget(nil), globalBucketTargetSys.targetsMap[bucket]...)
	globalBucketTargetSys.RUnlock()
	if len(targets) != 1 || targets[0].Arn != "new" {
		t.Errorf("%s: stale peer reload replaced current replication targets: %+v", instanceType, targets)
	}
}
