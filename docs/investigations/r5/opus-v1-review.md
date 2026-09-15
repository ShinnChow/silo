Write is disabled in this session, so the review is delivered here in full.

# Independent review — R5 repair plan v1

**Reviewer model: `claude-opus-5` (Opus 5), effort max.** I read `docs/investigations/r5/plan-v1.md` completely, `docs/investigations/r5/baseline.md`, both raw logs, `baseline_test.go`, and then independently inspected the sender/receiver/storage chain at `9ebe81c1b`.

**VERDICT: REQUEST_CHANGES** for plan v1 sha256 `fd6051527ebf19f624125bd3238da2f938420917226387c0f9373f1a98e87993`. Five blocking corrections (R1–R5). **No consensus is claimed.**

Binding caveat, stated exactly: this session has read-only tools, so I did not execute `sha256sum`. I reviewed the file content at that path at the baseline SHA and cite the plan's own line numbers so you can bind findings to the hash.

---

## 1. Claims I confirmed from source (necessity, not inference)

| Plan | Claim | Evidence | Verdict |
|---|---|---|---|
| L14 | PUT tagging stamps only when replication is selected; DELETE never stamps | `cmd/object-handlers.go:3762-3768`, `:3865-3870` | **correct** (and PUT uses two separate `UTCNow()` calls, 3765/3767) |
| L14 | both write under the existing object lock; multi-pool updates the addressed version in each copy | `cmd/erasure-object.go:2272-2337`; `cmd/erasure-server-pool.go:3033-3075` | **correct** |
| L15 | `putReplicationOpts` stamps only inside nonempty `UserTags`; multipart clears SourceMTime | `cmd/bucket-replication.go:849-863`, `:1776` | **correct** |
| L16 | metadata COPY carries an explicit empty tag; ModTime default only for nonempty | `:747`, `:1692-1700`; SDK `copyObjectDo:262-264` writes the map verbatim | **correct** |
| L18 | PUT / multipart initiation parse but never persist the source stamp | `cmd/object-api-options.go:473`; no writer anywhere in `cmd/` | **correct** (matches `baseline-extended.log:19-26`) |
| L18 | multipart completion already rechecks the persisted upload under the object lock | `cmd/erasure-multipart.go:1161-1190` | **correct** |
| L19 | `getReplicationAction` compares values/counts, not ordering time | `cmd/bucket-replication.go:1000-1005` | **correct** |
| L20 | `checkPreconditionsPUT` skips matching version/ETag for non-SSE-C replicas | `cmd/object-handlers-common.go:233-247` | **correct**; and senders treat 412 as delivered (`:1466`; multipart `:1786-1788` returns `nil`) |
| L21 | ACK callback copies stale `ri.UserTags` | `cmd/bucket-replication.go:1272-1274` | **correct, and worse than stated** |
| L23 | `reconcileStoredObjectTags` gates as described | `cmd/erasure-server-pool-consistency.go:232-243` | **correct** |

Two amplifiers the plan does not name, both strengthening it:

- The ACK callback writes stale tags **without** a timestamp. The revived tag set therefore inherits the *deletion's newer* revision and propagates downstream as authoritative. Removal is the right fix and is sufficient: `er.PutObjectMetadata` preserves `fi.Metadata`'s tag key (`cmd/erasure-object.go:2260`) and `updatePoolMetadata` falls back to merged `UserTags` (`cmd/erasure-server-pool-consistency.go:194-214`).
- The `encMetadata` merge at `cmd/object-handlers.go:1903` restores every reserved key snapshotted at `:1655-1659`; the guard at `:1855-1864` covers only the two Object Lock stamps. Tag revision is genuinely exposed, so L47 is justified.

---

## 2. Blocking disagreements

### R1 — Do not synthesize a ModTime revision for objects with no tags and no revision
**Where:** L35 ("otherwise object ModTime (also for empty legacy objects)") composed with L49 ("persist a nonzero parsed trusted source timestamp").

**Evidence:** `PutObjectOptions.Header()` emits the header whenever `TaggingTimestamp` is non-zero (SDK `api-put-object.go:236-238`). If L35 moves selection outside the nonempty branch *and* defaults to ModTime, every replicated object — including every object that has never carried a tag — ships a non-zero stamp, and L49 persists it. Every object on the destination then owns a tag revision. Composed with L39 (recorded revision ⇒ force metadata replication), **every object at the next hop always selects metadata replication.** It also contradicts L10 ("not a reason to change the storage format") and L57 ("we do not invent historical deletion times").

**Smallest correction:** send a stamp only when `objInfo.UserTags != ""` **or** a recorded revision exists. That keeps the tombstone case (empty + revision — the entire point), keeps the existing nonempty ModTime fallback, and drops only empty + no-revision, which L57 already declares unrecoverable. This makes §B consistent with §D.

### R2 — Bound the forced metadata replication in `getReplicationAction`
**Where:** L39.

