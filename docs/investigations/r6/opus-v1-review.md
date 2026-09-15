I'll start by reading AGENTS.md and the plan file, then dig into the actual source.

Let me locate the core source files and the evidence directory.

I've verified the plan against the actual source. Writing up the review now.

The `Write` tool is disabled in this session, so I can't create the plan file — the review is below in full.

---

# Independent review — SILO R6 `plan-v1.md`

**Plan hash reviewed:** `bcd023e2b00ad3dc709baa738bb02551d47aeebb49879b0f76f2789f4c4dff8b`
**Source:** `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`
**I did not run anything.** All findings come from reading the source, the existing tests, PR #184's diff/prose, and the supplied `baseline.log` (read, not produced by me).

## Verdict: **REVISE**

One blocking finding. The classification rule, the canonical-wire choice, the MRF 405 work, and the retry-budget work are technically correct and better grounded than PR #184. One explicitly prescribed detail in §A causes a silent regression on the *canonical* purge shape — the shape every live producer emits — and §C would not catch it. The correction is small and stays inside the plan's bounded scope.

---

## Blocking

### B1 — Pinning `rinfo.ReplicationStatus = rinfo.PrevReplicationStatus` for purges makes the persisted marker creation-status block get rewritten from the fan-out subset

§A prescribes this "on every exit" while also forbidding changes to `getReplicationState` merging. Together that is a regression, not preservation.

1. `bucket-replication-utils.go:392-399` — `targetState()` never sets `ReplicationStatus`, so a canonical purge returns `""` today.
2. `bucket-replication-utils.go:92-101` — `ReplicationStatusInternal()` rebuilds `"arn=STATUS;"` **only from `rinfos.Targets`**, i.e. only fanned-out targets.
3. `bucket-replication.go:508-543` — fan-out skips `!Replicate`, non-matching `dobj.TargetArn`, and nil clients. `TargetArn` is really set by `queueReplicateDeletesWrapper` (`:2400-2409`).
4. `bucket-replication-utils.go:410-412` → `drs`, passed as `DeleteReplication` at `bucket-replication.go:572-582`.
5. `erasure-object.go:2163-2177` → `xl-storage-format-v2.go:1438-1446` — for a `DeleteType` version, **if `fi.DeleteMarkerReplicationStatus()` is non-empty**, `MetaSys[ReplicationStatus]` and `MetaSys[ReplicationTimestamp]` are overwritten.

Today that guard never fires for a canonical purge: the string is `"arn1=;"`, `replicationStatusesMap` doesn't match it (`:428-439`), composite over the empty map is `""` (`:455-459`) — so the on-disk creation block is left **untouched**, which preserves it perfectly including non-attempted targets. Under the plan it becomes non-empty and is rewritten:

- **Status loss:** `"arn1=COMPLETED;arn2=COMPLETED;"` with a fan-out covering only `arn2` persists `"arn2=COMPLETED;"` — `arn1` silently dropped. That is the exact failure the plan exists to fix, newly introduced on the main shape.
- **Zero timestamp:** `rs.ReplicationTimeStamp = rinfos.ReplicationTimeStamp` (`:413`) is never assigned in `replicateDelete`; it's only rescued by `bucket-replication.go:568-570` when the composite *changes*. A repeated FAILED→FAILED purge writes `0001-01-01T00:00:00Z`.
- **Empty ReplicaStatus:** under a legacy `RoleArn` config the composite can be `REPLICA` (`bucket-replication-utils.go:497-503`), taking `xl-storage-format-v2.go:1398-1400`, which writes `ReplicaStatus` — never populated by `ObjectInfo.ReplicationState()` (`:569-587`).

Only delete-**marker** versions are affected; `ObjectType` versions only touch `VersionPurgeStatusKey` (`xl-storage-format-v2.go:1465-1476`).

**Minimal correction (strictly smaller than the plan):** keep the classification and every exit fix, but for purges **leave `rinfo.ReplicationStatus` at its zero value** rather than pinning it. That is what the canonical path already does, and it preserves the on-disk block byte-for-byte including non-attempted targets, with no change to `getReplicationState`. Consequences that then become mandatory, not optional:

- purge exits write only `VersionPurgeStatus` — never `Failed`, `Completed`, or `PrevReplicationStatus`;
- the resync defer (`:618-622`) must be gated on the operation's own success. Under the plan as written, pinning `Completed` would stamp the reset marker for a **failed** purge on any marker whose creation was COMPLETED — the two changes are coupled and cannot land separately;
- the stats gate at `:556` must be replaced by the per-target operation-status comparison §A already calls for, otherwise purges stop being reported at all.

Add a partial-fan-out test: two ARNs persisted, one excluded from fan-out, failed purge → marker metadata unchanged. §C item 5 fans out to *both* targets and cannot detect this.

---

## Nonblocking

