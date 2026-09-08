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
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/minio/minio/internal/bucket/lifecycle"
	"github.com/minio/minio/internal/config/ilm"
	"github.com/minio/minio/internal/dsync"
	"github.com/minio/minio/internal/hash"
)

const (
	accessTierMetadataKey = ReservedMetadataPrefixLower + "ilm-atier"
	accessTierQueueSize   = 100000
)

var (
	errAccessTierNotEligible    = errors.New("object is no longer eligible for access tiering")
	errAccessTierTooManyVersion = errors.New("object has too many versions for access tiering")
	errAccessTierRemoteVersion  = errors.New("access tiering does not move remotely transitioned versions")
)

type accessTierDirection uint8

const (
	accessTierPromote accessTierDirection = iota + 1
	accessTierDemote
)

type accessTierTask struct {
	ctx       context.Context
	bucket    string
	object    string
	src       int
	dst       int
	direction accessTierDirection
	bytes     uint64 // promotion reservation; zero for demotion
}

// accessTierState owns the leader-only mover queues and the usage deltas made
// since the data scanner's last published snapshot.
type accessTierState struct {
	ctx    context.Context
	objAPI ObjectLayer
	z      *erasureServerPools

	promoteCh chan accessTierTask
	demoteCh  chan accessTierTask

	mu              sync.Mutex
	numWorkers      int
	workerStops     []chan struct{}
	pending         map[string]struct{}
	baseUsage       map[string]uint64
	baseTotal       uint64
	delta           map[string]uint64
	deltaCycle      uint32
	deltaCycleValid bool
	reserved        map[string]uint64
	usageAt         time.Time
	usageCycle      uint32
	usageCycleSeen  bool
	usageReady      bool

	active           atomic.Int64
	promotions       atomic.Uint64
	demotions        atomic.Uint64
	bytesMoved       atomic.Uint64
	failures         atomic.Uint64
	skippedWatermark atomic.Uint64
	skippedMaxSize   atomic.Uint64
	skippedQuota     atomic.Uint64
}

var globalAccessTierState *accessTierState

func newAccessTierState(ctx context.Context) *accessTierState {
	return &accessTierState{
		ctx:       ctx,
		promoteCh: make(chan accessTierTask, accessTierQueueSize),
		demoteCh:  make(chan accessTierTask, accessTierQueueSize),
		pending:   make(map[string]struct{}),
		baseUsage: make(map[string]uint64),
		delta:     make(map[string]uint64),
		reserved:  make(map[string]uint64),
	}
}

func (s *accessTierState) Init(objAPI ObjectLayer) {
	if s == nil {
		return
	}
	z, ok := objAPI.(*erasureServerPools)
	if !ok {
		return
	}

	s.mu.Lock()
	s.objAPI = objAPI
	s.z = z
	s.updateWorkersLocked(globalILMConfig.accessCfg().AccessWorkers)
	s.mu.Unlock()

	go s.runLeader()
}

func (s *accessTierState) UpdateWorkers(n int) {
	if s == nil || n < 1 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objAPI == nil {
		return
	}
	s.updateWorkersLocked(n)
}

func (s *accessTierState) updateWorkersLocked(n int) {
	for s.numWorkers < n {
		stop := make(chan struct{})
		s.workerStops = append(s.workerStops, stop)
		go s.worker(stop)
		s.numWorkers++
	}
	for s.numWorkers > n {
		last := len(s.workerStops) - 1
		close(s.workerStops[last])
		s.workerStops = s.workerStops[:last]
		s.numWorkers--
	}
}

func (s *accessTierState) PendingTasks() int {
	if s == nil {
		return 0
	}
	return len(s.promoteCh) + len(s.demoteCh)
}

func (s *accessTierState) ActiveTasks() int64 {
	if s == nil {
		return 0
	}
	return s.active.Load()
}

func accessTierPendingKey(bucket, object string) string {
	return accessKey(bucket, object)
}

func (s *accessTierState) enqueueDemote(task accessTierTask) bool {
	key := accessTierPendingKey(task.bucket, task.object)
	s.mu.Lock()
	if _, exists := s.pending[key]; exists {
		s.mu.Unlock()
		return false
	}
	s.pending[key] = struct{}{}
	s.mu.Unlock()

	select {
	case <-task.ctx.Done():
		s.releasePending(task, false)
		return false
	case s.demoteCh <- task:
		return true
	default:
		s.releasePending(task, false)
		return false
	}
}

func addUsage(a, b uint64) uint64 {
	if ^uint64(0)-a < b {
		return ^uint64(0)
	}
	return a + b
}

func (s *accessTierState) bucketUsageLocked(bucket string) uint64 {
	return addUsage(addUsage(s.baseUsage[bucket], s.delta[bucket]), s.reserved[bucket])
}

func (s *accessTierState) totalUsageLocked() uint64 {
	var delta uint64
	var reserved uint64
	for _, v := range s.delta {
		delta = addUsage(delta, v)
	}
	for _, v := range s.reserved {
		reserved = addUsage(reserved, v)
	}
	return addUsage(addUsage(s.baseTotal, delta), reserved)
}

