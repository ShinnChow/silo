# R5 repair plan v1 — tag deletion and ordered replication

Status: proposed, no production implementation before real Opus consensus.
Base: `9ebe81c1b3611f9cc73e676b5b741c2be62c467a` (local and GitHub main checked).

## Problem and necessity

A tag value and its `x-minio-internal-tagging-timestamp` form one state, including an empty value. Successful DeleteObjectTagging currently leaves the old timestamp. A delayed, authenticated metadata COPY newer than that old timestamp restores deleted tags. The parent and this worktree reproduce this through signed HTTP with real single-drive and 16-drive storage. An empty incoming COPY is also ignored.

The consequence is persistent incorrect user metadata on the source or replicas, affecting tag-based access/lifecycle behavior. Repair is justified before claiming replication correctness. It is not a reason to change the supported PGSTY dependency graph, storage format, encryption protocol, or Object Lock behavior.

## Current chain and additional gaps

1. `PutObjectTaggingHandler` stamps only when replication is selected. `DeleteObjectTaggingHandler` never stamps. Both storage methods write the value under the existing object lock; multi-pool PutObjectTags updates the addressed version in each copy.
2. `putReplicationOpts` sets the timestamp only inside nonempty UserTags. Multipart uses this builder and clears SourceMTime at initiation, so using initiation time as a tag fallback would be wrong.
3. Metadata COPY carries an explicit empty tag via `getCopyObjMetadata`, but only defaults its timestamp to object ModTime for nonempty tags.
4. `CopyObjectHandler` branches on nonempty tags; its REPLACE metadata map also loses the previous timestamp before comparison.
5. `PutObjectHandler` and `NewMultipartUploadHandler` parse the source timestamp but never persist it in metadata. Multipart completion already rechecks the upload's persisted metadata against the addressed destination version under the object lock.
6. `getReplicationAction` compares tag values/counts, not their ordering time. An empty-to-empty deletion revision can be declared complete without transmitting the revision.
7. `checkPreconditionsPUT` skips matching version/ETag for non-SSE-C replicas even when their tag revision is newer. This affects PUT and multipart initiation. Explicit client If-Match/If-None-Match checks must still apply.
8. `replicateObject`'s completion metadata callback copies nonempty `ri.UserTags` from the old queue snapshot. A deletion committed before this worker's current-object read can be overwritten at acknowledgment. A status update must retain the tags read under its own metadata lock.

The existing storage fix (`3ce831925`) already supplies `reconcileStoredObjectTags` in PUT, COPY, multipart completion and all-pool reconciliation. Its strict ordering keeps stored state on equal timestamps and keeps a valid stored revision against missing/invalid incoming timestamps. Reuse these gates, do not replace them.

## Proposed minimal changes

### A. Produce local revisions

In both PUT tagging and DELETE tagging handlers, allocate opts.UserDefined if needed and assign a single UTCNow RFC3339Nano tag revision for each authorized mutation, independent of current replication selection. Use the same time for ReplicationTimestamp when replication is selected. This also covers empty PUT tagging and mutations while replication is disabled. Persist through existing PutObjectTags/DeleteObjectTags locks. Ordinary COPY must write an empty REPLACE tag and a fresh tag timestamp too.

This preserves the existing wall-clock conflict model, not a new distributed causal clock. The timestamp is generated before the storage lock as in existing PUT tagging. Reviewer should explicitly assess whether a commit-time generation change is necessary for this scoped repair; if necessary, revise before implementing. Clock skew and simultaneous conflicting equal revisions cannot be completely ordered by this protocol.

### B. Transport complete state

Move tag timestamp selection outside the nonempty-value branch in putReplicationOpts. Use recorded RFC3339Nano time when present, otherwise object ModTime (also for empty legacy objects). Malformed recorded timestamps fail option construction rather than being silently treated as fresh.

Use the same selection for metadata COPY; a small shared timestamp helper is acceptable to prevent inconsistent error/fallback rules. Preserve explicit empty tag REPLACE metadata. Multipart initiation retains this timestamp even though SourceMTime is cleared.

When getReplicationAction sees a recorded tag revision after the existing identity/full-copy checks, select metadata replication even if visible values match: HEAD does not expose that revision. Do not change the existing null-version resync exclusion. This is an extra COPY only for already-scheduled work with an explicit revision, including retry/heal; it does not add scans or network calls on ordinary object reads. Avoid a new HEAD protocol merely to save that COPY.