1. **The wire version ID is load-bearing.** For the legacy shape `dobj.VersionID` is empty, so the implementation must keep the existing `versionID` fallback (`bucket-replication.go:609-612`). A `RemoveObject` with empty `VersionID` against a versioned target **creates a new marker** (`erasure-object.go:2126-2149`) — the exact resurrection being fixed. Make §C1 assert the outgoing `versionId` and `x-minio-source-deletemarker` explicitly.
2. **Retry increment must cover all three delete-path MRF sites:** `:487` (lock), `:565` (aggregate failure), `:2427` (queue full). §B names only the first two; missing `:2427` leaves an unbounded loop once marker MRF is live. Mirror `ri.RetryCount++` at `:1310`.
3. **Stats will move.** `COMPLETE`→`COMPLETED` makes `ReplicationStats.Update` reach its `Completed` case for Heal/ExistingObject deletes (`bucket-replication-stats.go:184-201`, `replication.go:139-144`). Today `"COMPLETE" != "COMPLETED"` so nothing is recorded. Assert expected counter deltas rather than discovering them.
4. **§B's identity gate excludes null-version markers by construction** — `GetObjectInfo` returns `ObjectNotFound`, not 405, when `VersionID == ""` (`erasure-object.go:996-999`), and `ToObjectInfo` leaves `VersionID` empty when `versioned` is false (`erasure-metadata.go:120-123`). No regression, but document it instead of implying MRF healing is complete.
5. **Legacy-shape scope.** I found no producer of that shape on this baseline: `object-handlers.go:3225-3228`, `bucket-handlers.go:565-570`, `bucket-replication.go:3323-3327`/`:3775-3779` are mutually exclusive, and `erasure-object.go:1752-1766` only sets `DeleteMarkerVersionID` when `VersionID == ""`. `DeletedObjectReplicationInfo` isn't serialized, so it can't survive a restart. §A's legacy handling is upgrade/robustness work; the live value of R6 is mostly §B. Say so in the PR text so the fork doesn't inherit #184's overclaiming.
6. **§C item 3 is testable but fiddly.** `globalLocalDrivesMap` is populated by `newErasureServerPools` (`erasure-server-pool.go:174-181`), so save/load works — but `loadMRF` **deletes the file after reading** (`:4013-4015`) and `queueMRFHeal` dispatches a detached goroutine with a 1s per-entry context (`:4067-4081`). Re-persist between rounds and synchronize, don't sleep.
7. **Confirm the fixtures run.** The supplied `baseline.log` shows both `TestReplicateDeleteMarkerPurge` subtests aborting at `replication-delete-marker_test.go:121` with "Storage reached its minimum free drive threshold" — an environment failure. §C items 2, 3, 5 all depend on those fixtures.

---

## Independently confirmed as correct in the plan

- **Observation 1 holds at every exit:** `:624` (early-return without sending), `:645-649`/`:702-706`/`:712-716` (field selected on `dobj.VersionID`), `:684-688` (HEAD-not-ready overwrites creation status unconditionally), `:628` (completed-purge early-out only for non-empty `VersionID`), `:618-622` (resync stamp keyed on `ReplicationStatus`). The supplied probe log agrees.
- **Observation 2 is a real regression in PR #184.** `replicateDelete` selects on `dobj.VersionID != ""` (`:549-552`), so with only the target-side fix a failed legacy purge aggregates to `COMPLETED`, emits `ObjectReplicationComplete`, and **skips `queueMRFSave`** (`:563-566`) — worse than baseline.
- **Task-level classification is required; #184's per-target `isDMPurge` is wrong.** `VersionPurgeStatus()` needs `completed == len(ri.Targets)` (`bucket-replication-utils.go:122-139`); a target classified as a creation never sets the purge field, so the composite can never reach COMPLETE. The task-level rule is also safe here — no current producer emits a creation with a non-empty composite purge status.
- **The canonical-wire choice really does fix resurrection; #184 does not.** With `ReplicationDeleteMarker=true` and an absent version the receiver re-creates the marker (`xl-storage.go:1346-1349`; `xl-storage-format-v2.go:1517-1520`). With `false`, the receiver returns VersionNotFound (`erasure-object.go:2013-2027`) and the handler answers 204 (`object-handlers.go:3171-3192`). Scope it honestly: this only changes *legacy-shaped* purges (canonical already sends `false`), and does nothing for a delayed creation after a purge — correctly filed as separate.
- **§B's 405 premise is exact:** `erasure-object.go:996-1002` returns a populated `ObjectInfo` with `toObjectErr(errMethodNotAllowed,…)` → `MethodNotAllowed{}` (`object-api-errors.go:96-102`), preserved multi-pool too (`erasure-server-pool.go:1063-1077`).
- **§B's retry gap is real:** `ToMRFEntry()` (`:1927-1937`) never sets `RetryCount`, so the `> mrfRetryLimit` drop (`:3883-3887`) can never fire for deletes. No msgp code exists for the type, so the field is genuinely schema-free.
- **Resync accounting already handles both shapes** — `resyncTargetSucceeded` (`:3154-3162`) keys on `roi.VersionPurgeStatus`. The plan correctly leaves it alone.
- **The pushback on #184's permanence/topology prose is correct.** Canonical purge retries are not short-circuited on this baseline (`:624` requires `VersionID == ""`; `:628` exits only on `VersionPurgeComplete`). #184's Bugs 1 and 2 as narrated are legacy-shape-only.

---

Because B1 is a blocker, I am not stating consensus on this exact hash. Resolve B1 (and the coupled resync-defer/stats-gate items it forces) and the plan becomes technically acceptable in my judgement — implementation would still require the tests in §C plus the partial-fan-out regression above.