// reservePromotion makes all queued promotions participate in quota checks,
// so one sweep cannot admit many objects against the same stale scanner value.
func (s *accessTierState) reservePromotion(task accessTierTask, cfg ilm.Config, quota uint64) (string, bool) {
	key := accessTierPendingKey(task.bucket, task.object)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.pending[key]; exists {
		return "", false
	}
	if !s.usageReady {
		switch {
		case cfg.AccessMaxSize > 0:
			return "max-size", false
		case quota > 0:
			return "quota", false
		}
	}
	if cfg.AccessMaxSize > 0 && addUsage(s.totalUsageLocked(), task.bytes) > cfg.AccessMaxSize {
		return "max-size", false
	}
	if quota > 0 && addUsage(s.bucketUsageLocked(task.bucket), task.bytes) > quota {
		return "quota", false
	}
	s.pending[key] = struct{}{}
	s.reserved[task.bucket] = addUsage(s.reserved[task.bucket], task.bytes)
	return "", true
}

func (s *accessTierState) replaceReservation(bucket string, old, current uint64, cfg ilm.Config, quota uint64) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reserved := s.reserved[bucket]
	if old >= reserved {
		reserved = 0
	} else {
		reserved -= old
	}
	s.reserved[bucket] = reserved
	if cfg.AccessMaxSize > 0 && addUsage(s.totalUsageLocked(), current) > cfg.AccessMaxSize {
		s.reserved[bucket] = addUsage(s.reserved[bucket], old)
		return "max-size", false
	}
	if quota > 0 && addUsage(s.bucketUsageLocked(bucket), current) > quota {
		s.reserved[bucket] = addUsage(s.reserved[bucket], old)
		return "quota", false
	}
	s.reserved[bucket] = addUsage(s.reserved[bucket], current)
	return "", true
}

func (s *accessTierState) enqueuePromote(task accessTierTask, cfg ilm.Config, quota uint64) bool {
	reason, ok := s.reservePromotion(task, cfg, quota)
	if !ok {
		s.recordSkip(reason)
		return false
	}
	select {
	case <-task.ctx.Done():
		s.releasePending(task, true)
		return false
	case s.promoteCh <- task:
		return true
	default:
		s.releasePending(task, true)
		return false
	}
}

func (s *accessTierState) releasePending(task accessTierTask, releaseReservation bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, accessTierPendingKey(task.bucket, task.object))
	if releaseReservation {
		if task.bytes >= s.reserved[task.bucket] {
			delete(s.reserved, task.bucket)
		} else {
			s.reserved[task.bucket] -= task.bytes
		}
	}
}

func (s *accessTierState) completeTask(task accessTierTask, moved uint64) {
	s.mu.Lock()
	delete(s.pending, accessTierPendingKey(task.bucket, task.object))
	if task.direction == accessTierPromote {
		if task.bytes >= s.reserved[task.bucket] {
			delete(s.reserved, task.bucket)
		} else {
			s.reserved[task.bucket] -= task.bytes
		}
		s.delta[task.bucket] = addUsage(s.delta[task.bucket], moved)
		if s.usageCycleSeen {
			s.deltaCycle = s.usageCycle
			s.deltaCycleValid = true
		}
	}
	s.mu.Unlock()

	if task.direction == accessTierPromote {
		s.promotions.Add(1)
	} else {
		s.demotions.Add(1)
	}
	s.bytesMoved.Add(moved)
}

func (s *accessTierState) recordSkip(reason string) {
	switch reason {
	case "watermark":
		s.skippedWatermark.Add(1)
	case "max-size":
		s.skippedMaxSize.Add(1)
	case "quota":
		s.skippedQuota.Add(1)
	}
}

func (s *accessTierState) nextTask(stop <-chan struct{}) (accessTierTask, bool) {
	// Prefer demotion whenever one is already waiting, without starving
	// promotions when the demotion queue is empty.
	select {
	case <-stop:
		return accessTierTask{}, false
	default:
	}
	select {
	case task := <-s.demoteCh:
		return task, true
	default:
	}
	select {
	case <-stop:
		return accessTierTask{}, false
	case <-s.ctx.Done():
		return accessTierTask{}, false
	case task := <-s.demoteCh:
		return task, true
	case task := <-s.promoteCh:
		return task, true
	}
}

func (s *accessTierState) worker(stop <-chan struct{}) {
	for {
		task, ok := s.nextTask(stop)
		if !ok {
			return
		}
		s.active.Add(1)
		err := s.processTask(task)
		s.active.Add(-1)
		if err != nil {
			if !errors.Is(err, errAccessTierNotEligible) && !isErrObjectNotFound(err) && !isErrVersionNotFound(err) && !errors.Is(err, context.Canceled) {
				s.failures.Add(1)
				ilmLogIf(task.ctx, fmt.Errorf("access tier move failed for %s/%s: %w", task.bucket, task.object, err))
			}
		}
	}
}

func (s *accessTierState) runLeader() {
	for s.ctx.Err() == nil {
		leaderCtx, cancel := globalLeaderLock.GetLock(s.ctx)
		s.runSweeps(leaderCtx)
		cancel()
	}
}

