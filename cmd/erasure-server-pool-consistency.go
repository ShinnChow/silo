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
	"context"
	"maps"
	"sort"
	"strings"
	"time"

	madmin "github.com/minio/madmin-go/v3"
	"github.com/minio/minio/internal/bucket/lifecycle"
	xhttp "github.com/minio/minio/internal/http"
)

// objectPoolInfos reads the addressed version in every pool, including pools
// draining their contents. The caller must hold the pools-layer object lock.
// An unreadable pool may contain a newer version or metadata; it is not absence.
func (z *erasureServerPools) objectPoolInfos(ctx context.Context, bucket, object string, opts ObjectOptions) ([]PoolObjInfo, error) {
	opts.NoLock = true
	opts.CheckPrecondFn = nil
	var copies []PoolObjInfo
	for i, pool := range z.serverPools {
		oi, err := pool.GetObjectInfo(ctx, bucket, object, opts)
		if err == nil || (oi.DeleteMarker && (isErrObjectNotFound(err) || isErrMethodNotAllowed(err))) {
			copies = append(copies, PoolObjInfo{Index: i, ObjInfo: oi})
			continue
		}
		if !isErrObjectNotFound(err) && !isErrVersionNotFound(err) {
			return nil, err
		}
	}
	sort.Slice(copies, func(i, j int) bool {
		a, b := copies[i], copies[j]
		if a.ObjInfo.ModTime.Equal(b.ObjInfo.ModTime) {
			return a.Index < b.Index
		}
		return a.ObjInfo.ModTime.After(b.ObjInfo.ModTime)
	})
	if len(copies) == 0 {
		if opts.VersionID != "" {
			return nil, VersionNotFound{Bucket: bucket, Object: decodeDirObject(object), VersionID: opts.VersionID}
		}
		return nil, ObjectNotFound{Bucket: bucket, Object: decodeDirObject(object)}
	}
	return copies, nil
}

// Healing also commits object metadata. Both queued healing and drive healing
// must use the same namespace as pooled writes, rather than racing them under
// a destination set's independent namespace.
func (z *erasureServerPools) healObjectInPool(ctx context.Context, er *erasureObjects, bucket, object, versionID string, opts madmin.HealOpts) (madmin.HealResultItem, error) {
	if !z.SinglePool() && !opts.NoLock {
		lk := z.NewNSLock(bucket, object)
		lkctx, err := lk.GetLock(ctx, globalOperationTimeout)
		if err != nil {
			return madmin.HealResultItem{}, err
		}
		ctx = lkctx.Context()
		defer lk.Unlock(lkctx)
		opts.NoLock = true
	}
	return er.HealObject(ctx, bucket, object, versionID, opts)
}

// mergePoolLockState resolves each independently ordered field. An ordered
// removal is a value in its own right; an absent unordered field is not one.
func mergePoolLockState(copies []PoolObjInfo) objectLockState {
	state := storedObjectLockState(copies[0].ObjInfo.UserDefined)
	for _, copy := range copies[1:] {
		next := storedObjectLockState(copy.ObjInfo.UserDefined)
		retentionTime, _ := time.Parse(time.RFC3339Nano, next.retentionTimestamp)
		if state.retentionIsOlderThan(retentionTime) || (state.retentionTimestamp == "" && state.mode == "" && next.mode != "") {
			state.mode, state.retainUntil, state.retentionTimestamp = next.mode, next.retainUntil, next.retentionTimestamp
		}
		holdTime, _ := time.Parse(time.RFC3339Nano, next.legalHoldTimestamp)
		if state.legalHoldIsOlderThan(holdTime) || (state.legalHoldTimestamp == "" && state.legalHold == "" && next.legalHold != "") {
			state.legalHold, state.legalHoldTimestamp = next.legalHold, next.legalHoldTimestamp
		}
	}
	return state
}

func replaceObjectLockMetadata(metadata map[string]string, state objectLockState) {
	for _, key := range []string{
		strings.ToLower(xhttp.AmzObjectLockMode), strings.ToLower(xhttp.AmzObjectLockRetainUntilDate),
		strings.ToLower(xhttp.AmzObjectLockLegalHold),
		ReservedMetadataPrefixLower + ObjectLockRetentionTimestamp,
		ReservedMetadataPrefixLower + ObjectLockLegalHoldTimestamp,
	} {
		// Metadata updates use maps.Copy at the set layer. Empty values also
		// clear a previous value there, including timestamp-only removals.
		if _, exists := metadata[key]; exists {
			metadata[key] = ""
		}
	}
	state.restoreRetention(metadata)
	state.restoreLegalHold(metadata)
}

