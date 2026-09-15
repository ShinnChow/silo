# R5 repair plan v2 — tag deletion and ordered replication

Status: proposed v2, responding to real Opus v1 REQUEST_CHANGES; no production implementation before consensus.
Base: `9ebe81c1b3611f9cc73e676b5b741c2be62c467a` (local and GitHub main checked).

## Problem and necessity

A tag value and its `x-minio-internal-tagging-timestamp` form one state, including an empty value. Successful DeleteObjectTagging currently leaves the old timestamp. A delayed, authenticated metadata COPY newer than that old timestamp restores deleted tags. The parent and this worktree reproduce this through signed HTTP with real single-drive and 16-drive storage. An empty incoming COPY is also ignored.

The consequence is persistent incorrect user metadata on the source or replicas, affecting tag-based access/lifecycle behavior. Repair is justified before claiming replication correctness. It is not a reason to change the supported PGSTY dependency graph, storage format, encryption protocol, or Object Lock behavior.

## Current chain and additional gaps

1. `PutObjectTaggingHandler` stamps only when replication is selected. `DeleteObjectTaggingHandler` never stamps. Both storage methods write the value under the existing object lock; multi-pool PutObjectTags updates the addressed version in each copy.
2. `putReplicationOpts` sets the timestamp only inside nonempty UserTags. Multipart uses this builder and clears SourceMTime at initiation, so using initiation time as a tag fallback would be wrong.
3. Metadata COPY carries an explicit empty tag via `getCopyObjMetadata`, but only defaults its timestamp to object ModTime for nonempty tags.
4. `CopyObjectHandler` branches on nonempty tags. Production getCopyObjMetadata + SDK Core.CopyObject sends tagging REPLACE but no metadata directive, so its default metadata map retains the old timestamp. A peer using metadata REPLACE additionally loses that timestamp before the handler comparison. Cover both shapes; the empty-value skip affects both.
5. `PutObjectHandler` and `NewMultipartUploadHandler` parse the source timestamp but never persist it in metadata. Multipart completion already rechecks the upload's persisted metadata against the addressed destination version under the object lock.
6. `getReplicationAction` compares tag values/counts, not their ordering time. An empty-to-empty deletion revision can be declared complete without transmitting the revision.
7. `checkPreconditionsPUT` skips matching version/ETag for non-SSE-C replicas even when their tag revision is newer. This affects PUT and multipart initiation. Explicit client If-Match/If-None-Match checks must still apply.
8. `replicateObject`'s completion metadata callback copies nonempty `ri.UserTags` from the old queue snapshot. A deletion committed before this worker's current-object read can be overwritten at acknowledgment. A status update must retain the tags read under its own metadata lock.

The existing storage fix (`3ce831925`) already supplies `reconcileStoredObjectTags` in PUT, COPY, multipart completion and all-pool reconciliation. Its strict ordering keeps stored state on equal timestamps and keeps a valid stored revision against missing/invalid incoming timestamps. Reuse these gates, do not replace them.

## Proposed minimal changes

### A. Produce local revisions

In both PUT tagging and DELETE tagging handlers, allocate opts.UserDefined if needed and assign a single UTCNow RFC3339Nano tag revision for each authorized mutation, independent of current replication selection. Use the same time for ReplicationTimestamp when replication is selected. This also covers empty PUT tagging and mutations while replication is disabled. Persist through existing PutObjectTags/DeleteObjectTags locks. Ordinary COPY must write an empty REPLACE tag and a fresh tag timestamp too.

Under the existing storage write lock, guard local tagging revisions against regression: a valid supplied tag revision not strictly after the valid stored revision is advanced to stored + 1ns. Do this in er.PutObjectTags; for multi-pool writes, z.PutObjectTags first computes one revision strictly beyond every addressed copy and passes that identical value to all sets. This is necessary because ordinary replication source reads/returned primary ObjectInfo can observe one physical pool; merely allowing different pool revisions with equal values can send a revision older than an already-replicated tombstone. Only explicit valid local tag revisions are advanced; replicated PUT/COPY/multipart retain strict source ordering, and direct storage calls without a supplied revision retain current legacy semantics. Generate before locking as today; the guard runs under the lock. Preserve the requested map from unintended shared mutation. This is a per-object monotonic guard in the existing RFC3339Nano domain, not a new wire clock. Equal independent remote conflicting revisions still keep stored state; arbitrary distributed clock skew is not totally ordered by this protocol.