func (s *accessTierState) runSweeps(ctx context.Context) {
	cfg := globalILMConfig.accessCfg()
	interval := cfg.AccessFlush
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newCfg := globalILMConfig.accessCfg()
			if newCfg.AccessFlush > 0 && newCfg.AccessFlush != interval {
				interval = newCfg.AccessFlush
				ticker.Reset(interval)
			}
			cfg = newCfg
			if !cfg.AccessTiering {
				continue
			}
			if !validAccessTopology(s.z, cfg) {
				ilmLogOnceIf(ctx, fmt.Errorf("access tiering needs at least two existing server pools; configured pools are %v", cfg.AccessPools), "access-tier-topology")
				continue
			}
			if s.z.IsRebalanceStarted(ctx) || s.z.IsDecommissionRunning() {
				continue
			}
			s.refreshUsage(ctx)
			s.sweepDemotions(ctx, cfg)
			s.sweepPromotions(ctx, cfg)
		}
	}
}

func validAccessTopology(z *erasureServerPools, cfg ilm.Config) bool {
	if z == nil || len(cfg.AccessPools) < 2 || len(z.serverPools) < 2 {
		return false
	}
	for _, idx := range cfg.AccessPools {
		if idx < 0 || idx >= len(z.serverPools) {
			return false
		}
	}
	return true
}

func (s *accessTierState) refreshUsage(ctx context.Context) {
	dui, err := loadDataUsageFromBackend(ctx, s.objAPI)
	if err != nil || dui.LastUpdate.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !dui.LastUpdate.After(s.usageAt) {
		return
	}
	cycleAdvanced := s.usageCycleSeen && dui.ScannerCycle != s.usageCycle
	s.baseUsage = make(map[string]uint64, len(dui.BucketsUsage))
	s.baseTotal = 0
	for bucket, usage := range dui.BucketsUsage {
		// Only retain buckets that actually hold hot-tier bytes. Keeping a
		// zero entry per bucket would emit one Prometheus series per bucket
		// in the cluster from hotUsageSnapshot.
		if usage.HotTierSize == 0 {
			continue
		}
		s.baseUsage[bucket] = usage.HotTierSize
		s.baseTotal = addUsage(s.baseTotal, usage.HotTierSize)
	}
	if !s.usageCycleSeen {
		s.usageCycleSeen = true
		s.usageCycle = dui.ScannerCycle
		if len(s.delta) > 0 && !s.deltaCycleValid {
			s.deltaCycle = dui.ScannerCycle
			s.deltaCycleValid = true
		}
	} else if cycleAdvanced {
		s.usageCycle = dui.ScannerCycle
		// A move can finish while the next scanner pass is already in
		// progress. Retain promotion deltas until one additional complete
		// pass has elapsed, so quotas can over-count temporarily but never
		// under-count. Demotions rely only on the scanner to release space.
		if s.deltaCycleValid && scannerCyclesApart(dui.ScannerCycle, s.deltaCycle) >= 2 {
			clear(s.delta)
			s.deltaCycleValid = false
		}
		s.usageReady = true
	}
	s.usageAt = dui.LastUpdate
}

func scannerCyclesApart(current, previous uint32) uint32 {
	distance := current - previous
	if distance >= 1<<31 {
		return 0
	}
	return distance
}

func (s *accessTierState) hotUsageSnapshot() map[string]uint64 {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]uint64, len(s.baseUsage)+len(s.delta)+len(s.reserved))
	for _, m := range []map[string]uint64{s.baseUsage, s.delta, s.reserved} {
		for bucket := range m {
			if used := s.bucketUsageLocked(bucket); used > 0 {
				out[bucket] = used
			}
		}
	}
	return out
}

func (s *accessTierState) sweepDemotions(ctx context.Context, cfg ilm.Config) {
	for _, candidate := range globalAccessTracker.collectDemoteCandidates(ctx, s.objAPI, cfg) {
		if candidate.Bucket == minioMetaBucket {
			continue
		}
		pinfo, _, err := s.z.getPoolInfoExistingWithOpts(ctx, candidate.Bucket, candidate.Object, ObjectOptions{
			SkipDecommissioned: true,
			SkipRebalancing:    true,
		})
		if err != nil || pinfo.Index != candidate.Pool {
			continue
		}
		cold, _ := cfg.ColdPool()
		if pinfo.Index == cold || !accessPoolIsHotter(cfg, pinfo.Index, cold) {
			continue
		}
		lc, err := globalLifecycleSys.Get(candidate.Bucket)
		if err != nil || !accessDemoteEligible(lc, pinfo.ObjInfo, pinfo.Index, cfg, time.Now()) {
			continue
		}
		s.enqueueDemote(accessTierTask{
			ctx: ctx, bucket: candidate.Bucket, object: candidate.Object,
			src: pinfo.Index, dst: cold, direction: accessTierDemote,
		})
	}
}