func mergedPoolObjectInfo(copies []PoolObjInfo) ObjectInfo {
	oi := copies[0].ObjInfo
	oi.UserDefined = maps.Clone(oi.UserDefined)
	if oi.UserDefined == nil {
		oi.UserDefined = make(map[string]string)
	}
	replaceObjectLockMetadata(oi.UserDefined, mergePoolLockState(copies))
	for _, copy := range copies[1:] {
		ts := copy.ObjInfo.UserDefined[ReservedMetadataPrefixLower+TaggingTimestamp]
		stamp, _ := time.Parse(time.RFC3339Nano, ts)
		if olderThan(oi.UserDefined[ReservedMetadataPrefixLower+TaggingTimestamp], stamp) {
			oi.UserDefined[ReservedMetadataPrefixLower+TaggingTimestamp] = ts
			oi.UserDefined[xhttp.AmzObjectTagging] = copy.ObjInfo.UserDefined[xhttp.AmzObjectTagging]
		}
	}
	oi.UserTags = oi.UserDefined[xhttp.AmzObjectTagging]
	return oi
}

func (z *erasureServerPools) replicaObjectInfo(ctx context.Context, bucket, object string, opts ObjectOptions) (ObjectInfo, error) {
	if opts.VersionID == "" {
		opts.VersionID = nullVersionID
	}
	copies, err := z.objectPoolInfos(ctx, bucket, object, opts)
	if err != nil {
		return ObjectInfo{}, err
	}
	if copies[0].ObjInfo.DeleteMarker {
		return copies[0].ObjInfo, MethodNotAllowed{Bucket: bucket, Object: decodeDirObject(object), VersionID: opts.VersionID}
	}
	return mergedPoolObjectInfo(copies), nil
}

// metadataPoolInfos resolves an unqualified metadata request to one logical
// version before collecting its copies, rather than mixing different versions.
func (z *erasureServerPools) metadataPoolInfos(ctx context.Context, bucket, object string, opts ObjectOptions) ([]PoolObjInfo, error) {
	copies, err := z.objectPoolInfos(ctx, bucket, object, opts)
	if err != nil {
		return nil, err
	}
	if copies[0].ObjInfo.DeleteMarker {
		return nil, MethodNotAllowed{Bucket: bucket, Object: decodeDirObject(object), VersionID: opts.VersionID}
	}
	if opts.VersionID == "" {
		opts.VersionID = copies[0].ObjInfo.VersionID
		if opts.VersionID == "" {
			opts.VersionID = nullVersionID
		}
		return z.objectPoolInfos(ctx, bucket, object, opts)
	}
	return copies, nil
}

func (z *erasureServerPools) updatePoolMetadata(ctx context.Context, bucket, object string, opts ObjectOptions) (ObjectInfo, error) {
	copies, err := z.metadataPoolInfos(ctx, bucket, object, opts)
	if err != nil {
		return ObjectInfo{}, err
	}
	updated := mergedPoolObjectInfo(copies)
	before := maps.Clone(updated.UserDefined)
	if opts.EvalMetadataFn != nil {
		if _, err := opts.EvalMetadataFn(&updated, nil); err != nil {
			return ObjectInfo{}, err
		}
	}
	changes := make(map[string]string)
	for key, value := range updated.UserDefined {
		if prior, ok := before[key]; !ok || prior != value {
			changes[key] = value
		}
	}
	for key := range before {
		if _, ok := updated.UserDefined[key]; !ok {
			changes[key] = ""
		}
	}
	state := storedObjectLockState(updated.UserDefined)
	opts.VersionID = updated.VersionID
	if opts.VersionID == "" {
		opts.VersionID = nullVersionID
	}
	opts.NoLock = true
	opts.EvalMetadataFn = func(oi *ObjectInfo, _ error) (ReplicateDecision, error) {
		maps.Copy(oi.UserDefined, changes)
		replaceObjectLockMetadata(oi.UserDefined, state)
		for _, key := range []string{xhttp.AmzObjectTagging, ReservedMetadataPrefixLower + TaggingTimestamp} {
			value, exists := updated.UserDefined[key]
			if exists || oi.UserDefined[key] != "" {
				oi.UserDefined[key] = value
			}
		}
		return ReplicateDecision{}, nil
	}
	var primary ObjectInfo
	for _, copy := range copies {
		oi, err := z.serverPools[copy.Index].PutObjectMetadata(ctx, bucket, object, opts)
		if err != nil {
			return ObjectInfo{}, err
		}
		if copy.Index == copies[0].Index {
			primary = oi
		}
	}
	return primary, nil
}

func reconcileStoredObjectTags(metadata, stored map[string]string) {
	key := ReservedMetadataPrefixLower + TaggingTimestamp
	stamp, err := time.Parse(time.RFC3339Nano, stored[key])
	if err != nil {
		return
	}
	incoming, err := time.Parse(time.RFC3339Nano, metadata[key])
	if err != nil || !stamp.Before(incoming) {
		metadata[key] = stored[key]
		metadata[xhttp.AmzObjectTagging] = stored[xhttp.AmzObjectTagging]
	}
}

