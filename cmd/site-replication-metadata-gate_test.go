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
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio/internal/auth"
)

func TestBucketMetadataTombstoneExportAndInitialSync(t *testing.T) {
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{t: t, objAPITest: func(obj ObjectLayer, backend, bucket string, _ http.Handler, cred auth.Credentials, t *testing.T) {
		t.Run(backend, func(t *testing.T) {
			recordBucketConfigPeer(t, cred)
			old := globalSiteReplicationMetadataTombstones
			defer func() { globalSiteReplicationMetadataTombstones = old }()
			created := UTCNow().Add(-time.Hour)
			deletedAt := created.Add(time.Minute)
			for _, enabled := range []bool{false, true} {
				globalSiteReplicationMetadataTombstones = enabled
				meta := newBucketMetadata(bucket)
				meta.Created = created
				meta.defaultTimestamps()
				for _, file := range []string{bucketPolicyConfig, bucketTaggingConfig, bucketSSEConfig, bucketQuotaConfigFile} {
					_, at := replicatedBucketConfig(&meta, file)
					*at = deletedAt
				}
				if err := globalBucketMetadataSys.save(t.Context(), meta); err != nil {
					t.Fatal(err)
				}
				// Force a disk reload instead of accepting the just-published cache.
				globalBucketMetadataSys.Remove(bucket)
				info, err := globalSiteReplicationSys.SiteReplicationMetaInfo(t.Context(), obj, madmin.SRStatusOptions{Buckets: true})
				if err != nil {
					t.Fatal(err)
				}
				exported := info.Buckets[bucket]
				if !exported.PolicyUpdatedAt.Equal(deletedAt) {
					t.Fatal("Policy tombstone hidden by gate")
				}
				for _, at := range []time.Time{exported.TagConfigUpdatedAt, exported.SSEConfigUpdatedAt, exported.QuotaConfigUpdatedAt} {
					if enabled && !at.Equal(deletedAt) || !enabled && !at.IsZero() {
						t.Fatalf("gate=%v timestamp=%v", enabled, at)
					}
				}
				for file, data := range bucketConfigTestData(bucket) {
					event, send, err := initialBucketConfigReplicationEvent(meta, file)
					wantSend := enabled && !bucketConfigUpdateOnly(file)
					if err != nil || send != wantSend || send && !event.UpdatedAt.Equal(deletedAt) {
						t.Fatalf("%s initial gate=%v: %+v %v %v", file, enabled, event, send, err)
					}
					baseline := newBucketMetadata(bucket)
					baseline.Created = created
					baseline.defaultTimestamps()
					if _, send, err := initialBucketConfigReplicationEvent(baseline, file); err != nil || send {
						t.Fatalf("empty baseline sent: %s", file)
					}
					value, _ := replicatedBucketConfig(&baseline, file)
					*value = data
					event, send, err = initialBucketConfigReplicationEvent(baseline, file)
					if err != nil || !send || !event.UpdatedAt.Equal(created) {
						t.Fatalf("historical baseline-live omitted: %s %v", file, err)
					}
				}
			}
		})
	}})
}