func (s *accessTierState) sweepPromotions(ctx context.Context, cfg ilm.Config) {
	snapshot := globalAccessTracker.snapshot()
	if snapshot == nil {
		return
	}
	hot, _ := cfg.HotPool()
	lifecycles := make(map[string]*lifecycle.Lifecycle)
	missing := make(map[string]struct{})
	for key, entry := range snapshot.entries {
		bucket, object, ok := splitAccessKey(key)
		if !ok || bucket == minioMetaBucket {
			continue
		}
		lc := lifecycles[bucket]
		if lc == nil {
			if _, skip := missing[bucket]; skip {
				continue
			}
			var err error
			lc, err = globalLifecycleSys.Get(bucket)
			if err != nil || !lc.HasAccessTransition() {
				missing[bucket] = struct{}{}
				continue
			}
			lifecycles[bucket] = lc
		}

		if !accessHitsMightPromote(lc, object, entry, snapshot.binWidth) {
			continue
		}

		pinfo, _, err := s.z.getPoolInfoExistingWithOpts(ctx, bucket, object, ObjectOptions{
			SkipDecommissioned: true,
			SkipRebalancing:    true,
		})
		if err != nil || pinfo.Index == hot || pinfo.ObjInfo.DeleteMarker || pinfo.ObjInfo.IsDir || pinfo.ObjInfo.IsRemote() {
			continue
		}
		rule, _, ok := lc.AccessRule(pinfo.ObjInfo.ToLifecycleOpts())
		if !ok || entry.hits(rule.Window.D(), snapshot.binWidth) < uint64(rule.PromoteAfterAccesses) {
			continue
		}
		if rule.Window.D() > cfg.HistoryWindow() {
			ilmLogOnceIf(ctx, fmt.Errorf("access tier rule window %s for %s/%s exceeds retained history %s and is clamped", rule.Window.D(), bucket, object, cfg.HistoryWindow()), "access-window-"+bucket)
		}
		bytes, err := accessObjectBytes(ctx, s.z, pinfo.Index, bucket, object)
		if err != nil {
			continue
		}
		if !s.promotionWatermarkOK(ctx, cfg, hot, bucket, object, bytes) {
			s.recordSkip("watermark")
			continue
		}
		s.enqueuePromote(accessTierTask{
			ctx: ctx, bucket: bucket, object: object, src: pinfo.Index, dst: hot,
			direction: accessTierPromote, bytes: bytes,
		}, cfg, lc.AccessQuotaBytes())
	}
}

// accessHitsMightPromote is the cheap prefilter for sweepPromotions: prefix
// plus hit count, without a quorum metadata read. Size and tag filters are
// applied later via AccessRule once object info is in hand.
func accessHitsMightPromote(lc *lifecycle.Lifecycle, object string, entry accessEntry, binWidth int64) bool {
	if lc == nil {
		return false
	}
	for _, rule := range lc.Rules {
		if rule.Status == lifecycle.Disabled || rule.AccessTransition.IsNull() {
			continue
		}
		prefix := rule.GetPrefix()
		if prefix != "" && !strings.HasPrefix(object, prefix) {
			continue
		}
		at := rule.AccessTransition
		if entry.hits(at.Window.D(), binWidth) >= uint64(at.PromoteAfterAccesses) {
			return true
		}
	}
	return false
}

func (s *accessTierState) promotionWatermarkOK(ctx context.Context, cfg ilm.Config, hot int, bucket, object string, bytes uint64) bool {
	if bytes > ^uint64(0)>>1 {
		return false
	}
	spaces := s.z.getServerPoolsAvailableSpace(ctx, bucket, object, int64(bytes))
	if hot < 0 || hot >= len(spaces) {
		return false
	}
	space := spaces[hot]
	return space.Available > 0 && space.MaxUsedPct < cfg.AccessPromoteWatermark
}

func accessPoolIsHotter(cfg ilm.Config, src, dst int) bool {
	srcPos, dstPos := -1, -1
	for pos, pool := range cfg.AccessPools {
		if pool == src {
			srcPos = pos
		}
		if pool == dst {
			dstPos = pos
		}
	}
	return srcPos >= 0 && dstPos >= 0 && srcPos < dstPos
}

// accessTierStamp renders the marker written to every version a move touches.
// parseAccessTierStamp is its inverse; keep the two together.
func accessTierStamp(pool int, movedAt int64) string {
	return strconv.Itoa(pool) + ":" + strconv.FormatInt(movedAt, 10)
}

func parseAccessTierStamp(meta map[string]string) (pool int, movedAt time.Time, ok bool) {
	value, ok := meta[accessTierMetadataKey]
	if !ok {
		return 0, time.Time{}, false
	}
	poolText, stampText, ok := strings.Cut(value, ":")
	if !ok {
		return 0, time.Time{}, false
	}
	pool, err := strconv.Atoi(poolText)
	if err != nil {
		return 0, time.Time{}, false
	}
	stamp, err := strconv.ParseInt(stampText, 10, 64)
	if err != nil || stamp <= 0 {
		return 0, time.Time{}, false
	}
	return pool, time.Unix(0, stamp), true
}