**Evidence:** the destination's revision is invisible to HEAD, so the condition never becomes false. Any object carrying a revision never returns `replicateNone` again: every heal, MRF retry and `ExistingObjectReplicationType` resync re-COPIES its metadata, rewriting `xl.meta` on the destination (and, multi-pool, running `retireReplicaCopies`) each pass. L39's "extra COPY only for already-scheduled work" understates a permanent non-convergence. Existing tests won't catch it — `newMatchingReplicationPair` (`cmd/bucket-replication_test.go:716-739`) carries no revision.

**Smallest correction:** fire only when `oi1.UserTags == ""` and a revision is recorded — exactly the empty-to-empty tombstone L19 names and `TestReviewR5SameEmptyTagsMustTransferTimestamp` asserts. Nonempty states are already caught by the existing value/count comparison at `:1003`. Then state the residual: tag-deleted objects still never converge to `replicateNone`.

### R3 — The production metadata COPY does not send `x-amz-metadata-directive: REPLACE`
**Where:** L17.

**Evidence:** `getCopyObjMetadata` sets `x-amz-tagging-directive: REPLACE` (`:748`) but never the metadata directive, so `getCpObjMetadataFromHeader` takes the `defaultMeta` branch (`cmd/object-handlers.go:1143,1165-1170`). Therefore:
1. "its REPLACE metadata map also loses the previous timestamp before comparison" is **false on the production path** — `defaultMeta` preserves the stored revision. It is true only for a peer that does send REPLACE.
2. The empty tombstone is dropped for a *different* reason than the plan gives: `defaultMeta` carries the stored `X-Amz-Tagging` forward and the `objTags != ""` gate at `:1817` skips the overwrite. (Note `X-Amz-Tagging` is in `supportedHeaders`, `cmd/handler-utils.go:84`, so on the REPLACE path the empty value *does* arrive in the map — only the stamp is missing.)
3. The reproduction sends `x-amz-metadata-directive: REPLACE` (`baseline_test.go:134`), so it **does not pin the production request shape.** The conclusion still holds (with a stale stored stamp the delayed COPY wins either way), but the evidence chain as written is not the one production executes.

**Smallest correction:** fix L17, and add a sender-shaped COPY case asserting against `getCopyObjMetadata` output rather than a hand-built header map.

### R4 — Missing monotonic guard on the local revision at commit
**Where:** L31 explicitly asks the reviewer to decide. My answer: commit-time *generation* is not required; a commit-time monotonic *guard* is.

**Evidence:** `er.PutObjectTags` writes `fi.Metadata[x-amz-tagging]` and copies `opts.UserDefined` with **no ordering check** (`cmd/erasure-object.go:2328-2330`), and the handler mints the stamp *before* the namespace lock. R5 newly makes DELETE mint a revision, so a DELETE→PUT pair can invert — via lock queueing (`globalOperationTimeout` waits) or clock skew between the two nodes serving the two requests. Result: source holds `tags=X @ t_old`, replica holds the tombstone `@ t_new`. Every retransmit is then rejected by `reconcileStoredObjectTags` (`stamp.Before(incoming)` false), the sender still records **Completed**, and — with R2's rule — re-sends forever. Permanent, silent divergence: precisely the failure class R5 exists to remove, newly broadened by change A.

**Smallest correction:** in `er.PutObjectTags`, under the lock, if the incoming revision is not strictly after the stored one, advance it to stored + 1ns. Multi-pool is safe without a second site of change: `z.PutObjectTags` writes identical tags to all copies, so any per-pool stamp differences still merge to a consistent `(tags, newest stamp)` pair through `mergedPoolObjectInfo` (`cmd/erasure-server-pool-consistency.go:124-131`).

### R5 — Under-specified duplicate-suppression comparison
**Where:** L51, "a valid newer source tag timestamp makes matching version/ETag insufficient".