### B. Transport complete state

Move tag timestamp selection outside the nonempty-value branch in putReplicationOpts. Use recorded RFC3339Nano time when present even for an empty value. Without a recorded revision, retain the existing ModTime fallback only for nonempty tags; empty + no revision sends no timestamp. This avoids inventing a tombstone for never-tagged/historically unordered objects. Malformed recorded timestamps fail option construction rather than being silently treated as fresh.

Use the same selection for metadata COPY; a small shared timestamp helper is acceptable to prevent inconsistent error/fallback rules. Preserve explicit empty tag REPLACE metadata. Multipart initiation retains this timestamp even though SourceMTime is cleared.

When getReplicationAction sees a recorded tag revision after the existing identity/full-copy checks, select metadata replication even if visible values match: HEAD does not expose that revision. Keep this for empty AND nonempty states. Example: destination key=X@T1, source deleted at T2 and re-added key=X@T3; if the equal nonempty state skips T3, delayed delete T2 incorrectly removes X. An empty-only condition does not fix ordered deletion/re-addition. Preserve existing null-version resync exclusion. Cost: every explicitly scheduled retry/heal/resync for an object with a recorded revision may require a metadata COPY, even if already converged. It does not create a background retry loop: queueReplicationHeal returns early for Completed objects without requested resync (bucket-replication.go around 3758), and replicateObject requeues only failed results (around 1305). Successful copies remain Completed. Never-tagged objects retain the old skip optimization under the preceding rule. Accept the extra metadata I/O during explicit resync as the smallest correctness-complete option; avoid introducing an authenticated HEAD revision protocol solely as an optimization.

Remove the stale ri.UserTags assignment from replication completion metadata write-back. The callback changes replication status only; existing metadata write-back preserves the current tags and timestamp.

### C. Accept, order, and persist

COPY captures the stored UserTags and timestamp before metadata reconstruction. For a trusted replication request with a nonzero source timestamp, install the incoming tag value (including empty) with that timestamp, then reuse reconcileStoredObjectTags against the captured state. A missing source timestamp preserves stored state for metadata COPY. Storage rechecks under its lock, including all pools. Equal timestamps keep stored state. Ordinary COPY generates a fresh revision for its chosen tags, including empty.

Ensure the final encMetadata merge cannot restore a stale tag timestamp over the accepted pair (SSE-C rotation snapshots reserved keys). Reconcile the timestamp entry in encMetadata with the tag decision before merging, using the existing lock-timestamp pattern.

PUT and multipart initiation persist a nonzero parsed trusted source timestamp into the existing metadata map before entering storage. Their existing lock/reconcile flags retain the newest state. Completion takes tag state from the persisted upload, not client-supplied completion headers, and orders it again against current state.

For trusted REPLICA PUT/multipart initiation, a valid source tag timestamp strictly newer than the destination's stored tag revision makes matching version/ETag insufficient to skip the request. Use the existing olderThan predicate (zero never wins, valid source beats missing/invalid stored). Preserve explicit If-Match/If-None-Match and existing SSE-C behavior. Duplicate/equal/older non-SSE-C writes may keep their existing no-op/412 behavior; the sender treats these as already delivered. Completion does not set PreserveETag and does not need a new duplicate exception. This narrow exception can re-upload data to carry a tag-only revision; normal metadata work uses COPY, while full retransmission must not silently acknowledge a newer revision it did not persist.

### D. Boundaries and compatibility

R4 owns object-api-options.go SSE-KMS common-field preservation. R5 will not implement it. R4 will provide a reviewed patch for isolated combined KMS verification. R5 owns the handlers, sender and tag-related duplicate exception.

