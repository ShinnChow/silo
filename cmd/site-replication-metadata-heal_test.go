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
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio/internal/auth"
)

func bucketConfigTestData(bucket string) map[string][]byte {
	return map[string][]byte{
		bucketPolicyConfig:     []byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/*"}]}`, bucket)),
		bucketTaggingConfig:    []byte(`<Tagging><TagSet><Tag><Key>key</Key><Value>value</Value></Tag></TagSet></Tagging>`),
		bucketSSEConfig:        []byte(`<ServerSideEncryptionConfiguration><Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault></Rule></ServerSideEncryptionConfiguration>`),
		bucketQuotaConfigFile:  []byte(`{"quota":1024,"quotatype":"hard"}`),
		bucketVersioningConfig: enabledBucketVersioningConfig,
		objectLockConfig:       enabledBucketObjectLockConfig,
	}
}

// Construct independent wire fixtures, including timestamps hidden by legacy
// exporters, without going through the production event or state helpers.
func bucketConfigTestInfo(bucket, file string, data []byte, at, created time.Time) srBucketStatsSummary {
	info := madmin.SRBucketInfo{Bucket: bucket, CreatedAt: created}
	var payload *string
	if len(data) != 0 {
		encoded := base64.StdEncoding.EncodeToString(data)
		payload = &encoded
	}
	switch file {
	case bucketPolicyConfig:
		info.Policy, info.PolicyUpdatedAt = data, at
	case bucketTaggingConfig:
		info.Tags, info.TagConfigUpdatedAt = payload, at
	case bucketSSEConfig:
		info.SSEConfig, info.SSEConfigUpdatedAt = payload, at
	case bucketQuotaConfigFile:
		info.QuotaConfig, info.QuotaConfigUpdatedAt = payload, at
	case bucketVersioningConfig:
		info.Versioning, info.VersioningConfigUpdatedAt = payload, at
	case objectLockConfig:
		info.ObjectLockConfig, info.ObjectLockConfigUpdatedAt = payload, at
	}
	return srBucketStatsSummary{meta: srBucketMetaInfo{SRBucketInfo: info}}
}

func TestLatestBucketConfigCandidates(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for file, data := range bucketConfigTestData("bucket") {
		t.Run(file, func(t *testing.T) {
			info := srStatusInfo{Sites: map[string]madmin.PeerInfo{"a": {}, "b": {}, "c": {}}, BucketStats: map[string]map[string]srBucketStatsSummary{"bucket": {}}}
			bs := info.BucketStats["bucket"]
			baseline := bucketConfigTestInfo("bucket", file, nil, created, created)
			bs["a"], bs["b"], bs["c"] = baseline, baseline, baseline
			if _, found := latestBucketConfig("bucket", file, info); found {
				t.Fatal("empty baseline chosen as source")
			}
			// Unknown and unavailable IDs cannot contribute, even with future clocks.
			bs[""], bs["unknown"] = bucketConfigTestInfo("bucket", file, data, created.Add(10*time.Hour), created), bucketConfigTestInfo("bucket", file, data, created.Add(20*time.Hour), created)
			if _, found := latestBucketConfig("bucket", file, info); found {
				t.Fatal("unknown source chosen")
			}
			liveBaseline := bucketConfigTestInfo("bucket", file, data, created, created)
			real := bucketConfigTestInfo("bucket", file, data, created.Add(time.Hour), created)
			oldGeneration := bucketConfigTestInfo("bucket", file, data, created, created.Add(time.Second))
			states := []srBucketStatsSummary{liveBaseline, real, oldGeneration}
			for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
				bs["a"], bs["b"], bs["c"] = states[order[0]], states[order[1]], states[order[2]]
				winner, found := latestBucketConfig("bucket", file, info)
				if !found || !winner.at.Equal(created.Add(time.Hour)) || !bytes.Equal(winner.data, data) {
					t.Fatalf("permutation %v chose %+v found=%v", order, winner, found)
				}
			}
			bs["a"], bs["b"], bs["c"] = baseline, liveBaseline, baseline
			if winner, found := latestBucketConfig("bucket", file, info); !found || !bytes.Equal(winner.data, data) || winner.real {
				t.Fatal("historical baseline-live cannot initialize")
			}
			if !bucketConfigUpdateOnly(file) {
				// A late-created empty baseline cannot beat a real earlier write.
				bs["a"], bs["b"], bs["c"] = real, bucketConfigTestInfo("bucket", file, nil, created.Add(2*time.Hour), created.Add(2*time.Hour)), baseline
				if winner, found := latestBucketConfig("bucket", file, info); !found || len(winner.data) == 0 {
					t.Fatal("default became a deletion")
				}
				bs["c"] = bucketConfigTestInfo("bucket", file, nil, created.Add(time.Hour), created)
				if winner, found := latestBucketConfig("bucket", file, info); !found || len(winner.data) != 0 || !winner.real {
					t.Fatal("real tombstone lost equal-time tie")
				}
			}
		})
	}
}

func TestHealBucketConfigSourceAndQuiescence(t *testing.T) {
	ExecObjectLayerAPITest(ExecObjectLayerAPITestArgs{t: t, objAPITest: func(obj ObjectLayer, backend, bucket string, _ http.Handler, cred auth.Credentials, t *testing.T) {
		t.Run(backend, func(t *testing.T) {
			ctx := t.Context()
			events := recordBucketConfigPeer(t, cred)
			local := globalDeploymentID()
			created := UTCNow().Add(-time.Hour)
			oldAt, newAt := created.Add(time.Minute), created.Add(2*time.Minute)
			counter := &bucketConfigWriteCounter{ObjectLayer: obj}
			setObjectLayer(counter)
			defer setObjectLayer(obj)
			for file, data := range bucketConfigTestData(bucket) {
				t.Run(file, func(t *testing.T) {
					meta := newBucketMetadata(bucket)
					meta.Created = created
					meta.defaultTimestamps()
					value, at := replicatedBucketConfig(&meta, file)
					*value, *at = data, newAt
					if err := globalBucketMetadataSys.save(ctx, meta); err != nil {
						t.Fatal(err)
					}
					bs := map[string]srBucketStatsSummary{
						local:           bucketConfigTestInfo(bucket, file, data, newAt, created),
						"metadata-peer": bucketConfigTestInfo(bucket, file, data, oldAt, created),
						"":              {}, "unknown": bucketConfigTestInfo(bucket, file, nil, newAt.Add(time.Hour), created),
						// Known but absent from c.state.Peers, forcing getAdminClient failure.
						"broken": bucketConfigTestInfo(bucket, file, nil, created, created),
					}
					info := srStatusInfo{Sites: map[string]madmin.PeerInfo{local: {}, "metadata-peer": {}, "broken": {}}, BucketStats: map[string]map[string]srBucketStatsSummary{bucket: bs}}
					before := len(events())
					writes := counter.writes.Load()
					if err := globalSiteReplicationSys.healBucketConfig(ctx, bucket, file, info); err != nil {
						t.Fatal(err)
					}
					got := events()[before:]
					if len(got) != 1 || !got[0].UpdatedAt.Equal(newAt) {
						t.Fatalf("same-payload timestamp heal did not reach healthy peer: %+v", got)
					}
					if file == bucketTaggingConfig && got[0].Tags == nil {
						t.Fatal("tag event missing payload")
					}
					if counter.writes.Load() != writes {
						t.Fatal("matching local state was rewritten")
					}
					bs["metadata-peer"], bs["broken"] = bs[local], bs[local]
					if err := globalSiteReplicationSys.healBucketConfig(ctx, bucket, file, info); err != nil {
						t.Fatal(err)
					}
					if len(events()) != before+1 || counter.writes.Load() != writes {
						t.Fatal("stable second heal wrote or broadcast")
					}
					if bucketConfigUpdateOnly(file) {
						return
					}
					bs["metadata-peer"] = bucketConfigTestInfo(bucket, file, nil, newAt.Add(time.Minute), created)
					delete(bs, "broken")
					if err := globalSiteReplicationSys.healBucketConfig(ctx, bucket, file, info); err != nil {
						t.Fatal(err)
					}
					after, err := loadBucketMetadata(ctx, obj, bucket)
					if err != nil {
						t.Fatal(err)
					}
					value, at = replicatedBucketConfig(&after, file)
					if len(*value) != 0 || !at.Equal(newAt.Add(time.Minute)) {
						t.Fatalf("local heal lost deletion/source time: %s %v", *value, *at)
					}
					if file == bucketQuotaConfigFile {
						q, _, err := globalBucketMetadataSys.GetQuotaConfig(ctx, bucket)
						if err != nil || q.Quota != 0 {
							t.Fatalf("quota cache not cleared: %+v %v", q, err)
						}
					}
				})
			}
		})
	}})
}