**Evidence:** read naively as "non-zero source stamp ⇒ bypass", this disables the duplicate guard for *every* tagged replica write; for multipart it re-uploads all parts, since 412 at initiation is currently the cheap exit (`:1786-1788`). `TestReviewR5NewerTagsMustBypassContentDuplicate` already encodes the correct comparison (source stamp vs. `oi`'s stored stamp), but the prose does not.

**Smallest correction:** one sentence — "strictly newer than the destination's stored tag revision" — plus the cost note that even correctly scoped, this re-PUTs object data to deliver a tag-only change.

---

## 3. Accepted / non-blocking (state them; do not necessarily fix)

- **§A local generation semantics** (L29): accepted as sufficient, subject to R4. Use one `UTCNow()` for both stamps as proposed.
- **§B ACK removal** (L41): necessary and sufficient; preservation verified on both the single-pool and pooled write-back paths.
- **§C `encMetadata` reconciliation** (L47): accepted. Today's observable effect is fail-closed (update dropped) when `ReplicaLockReconcile` is on, and a mismatched `(new tags, old stamp)` pair when `VersionID == ""` — worth one sentence.
- **Equal timestamps:** adopting `reconcileStoredObjectTags` in the handler silently flips the non-versioned COPY path from "incoming wins on equal" (`cmd/object-handlers.go:1824`, `!ondiskTimestamp.After(srcTimestamp)`) to "stored wins on equal". This is the right direction and removes a real handler/storage inconsistency, but it is a compat-visible change and belongs in §D.
- **Missing/invalid timestamps:** the gate asymmetry is correct as the plan describes — invalid *stored* ⇒ incoming wins; invalid *incoming* with valid stored ⇒ stored wins. Note the metadata COPY sender swallows parse errors (`:1696-1699`, `if err == nil`); L35's fail-loud rule should cover that call site too.
- **Trust boundary: sound.** Stamp honored only under `trustedReplication` (`cmd/object-api-options.go:390-396`); headers stripped otherwise (`cmd/replication-trust.go:96-112,139-143`); client-supplied reserved headers rejected wholesale (`cmd/generic-handlers.go:75-85`). One asymmetry to resolve deliberately: the tag decision keys on `dstOpts.ReplicationRequest` (trusted marker) while Object Lock keys on `replicaTrusted` (marker + REPLICA). Production sets both.
- **SSE-KMS / R4 boundary — more precise than the plan:** the KMS early return (`cmd/object-api-options.go:431-461`) drops `ReplicationSourceTaggingTimestamp`, so R5's PUT/multipart persistence is a **silent no-op for KMS-header requests** until R4 lands. The metadata-COPY leg is **not** affected: the public SSE header is synthesized only in responses (`cmd/api-response.go:525-533`), so `getCopyObjMetadata` never forwards it and tag deletions still order correctly for SSE-KMS objects via COPY. Say this instead of only "KMS combined tests after R4".
- **Ordinary PUT/multipart with tags writes no revision,** so §A's invariant has a hole. Mostly masked by versioning; exposed on null/suspended versions, where `ReplicaLockReconcile` is off anyway (`:1847`, `:2432`). Either stamp there too (~3 lines) or document.
- **Tag-filtered replication rules never see a deletion:** `FilterTargetArns` runs on the *post*-deletion `UserTags` (`cmd/bucket-replication.go:1189-1193`; `scheduleReplication` is called with the post-write `oi` at `cmd/object-handlers.go:3879`), so a rule with a tag filter yields no target. Pre-existing, but it bounds any completeness claim.
- **Sender retries are otherwise clean:** `replicateAll` re-reads current state (`:1517`), so retries carry the current revision; staleness was confined to the ACK callback. No new HEAD protocol — agreed.
- **UUID / null / versioned / multi-pool:** `metadataPoolInfos`, `mergedPoolObjectInfo`, `retireReplicaCopies` and the `nullVersionID` normalization (`:136-138`, `:1162-1168`) do carry tag state correctly; I found no additional loss path there beyond R4's clock inversion.

---

## 4. Can the proposed coverage establish the scope?

**Can:** per-hop handler + storage behavior on real erasure disks (ErasureSD and 16-drive), multi-pool merge/retire through `consistencyPools` and the existing tag suite (`cmd/erasure-server-pool-tags_test.go`), sender option construction, and the ACK race with a deterministic interleave.

**Cannot:** cross-site convergence, real clock skew, real lock contention. Both `ExecObjectLayerAPITest` instances run in one process against one clock. L73's "Not production multi-site acceptance" should be sharpened to say the suite establishes **per-hop** correctness only.

**Must add:** (a) an untagged-object regression asserting **no** revision is synthesized (R1); (b) the sender-shaped metadata COPY (R3); (c) a DELETE→PUT inversion, by passing an older stamp directly at the storage call (R4); (d) equal-timestamp parity across the versioned and null-version COPY paths; (e) an SSE-KMS metadata COPY case marked blocked-on-R4. Also keep the three assertions inside `TestReviewR5DeleteThenDelayedTagUpdate` separable — once change A lands, the first passes and the later two silently depend on it.

---

## 5. Summary

- **Accepted:** §12.1/.2/.3/.5/.6/.7/.8 defect claims; §A generation model; §B ACK removal; §C `encMetadata` reconciliation, storage-recheck reuse, multipart persistence; §D R4 ownership and the no-migration position.
- **Non-blocking:** equal-timestamp behavior change, trust asymmetry, ordinary-PUT revision hole, tag-filtered rules, swallowed parse error in the COPY sender.
- **Required (blocking):** R1 ModTime fallback scope; R2 forced-replication scope; R3 metadata-directive correction + sender-shaped test; R4 monotonic guard at commit; R5 duplicate-suppression comparison wording.

Answering L75 directly: §A's existing local timestamp semantics are **not** sufficient without R4. §B's forced metadata synchronization is necessary but **wrongly scoped** (R1, R2); the stale ACK removal is necessary and correctly scoped. §C's duplicate exception is defensible but **under-specified** (R5). And yes — a path can still lose or revive the deletion revision: the DELETE→PUT local inversion (R4), plus the documented SSE-KMS PUT leg and tag-filtered-rule gaps.

I recommend a v2 addressing R1–R5, then re-review against the new hash. I made no edits.