func accessDemoteEligible(lc *lifecycle.Lifecycle, oi ObjectInfo, pool int, cfg ilm.Config, now time.Time) bool {
	if lc == nil || oi.DeleteMarker || oi.IsDir || oi.IsRemote() {
		return false
	}
	markedPool, movedAt, ok := parseAccessTierStamp(oi.UserDefined)
	if !ok || markedPool != pool || now.Sub(movedAt) < cfg.AccessMinResidency {
		return false
	}
	cold, ok := cfg.ColdPool()
	if !ok || !accessPoolIsHotter(cfg, pool, cold) {
		return false
	}
	rule, _, ok := lc.AccessRule(oi.ToLifecycleOpts())
	if !ok {
		return false
	}
	last := globalAccessTracker.lastAccess(oi.Bucket, oi.Name)
	if !last.IsZero() && now.Sub(last) < rule.DemoteAfterIdle.D() {
		return false
	}
	hits := globalAccessTracker.hits(oi.Bucket, oi.Name, rule.Window.D())
	return hits <= uint64(rule.DemoteAfterAccesses) && hits < uint64(rule.PromoteAfterAccesses)
}

// applyAccessTransition runs on scanner nodes and publishes eligible demotion
// candidates to the node's next access-counter shard.
func applyAccessTransition(_ context.Context, item *scannerItem, oi ObjectInfo) {
	if item == nil || item.lifeCycle == nil || globalAccessTierState == nil || !item.lifeCycle.HasAccessTransition() {
		return
	}
	cfg := globalILMConfig.accessCfg()
	if !cfg.AccessTiering || !accessDemoteEligible(item.lifeCycle, oi, item.poolIdx, cfg, time.Now()) {
		return
	}
	globalAccessTracker.noteDemoteCandidate(item.bucket, item.objectPath(), item.poolIdx)
}

func (s *accessTierState) processTask(task accessTierTask) error {
	completed := false
	defer func() {
		if !completed {
			s.releasePending(task, task.direction == accessTierPromote)
		}
	}()
	if err := task.ctx.Err(); err != nil {
		return err
	}
	cfg := globalILMConfig.accessCfg()
	if !cfg.AccessTiering || !validAccessTopology(s.z, cfg) || s.z.IsRebalanceStarted(task.ctx) || s.z.IsDecommissionRunning() {
		return errAccessTierNotEligible
	}
	if s.z.IsSuspended(task.dst) || s.z.IsPoolRebalancing(task.dst) {
		return errAccessTierNotEligible
	}
	hot, _ := cfg.HotPool()
	cold, _ := cfg.ColdPool()
	if task.direction == accessTierPromote && task.dst != hot || task.direction == accessTierDemote && task.dst != cold {
		return errAccessTierNotEligible
	}

	reserved := task.bytes
	var auditOI ObjectInfo
	moved, err := moveObjectPool(task.ctx, s.z, task.bucket, task.object, task.src, task.dst,
		func(latest ObjectInfo, exactBytes uint64) error {
			auditOI = latest
			lc, err := globalLifecycleSys.Get(task.bucket)
			if err != nil {
				return errAccessTierNotEligible
			}
			rule, _, ok := lc.AccessRule(latest.ToLifecycleOpts())
			if !ok {
				return errAccessTierNotEligible
			}
			hits := globalAccessTracker.hits(task.bucket, task.object, rule.Window.D())
			if task.direction == accessTierDemote {
				if !accessDemoteEligible(lc, latest, task.src, cfg, time.Now()) {
					return errAccessTierNotEligible
				}
				return nil
			}
			if hits < uint64(rule.PromoteAfterAccesses) {
				return errAccessTierNotEligible
			}
			if !s.promotionWatermarkOK(task.ctx, cfg, task.dst, task.bucket, task.object, exactBytes) {
				s.recordSkip("watermark")
				return errAccessTierNotEligible
			}
			reason, ok := s.replaceReservation(task.bucket, reserved, exactBytes, cfg, lc.AccessQuotaBytes())
			if !ok {
				s.recordSkip(reason)
				return errAccessTierNotEligible
			}
			reserved = exactBytes
			return nil
		})
	task.bytes = reserved
	if err != nil {
		return err
	}
	s.completeTask(task, moved)
	completed = true

	if auditOI.Bucket == "" {
		auditOI = ObjectInfo{Bucket: task.bucket, Name: task.object}
	}
	direction := "demote"
	if task.direction == accessTierPromote {
		direction = "promote"
	}
	tags := newLifecycleAuditEvent(lcEventSrc_AccessTier, lifecycle.Event{}).Tags()
	tags["ilm-access-direction"] = direction
	tags["ilm-access-src-pool"] = strconv.Itoa(task.src)
	tags["ilm-access-dst-pool"] = strconv.Itoa(task.dst)
	auditLogLifecycle(task.ctx, auditOI, ILMAccessTier, tags, globalLifecycleSys.trace(auditOI))
	return nil
}