Compatibility: equal-timestamp COPY consistently keeps stored state, including unqualified and explicit null requests; this aligns with existing storage tie behavior and changes the old unqualified handler incoming-wins tie. Preserve the existing tag trust predicate (trusted replication marker) and stronger ReplicaLockReconcile predicate (trusted marker + REPLICA and addressed version), without expanding trust. Production replication supplies both.

Scope limitations: ordinary full object PUT/multipart creation retain their existing nonempty ModTime fallback; this change does not order independent unversioned content replacements against one another. Existing tag-filter target eligibility can exclude post-deletion empty tags; target-selection semantics are not changed here. This work guarantees correct tag ordering along selected per-hop replication requests, not every replication-rule configuration. Destination bucket-default/global-auto KMS is applied before copyDstOpts (object-handlers.go 1425-1435), so COPY can depend on R4 even when the sender does not forward an explicit encryption header. PUT/multipart KMS also require R4.

No migration: historical deletions with missing/wrong timestamps have irrecoverably lost ordering information. We do not invent historical deletion times or rewrite production state. A fresh authenticated tagging mutation after upgrade produces an ordered state. Upgrade both sender and receiver for complete guarantees; older peers may continue to drop empty revisions. No main merge, push, release or deployment is authorized here.

## Verification matrix

- Re-run parent overlay on exact HEAD; preserve raw failures. Extend temporary reproduction for PUT/multipart lost persistence, empty-to-empty sender decision, and matching-content newer revision skip.
- Signed HTTP tag PUT/delete, active replication and no selected replication, empty PUT, repeated DELETE; check response and persisted tag/time, worker scheduling when active.
- COPY old/new/equal/missing/invalid source time, empty/nonempty, metadata COPY/REPLACE, explicit UUID/null version; protect unrelated/latest versions and local empty COPY.
- Production putReplicationOpts/SDK headers and actual metadata sender requests: explicit empty tombstone, nonempty legacy ModTime fallback, no fabricated empty legacy revision, nanoseconds, malformed timestamp. Equal empty values must still send ordered deletion; equal nonempty values must send re-addition revisions to defeat intervening delayed deletions. Pin actual SDK metadata COPY headers, and cover peer metadata REPLACE independently. Exercise retry after failed send.
- PUT and multipart through signed requests/SDK: first receipt, newer removal, older replay after removal, duplicate receipt, missing timestamp. For multipart, mutate tags between initiation and completion and check final disk state.
- Multi-pool real-storage fixture with duplicate UUID/null versions, newer tag state in secondary pool, all-pool persistence/retirement; reuse current tag storage suite and failure-closed coverage. Include a deterministic update after handler snapshot to show storage lock recheck. A local DELETE-to-PUT inversion with supplied older timestamp must produce one revision greater than the maximum across pools, in both response and every stored copy.
- Old queued replication event followed by deletion: process event and confirm source completion callback cannot restore tags; check tag/time after reread.
- Run related replication trust, Object Lock/SSE-C retransmit, tag storage and API precondition tests; targeted race tests, gofmt, git diff --check. No whole-repository tests in parallel with other R tasks without need.
- KMS combined dependent tests only after R4 reviewed change is available. Report R5-only and combined results separately.

## Work and acceptance

Estimated 2–4 engineer days including replication boundary tests and review. The patch should remain localized; added regression code is expected to exceed production LOC. Completion requires a reviewable diff, actual Opus model/effort record, same-plan consensus, meaningful persisted-state tests, and explicit local versus merge/release state. Establishes per-hop behavior with deterministic clock/commit interleaves; not production multi-site or real host-clock-skew acceptance.

## v2 review focus

See `opus-v1-response.md` for every blocking/nonblocking disposition and exact counterarguments. R1/R3/R4/R5 accepted with concrete changes. R2 is disputed as proposed: empty-only forced transfer is insufficient for same-value re-addition after deletion, and Completed scanner gates bound work. Please adjudicate on this v2 hash, not on general preference for avoiding metadata I/O. Do not treat unresolved disagreement as consensus.