func TestPeerBucketMetadataLegacyAndGeneration(t *testing.T) {
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{t: t, objAPITest: func(obj ObjectLayer, backend, bucket string, _ http.Handler, cred auth.Credentials, t *testing.T) {
		t.Run(backend, func(t *testing.T) {
			old := globalSiteReplicationMetadataTombstones
			defer func() { globalSiteReplicationMetadataTombstones = old }()
			created := UTCNow().Add(-time.Hour)
			for _, enabled := range []bool{false, true} {
				globalSiteReplicationMetadataTombstones = enabled
				for file, data := range bucketConfigTestData(bucket) {
					meta := newBucketMetadata(bucket)
					meta.Created = created
					meta.defaultTimestamps()
					if err := globalBucketMetadataSys.save(t.Context(), meta); err != nil {
						t.Fatal(err)
					}
					counter := &bucketConfigWriteCounter{ObjectLayer: obj}
					setObjectLayer(counter)
					event := newBucketConfigReplicationEvent(bucket, file, bucketConfigState{data: data, at: created.Add(-time.Second)})
					rec := applySRBucketMetaViaAdmin(t, cred, event)
					if rec.Code != http.StatusOK || counter.writes.Load() != 0 {
						t.Fatalf("pre-creation apply: %s %d writes=%d", file, rec.Code, counter.writes.Load())
					}
					// A live baseline may initialize. The same-time nil cannot delete it.
					event.UpdatedAt = created
					rec = applySRBucketMetaViaAdmin(t, cred, event)
					if rec.Code != http.StatusOK {
						t.Fatalf("baseline apply: %s %s", file, rec.Body.String())
					}
					before := counter.writes.Load()
					rec = applySRBucketMetaViaAdmin(t, cred, madmin.SRBucketMeta{Type: event.Type, Bucket: bucket, UpdatedAt: created})
					if rec.Code != http.StatusOK || counter.writes.Load() != before {
						t.Fatalf("nil baseline cleared configuration: %s", file)
					}
					event.UpdatedAt = time.Time{}
					for range 2 {
						rec = applySRBucketMetaViaAdmin(t, cred, event)
						if rec.Code != http.StatusOK {
							t.Fatalf("legacy-zero rejected: %s %s", file, rec.Body.String())
						}
					}
					meta, err := loadBucketMetadata(t.Context(), obj, bucket)
					if err != nil {
						t.Fatal(err)
					}
					value, at := replicatedBucketConfig(&meta, file)
					if len(*value) == 0 || !at.After(created) {
						t.Fatalf("legacy-zero not reclocked: %s %v", file, *at)
					}
					setObjectLayer(obj)
				}
			}
		})
	}})
}

type bucketMetadataCreatedObjectLayer struct {
	ObjectLayer
	created time.Time
	missing bool
}

func (o bucketMetadataCreatedObjectLayer) GetBucketInfo(ctx context.Context, bucket string, opts BucketOptions) (BucketInfo, error) {
	if opts.NoMetadata {
		if o.missing {
			return BucketInfo{}, BucketNotFound{Bucket: bucket}
		}
		return BucketInfo{Name: bucket, Created: o.created}, nil
	}
	return o.ObjectLayer.GetBucketInfo(ctx, bucket, opts)
}

func TestPeerBucketMetadataUnknownCreated(t *testing.T) {
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{t: t, objAPITest: func(obj ObjectLayer, backend, bucket string, _ http.Handler, _ auth.Credentials, t *testing.T) {
		t.Run(backend, func(t *testing.T) {
			defer setObjectLayer(obj)
			data := bucketConfigTestData(bucket)[bucketTaggingConfig]
			for _, mode := range []string{"unknown", "missing", "physical-created"} {
				t.Run(mode, func(t *testing.T) {
					meta := newBucketMetadata(bucket)
					setObjectLayer(obj)
					if err := globalBucketMetadataSys.save(t.Context(), meta); err != nil {
						t.Fatal(err)
					}
					created := UTCNow().Add(-time.Hour)
					physical := bucketMetadataCreatedObjectLayer{ObjectLayer: obj, missing: mode == "missing"}
					if mode == "physical-created" {
						physical.created = created
					}
					counter := &bucketConfigWriteCounter{ObjectLayer: physical}
					setObjectLayer(counter)
					stamp := created.Add(time.Minute)
					_, err := globalBucketMetadataSys.updateAndParseMetadata(t.Context(), bucket, bucketTaggingConfig, data, false, false, &stamp)
					if mode != "physical-created" {
						if err == nil || counter.writes.Load() != 0 {
							t.Fatalf("unknown generation was invented: %v writes=%d", err, counter.writes.Load())
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						got, err := readBucketMetadata(t.Context(), obj, bucket)
						if err != nil || !got.Created.Equal(created) || !got.TaggingConfigUpdatedAt.Equal(stamp) || !bytes.Equal(got.TaggingConfigXML, data) {
							t.Fatalf("physical creation recovery: %v", err)
						}
					}
				})
			}
		})
	}})
}