Remove the stale ri.UserTags assignment from replication completion metadata write-back. The callback changes replication status only; existing metadata write-back preserves the current tags and timestamp.

### C. Accept, order, and persist

COPY captures the stored UserTags and timestamp before metadata reconstruction. For a trusted replication request with a nonzero source timestamp, install the incoming tag value (including empty) with that timestamp, then reuse reconcileStoredObjectTags against the captured state. A missing source timestamp preserves stored state for metadata COPY. Storage rechecks under its lock, including all pools. Equal timestamps keep stored state. Ordinary COPY generates a fresh revision for its chosen tags, including empty.

Ensure the final encMetadata merge cannot restore a stale tag timestamp over the accepted pair (SSE-C rotation snapshots reserved keys). Reconcile the timestamp entry in encMetadata with the tag decision before merging, using the existing lock-timestamp pattern.

PUT and multipart initiation persist a nonzero parsed trusted source timestamp into the existing metadata map before entering storage. Their existing lock/reconcile flags retain the newest state. Completion takes tag state from the persisted upload, not client-supplied completion headers, and orders it again against current state.

For trusted REPLICA PUT/multipart initiation, a valid newer source tag timestamp makes matching version/ETag insufficient to skip the request. Preserve explicit If-Match/If-None-Match and existing SSE-C behavior. Duplicate/equal/older non-SSE-C writes may keep their existing no-op/412 behavior; the sender treats these as already delivered. Completion does not set PreserveETag and does not need a new duplicate exception.

### D. Boundaries and compatibility

R4 owns object-api-options.go SSE-KMS common-field preservation. R5 will not implement it. R4 will provide a reviewed patch for isolated combined KMS verification. R5 owns the handlers, sender and tag-related duplicate exception.

No migration: historical deletions with missing/wrong timestamps have irrecoverably lost ordering information. We do not invent historical deletion times or rewrite production state. A fresh authenticated tagging mutation after upgrade produces an ordered state. Upgrade both sender and receiver for complete guarantees; older peers may continue to drop empty revisions. No main merge, push, release or deployment is authorized here.

## Verification matrix

- Re-run parent overlay on exact HEAD; preserve raw failures. Extend temporary reproduction for PUT/multipart lost persistence, empty-to-empty sender decision, and matching-content newer revision skip.
- Signed HTTP tag PUT/delete, active replication and no selected replication, empty PUT, repeated DELETE; check response and persisted tag/time, worker scheduling when active.
- COPY old/new/equal/missing/invalid source time, empty/nonempty, metadata COPY/REPLACE, explicit UUID/null version; protect unrelated/latest versions and local empty COPY.
- Production putReplicationOpts/SDK headers and actual metadata sender requests: explicit empty tombstone, legacy ModTime fallback, nanoseconds, malformed timestamp. Equal empty values must still send ordered deletion; exercise retry after failed send.
- PUT and multipart through signed requests/SDK: first receipt, newer removal, older replay after removal, duplicate receipt, missing timestamp. For multipart, mutate tags between initiation and completion and check final disk state.
- Multi-pool real-storage fixture with duplicate UUID/null versions, newer tag state in secondary pool, all-pool persistence/retirement; reuse current tag storage suite and failure-closed coverage. Include a deterministic update after handler snapshot to show storage lock recheck.
- Old queued replication event followed by deletion: process event and confirm source completion callback cannot restore tags; check tag/time after reread.
- Run related replication trust, Object Lock/SSE-C retransmit, tag storage and API precondition tests; targeted race tests, gofmt, git diff --check. No whole-repository tests in parallel with other R tasks without need.
- KMS combined dependent tests only after R4 reviewed change is available. Report R5-only and combined results separately.

## Work and acceptance

Estimated 2–4 engineer days including replication boundary tests and review. The patch should remain localized; added regression code is expected to exceed production LOC. Completion requires a reviewable diff, actual Opus model/effort record, same-plan consensus, meaningful persisted-state tests, and explicit local versus merge/release state. Not production multi-site acceptance.

Review questions: Are A's existing local timestamp generation semantics sufficient here? Are B's forced metadata synchronization, stale ACK removal and C's narrow duplicate exception necessary and correctly scoped? Is any incoming/forwarding/multi-pool path still able to lose or revive the deletion revision?