// accessObjectVersions resolves a single object's complete version stack from
// the specified source pool using the same quorum listing primitive as
// rebalance/decommission.
func accessObjectVersions(ctx context.Context, z *erasureServerPools, src int, bucket, object string) ([]FileInfo, error) {
	if src < 0 || src >= len(z.serverPools) {
		return nil, errInvalidArgument
	}
	set := z.serverPools[src].getHashedSet(encodeDirObject(object))
	disks, _ := set.getOnlineDisksWithHealing(false)
	if len(disks) == 0 {
		return nil, errErasureReadQuorum
	}
	listingQuorum := (set.setDriveCount + 1) / 2
	resolver := metadataResolutionParams{dirQuorum: listingQuorum, objQuorum: listingQuorum, bucket: bucket}

	var (
		mu       sync.Mutex
		versions []FileInfo
		found    bool
		foundErr error
	)
	consume := func(entry metaCacheEntry) {
		if entry.name != object {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if found {
			return
		}
		fivs, err := entry.fileInfoVersions(bucket)
		if err != nil {
			foundErr = err
			found = true
			return
		}
		versions = append(versions, fivs.Versions...)
		found = true
	}
	err := listPathRaw(ctx, listPathRawOptions{
		disks: disks, bucket: bucket, path: object, recursive: true,
		minDisks: listingQuorum, reportNotFound: false,
		agreed: consume,
		partial: func(entries metaCacheEntries, _ []error) {
			if entry, ok := entries.resolve(&resolver); ok {
				consume(*entry)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	if foundErr != nil {
		return nil, foundErr
	}
	if !found || len(versions) == 0 {
		return nil, ObjectNotFound{Bucket: bucket, Object: object}
	}
	if int64(len(versions)) > scannerExcessObjectVersions.Load() {
		return nil, errAccessTierTooManyVersion
	}
	for _, version := range versions {
		if version.IsRemote() {
			return nil, errAccessTierRemoteVersion
		}
	}
	versionsSorter(versions).reverse()
	return versions, nil
}

func accessObjectBytes(ctx context.Context, z *erasureServerPools, src int, bucket, object string) (uint64, error) {
	versions, err := accessObjectVersions(ctx, z, src, bucket, object)
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, version := range versions {
		if !version.Deleted && version.Size > 0 {
			total = addUsage(total, uint64(version.Size))
		}
	}
	return total, nil
}

func deleteAccessTierPoolVersion(ctx context.Context, z *erasureServerPools, pool int, bucket, object, versionID string) error {
	if versionID == "" {
		versionID = nullVersionID
	}
	_, err := z.serverPools[pool].DeleteObject(ctx, bucket, encodeDirObject(object), ObjectOptions{
		Versioned: true, VersionID: versionID, NoLock: true, NoAuditLog: true,
	})
	if isErrObjectNotFound(err) || isErrVersionNotFound(err) {
		return nil
	}
	return err
}

// rollbackAccessTierDestination removes only what this move wrote.
//
// Existing destination versions are never included in written. Both commit
// locks remain held during rollback; the marker check also refuses cleanup of
// an unversioned copy that no longer belongs to this attempt.
func rollbackAccessTierDestination(ctx context.Context, z *erasureServerPools, dst int, bucket, object string, written []string, movedAt int64) {
	stamp := accessTierStamp(dst, movedAt)
	for _, versionID := range written {
		if versionID == "" {
			// Unversioned bucket: a single version with no ID. Only drop it
			// if it is still the copy this move made.
			oi, err := z.serverPools[dst].GetObjectInfo(ctx, bucket, encodeDirObject(object), ObjectOptions{NoLock: true})
			if err != nil {
				if !isErrObjectNotFound(err) && !isErrVersionNotFound(err) {
					ilmLogIf(ctx, fmt.Errorf("access tier rollback failed for %s/%s in pool %d: %w", bucket, object, dst, err))
				}
				continue
			}
			if oi.UserDefined[accessTierMetadataKey] != stamp {
				ilmLogIf(ctx, fmt.Errorf("access tier rollback skipped for %s/%s in pool %d: destination was overwritten concurrently", bucket, object, dst))
				continue
			}
		}
		if err := deleteAccessTierPoolVersion(ctx, z, dst, bucket, object, versionID); err != nil {
			ilmLogIf(ctx, fmt.Errorf("access tier rollback failed for %s/%s version %s in pool %d: %w", bucket, object, versionID, dst, err))
		}
	}
}

// lockAccessTierObject excludes reads/deletes using the top-level namespace
// and writes committing in either hashed set. Distributed sets can share lock
// servers, so lock each distinct server once, in a stable order. Background
// moves require all involved lock servers; ordinary S3 quorum rules stay intact.
func lockAccessTierObject(ctx context.Context, z *erasureServerPools, bucket, object string, src, dst int) (context.Context, func(), error) {
	sets := []*erasureObjects{z.serverPools[0].sets[0], z.serverPools[src].getHashedSet(object), z.serverPools[dst].getHashedSet(object)}
	var locks []RWLocker
	local := make(map[*nsLockMap]bool)
	remote := make(map[string]dsync.NetLocker)
	var owner string
	for _, set := range sets {
		if !set.nsMutex.isDistErasure {
			if !local[set.nsMutex] {
				locks = append(locks, set.NewNSLock(bucket, object))
				local[set.nsMutex] = true
			}
			continue
		}
		peers, lockOwner := set.getLockers()
		if len(peers) == 0 {
			return nil, nil, errAccessTierNotEligible
		}
		owner = lockOwner
		for _, peer := range peers {
			if peer == nil {
				return nil, nil, errAccessTierNotEligible
			}
			remote[peer.String()] = peer
		}
	}
	for _, address := range slices.Sorted(maps.Keys(remote)) {
		peer := remote[address]
		locks = append(locks, (&nsLockMap{isDistErasure: true}).NewNSLock(func() ([]dsync.NetLocker, string) {
			return []dsync.NetLocker{peer}, owner
		}, bucket, object))
	}
	var unlockers []func()
	unlock := func() {
		for i := len(unlockers) - 1; i >= 0; i-- {
			unlockers[i]()
		}
	}
	for _, lk := range locks {
		lkctx, err := lk.GetLock(ctx, globalDeleteOperationTimeout)
		if err != nil {
			unlock()
			return nil, nil, err
		}
		ctx = lkctx.Context()
		unlockers = append(unlockers, func() { lk.Unlock(lkctx) })
	}
	return ctx, unlock, nil
}

// A retry may reuse an identical destination version. Conflicting versions
// are left intact, including null versions and independently changed tags or
// retention settings. Physical layout and move bookkeeping may differ by pool.
func sameAccessTierVersion(a, b FileInfo) bool {
	if a.VersionID != b.VersionID || a.Deleted != b.Deleted || a.Size != b.Size || !a.ModTime.Equal(b.ModTime) || !bytes.Equal(a.Checksum, b.Checksum) || len(a.Parts) != len(b.Parts) {
		return false
	}
	for i, part := range a.Parts {
		other := b.Parts[i]
		if part.Number != other.Number || part.Size != other.Size || part.ActualSize != other.ActualSize || part.ETag != other.ETag || !bytes.Equal(part.Index, other.Index) || !maps.Equal(part.Checksums, other.Checksums) {
			return false
		}
	}
	metadata := func(fi FileInfo) map[string]string {
		m := maps.Clone(fi.Metadata)
		delete(m, accessTierMetadataKey)
		delete(m, xMinIODataMov)
		delete(m, ReservedMetadataPrefixLower+"inline-data")
		delete(m, minIOErasureUpgraded)
		return m
	}
	return maps.Equal(metadata(a), metadata(b))
}

// moveObjectPool copies missing versions oldest-first, preserving versions
// already at the destination. The source is removed only after every source
// version is present there. This also resumes after an uncertain source delete.
func moveObjectPool(ctx context.Context, z *erasureServerPools, bucket, object string, src, dst int, beforeCopy func(ObjectInfo, uint64) error) (uint64, error) {
	if bucket == minioMetaBucket || src == dst || src < 0 || dst < 0 || src >= len(z.serverPools) || dst >= len(z.serverPools) {
		return 0, errAccessTierNotEligible
	}
	if z.IsRebalanceStarted(ctx) || z.IsDecommissionRunning() || z.IsSuspended(dst) || z.IsPoolRebalancing(dst) {
		return 0, errAccessTierNotEligible
	}

	ctx, unlock, err := lockAccessTierObject(ctx, z, bucket, encodeDirObject(object), src, dst)
	if err != nil {
		return 0, err
	}
	defer unlock()

	versions, err := accessObjectVersions(ctx, z, src, bucket, object)
	if err != nil {
		return 0, err
	}
	if len(versions) == 1 && versions[0].Deleted {
		return 0, errAccessTierNotEligible
	}
	versioned := globalBucketVersioningSys.PrefixEnabled(bucket, object)
	latest := versions[len(versions)-1].ToObjectInfo(bucket, object, versioned)
	var total uint64
	for _, version := range versions {
		if !version.Deleted && version.Size > 0 {
			total = addUsage(total, uint64(version.Size))
		}
	}
	if beforeCopy != nil {
		if err := beforeCopy(latest, total); err != nil {
			return 0, err
		}
	}

	// Never clear a pre-existing destination: it may hold the only surviving
	// copy of a version after a partially committed source deletion.
	dstVersions, dstErr := accessObjectVersions(ctx, z, dst, bucket, object)
	if dstErr != nil && !isErrObjectNotFound(dstErr) && !isErrVersionNotFound(dstErr) {
		return 0, dstErr
	}
	existing := make(map[string]FileInfo, len(dstVersions))
	for _, version := range dstVersions {
		existing[version.VersionID] = version
	}
	for _, version := range versions {
		if prior, ok := existing[version.VersionID]; ok && !sameAccessTierVersion(version, prior) {
			return 0, errAccessTierNotEligible
		}
	}

	movedAt := time.Now().UnixNano()
	var written []string
	rollbackDestination := true
	defer func() {
		if len(written) == 0 || !rollbackDestination {
			return
		}
		// Roll back before releasing the namespace lock. Use a fresh timeout
		// when leadership cancellation caused the original failure.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), globalDeleteOperationTimeout.Timeout())
		defer cancel()
		rollbackAccessTierDestination(cleanupCtx, z, dst, bucket, object, written, movedAt)
	}()

	set := z.serverPools[src].getHashedSet(encodeDirObject(object))
	for _, version := range versions {
		if _, ok := existing[version.VersionID]; ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		versionID := version.VersionID
		if versionID == "" {
			versionID = nullVersionID
		}
		if version.Deleted {
			written = append(written, version.VersionID)
			_, err = z.serverPools[dst].DeleteObject(ctx, bucket, encodeDirObject(object), ObjectOptions{
				Versioned: true, VersionID: versionID, MTime: version.ModTime,
				DeleteReplication: version.ReplicationState,
				SrcPoolIdx:        src, DstPoolIdx: &dst, DataMovement: true,
				DeleteMarker: true, NoLock: true, NoAuditLog: true,
			})
			if err != nil {
				return 0, err
			}
			continue
		}

		gr, err := set.GetObjectNInfo(ctx, bucket, encodeDirObject(object), nil, http.Header{}, ObjectOptions{
			VersionID: versionID, NoDecryption: true, NoLock: true, NoAuditLog: true,
		})
		if err != nil {
			return 0, err
		}
		written = append(written, version.VersionID)
		if err = moveAccessTierVersion(ctx, z, src, dst, bucket, gr, movedAt); err != nil {
			return 0, err
		}
	}

	// Re-read while still holding the namespace lock and refuse the source
	// delete if the latest version changed despite the lock contract.
	current, err := z.serverPools[src].GetObjectInfo(ctx, bucket, encodeDirObject(object), ObjectOptions{NoLock: true, Versioned: versioned})
	if err != nil {
		return 0, err
	}
	if !current.ModTime.Equal(latest.ModTime) || current.VersionID != latest.VersionID || current.ETag != latest.ETag || current.DeleteMarker != latest.DeleteMarker {
		return 0, errAccessTierNotEligible
	}
	// Once source deletion starts, an error can be an uncertain commit. Keep
	// the complete destination in that case: preserving two copies is safer
	// than risking zero.
	rollbackDestination = false
	// A recursive prefix delete could also remove another key such as
	// "object/child" on this set. Delete only the versions we actually copied.
	for _, version := range versions {
		if err := deleteAccessTierPoolVersion(ctx, z, src, bucket, object, version.VersionID); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func moveAccessTierVersion(ctx context.Context, z *erasureServerPools, src, dst int, bucket string, gr *GetObjectReader, movedAt int64) (err error) {
	oi := gr.ObjInfo
	defer gr.Close()

	actualSize, err := oi.GetActualSize()
	if err != nil {
		return err
	}
	metadata := maps.Clone(oi.UserDefined)
	if metadata == nil {
		metadata = make(map[string]string, 1)
	}
	metadata[accessTierMetadataKey] = accessTierStamp(dst, movedAt)
	// Multipart ETags are derived from plaintext part hashes at upload time.
	// A raw encrypted copy must retain that ETag instead of hashing ciphertext.
	metadata["etag"] = oi.ETag

	if oi.isMultipart() {
		res, err := z.NewMultipartUpload(ctx, bucket, oi.Name, ObjectOptions{
			VersionID: oi.VersionID, UserDefined: metadata, NoAuditLog: true,
			NoLock: true, DataMovement: true, SrcPoolIdx: src, DstPoolIdx: &dst,
		})
		if err != nil {
			return err
		}
		abort := true
		defer func() {
			if abort {
				z.AbortMultipartUpload(ctx, bucket, oi.Name, res.UploadID, ObjectOptions{NoAuditLog: true})
			}
		}()
		parts := make([]CompletePart, len(oi.Parts))
		for i, part := range oi.Parts {
			hr, err := hash.NewReader(ctx, io.LimitReader(gr, part.Size), part.Size, "", "", part.ActualSize)
			if err != nil {
				return err
			}
			pi, err := z.PutObjectPart(ctx, bucket, oi.Name, res.UploadID, part.Number, NewPutObjReader(hr), ObjectOptions{
				PreserveETag: part.ETag,
				IndexCB:      func() []byte { return part.Index },
				NoAuditLog:   true, NoLock: true,
			})
			if err != nil {
				return err
			}
			parts[i] = CompletePart{
				ETag: pi.ETag, PartNumber: pi.PartNumber,
				ChecksumCRC32: pi.ChecksumCRC32, ChecksumCRC32C: pi.ChecksumCRC32C,
				ChecksumCRC64NVME: pi.ChecksumCRC64NVME,
				ChecksumSHA1:      pi.ChecksumSHA1, ChecksumSHA256: pi.ChecksumSHA256,
			}
		}
		_, err = z.CompleteMultipartUpload(ctx, bucket, oi.Name, res.UploadID, parts, ObjectOptions{
			SrcPoolIdx: src, DstPoolIdx: &dst, DataMovement: true,
			VersionID: oi.VersionID, MTime: oi.ModTime, NoLock: true, NoAuditLog: true,
		})
		if err != nil {
			return err
		}
		abort = false
		return nil
	}

	hr, err := hash.NewReader(ctx, io.LimitReader(gr, oi.Size), oi.Size, "", "", actualSize)
	if err != nil {
		return err
	}
	opts := ObjectOptions{
		SrcPoolIdx: src, DstPoolIdx: &dst, DataMovement: true,
		VersionID: oi.VersionID, MTime: oi.ModTime, UserDefined: metadata,
		PreserveETag: oi.ETag, NoLock: true, NoAuditLog: true,
	}
	if len(oi.Parts) > 0 {
		opts.IndexCB = func() []byte { return oi.Parts[0].Index }
	}
	_, err = z.PutObject(ctx, bucket, oi.Name, NewPutObjReader(hr), opts)
	return err
}