// A restored version still owns its tier reference even while IsRemote is
// false. Only the last copy of a reference may schedule its contents for GC.
func sharesTierObject(oi ObjectInfo, copies []PoolObjInfo) bool {
	ref := oi.TransitionedObject
	if ref.Status != lifecycle.TransitionComplete {
		return false
	}
	for _, copy := range copies {
		other := copy.ObjInfo.TransitionedObject
		if other.Status == lifecycle.TransitionComplete && ref.Tier == other.Tier && ref.Name == other.Name && ref.VersionID == other.VersionID {
			return true
		}
	}
	return false
}

// retireReplicaCopies runs only after committing a replacement. Failures are
// returned to the caller, so a stale copy cannot be hidden behind a successful
// response. Data movement owns its source cleanup and does not use this helper.
func (z *erasureServerPools) retireReplicaCopies(ctx context.Context, bucket, object string, keep int, oi ObjectInfo) error {
	versionID := oi.VersionID
	if versionID == "" {
		versionID = nullVersionID
	}
	copies, err := z.objectPoolInfos(ctx, bucket, object, ObjectOptions{VersionID: versionID})
	if err != nil {
		return err
	}
	var retained []PoolObjInfo
	for _, copy := range copies {
		if copy.Index == keep {
			retained = []PoolObjInfo{copy}
			break
		}
	}
	if len(retained) == 0 {
		return VersionNotFound{Bucket: bucket, Object: decodeDirObject(object), VersionID: versionID}
	}
	for i, copy := range copies {
		if copy.Index == keep {
			continue
		}
		_, err := z.serverPools[copy.Index].DeleteObject(ctx, bucket, object,
			ObjectOptions{
				VersionID: versionID, NoLock: true, NoAuditLog: true,
				SkipFreeVersion: sharesTierObject(copy.ObjInfo, retained) || sharesTierObject(copy.ObjInfo, copies[i+1:]),
			})
		if err != nil && !isErrObjectNotFound(err) && !isErrVersionNotFound(err) {
			return err
		}
	}
	return nil
}

// deleteObjectConditional evaluates the condition once against the logical
// version, then removes all its copies under the same lock as pooled writers.
func (z *erasureServerPools) deleteObjectConditional(ctx context.Context, bucket, object string, opts ObjectOptions) (ObjectInfo, error) {
	copies, err := z.objectPoolInfos(ctx, bucket, object, opts)
	if err != nil {
		return ObjectInfo{}, err
	}
	primary := copies[0]
	if opts.CheckPrecondFn(primary.ObjInfo) {
		return ObjectInfo{}, PreConditionFailed{}
	}
	opts.CheckPrecondFn = nil
	opts.NoLock = true
	if opts.EvalRetentionBypassFn != nil || opts.EvalMetadataFn != nil {
		versions, err := z.metadataPoolInfos(ctx, bucket, object, opts)
		if err != nil {
			return ObjectInfo{}, err
		}
		logical := mergedPoolObjectInfo(versions)
		if opts.EvalRetentionBypassFn != nil {
			if err := opts.EvalRetentionBypassFn(logical, nil); err != nil {
				return ObjectInfo{}, err
			}
			opts.EvalRetentionBypassFn = nil
		}
		if opts.EvalMetadataFn != nil {
			decision, err := opts.EvalMetadataFn(&logical, nil)
			if err != nil {
				return ObjectInfo{}, err
			}
			if decision.ReplicateAny() {
				opts.SetDeleteReplicationState(decision, opts.VersionID)
			}
			opts.EvalMetadataFn = nil
		}
	}
	if opts.VersionID == "" && (opts.Versioned || opts.VersionSuspended) {
		// A single new delete marker hides the current version. Older versions
		// remain history and must not be removed by an unqualified DELETE.
		oi, err := z.serverPools[primary.Index].DeleteObject(ctx, bucket, object, opts)
		oi.Name = decodeDirObject(object)
		oi.replicationDecision = opts.DeleteReplication.ReplicateDecisionStr
		return oi, err
	}
	// Retire non-authoritative copies first. If cleanup fails, retain the
	// authoritative version and report the error instead of acknowledging a
	// deletion that would expose an older copy.
	for i := 1; i < len(copies); i++ {
		candidate := copies[i]
		deleteOpts := opts
		deleteOpts.SkipFreeVersion = opts.SkipFreeVersion || sharesTierObject(candidate.ObjInfo, copies[:1]) || sharesTierObject(candidate.ObjInfo, copies[i+1:])
		_, err := z.serverPools[candidate.Index].DeleteObject(ctx, bucket, object, deleteOpts)
		if err != nil && !isErrObjectNotFound(err) && !isErrVersionNotFound(err) {
			return ObjectInfo{}, err
		}
	}
	oi, err := z.serverPools[primary.Index].DeleteObject(ctx, bucket, object, opts)
	oi.Name = decodeDirObject(object)
	oi.replicationDecision = opts.DeleteReplication.ReplicateDecisionStr
	return oi, err
}
