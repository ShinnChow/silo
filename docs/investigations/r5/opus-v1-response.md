# Response to actual Opus v1 review

The original review is retained verbatim in opus-v1-review.md, with actual model identity/usage in opus-v1-metadata.json and the full stream in the raw evidence directory. Result was REQUEST_CHANGES, five blockers. No implementation was started and no consensus is implied by the response below.

## Blocking items

| Item | Disposition for v2 |
|---|---|
| R1 empty/no-revision ModTime fallback | Accepted. Never synthesize a revision for empty tags without one. Keep only existing nonempty ModTime fallback. Add no-revision wire regression. |
| R2 force metadata only for empty values | Disagree with the proposed restriction; accept the I/O cost warning. Same nonempty values can carry different revisions: X@T1, delete@T2, re-add X@T3. Skipping T3 lets delayed delete T2 incorrectly win. A recorded revision requires delivery for either value. v2 explicitly accepts extra COPY per scheduled/resync invocation. queueReplicationHeal already skips Completed unless resync requested; replicateObject only requeues Failed. Thus the predicate is permanently conservative, but it does not create perpetual background work. Avoiding a new HEAD protocol is the smaller implementation. Re-review required. |
| R3 actual metadata COPY request shape | Accepted after inspecting the pinned minio-go Core.CopyObject/copyObjectDo. getCopyObjMetadata supplies only tagging REPLACE; the SDK adds no metadata directive. Update provenance and test both actual SDK shape and peers using metadata REPLACE. |
| R4 local revision inversion | Accepted and strengthened for uniform multi-pool persistence. er.PutObjectTags advances a valid supplied revision beyond stored time under lock. z.PutObjectTags computes one value beyond every addressed copy before writing, so the response, ordinary source read and all copies agree. Per-pool-only guards can produce different times; mergedPoolObjectInfo is not every ordinary read path, so relying on later merge is insufficient for a precise source revision. Direct calls without valid supplied revisions keep old semantics. |
| R5 duplicate suppression wording | Accepted. Explicitly use strictly-newer-than-stored, with existing olderThan semantics. Preserve client preconditions; only trusted REPLICA source timestamps can relax version/ETag duplicate suppression. Document possible data re-upload cost. |

## Nonblocking items

- Equal times: document stored-wins consistency across COPY and storage, including null/unqualified requests.
- Invalid COPY sender timestamp: fail the metadata send with Failed, as PUT option construction does; no silent fallback.
- Tag trust versus replica trust: preserve existing predicates, document production supplies both. No permission relaxation.
- KMS nuance: agree PUT/multipart depend on R4, but disagree that metadata COPY never depends on R4. Destination bucket defaults and globalAutoEncryption inject KMS before copyDstOpts at object-handlers.go 1425–1435. R4 independently reproduced all three explicit/default/auto entrypoints. Do not adopt the inaccurate broader exclusion. Combined tests required.
- Ordinary whole-object replacement/no-version clocks: document unchanged semantics. This plan addresses local tagging mutation and selected per-hop replicated version updates; it does not create a new conflict model for independent unversioned content overwrites.
- Tag-filtered target eligibility: document the pre-existing scope limitation; no selection/rule protocol redesign in R5. Final result must not claim arbitrary configuration convergence.
- ACK: additionally reproduced on both real storage backends in baseline-ack.log. The source revives `key=queued` with the deletion's timestamp after old queue event completion. Remove the stale assignment; storage preserves current state.
- Storage lock recheck: reuse and keep existing error behavior. The tests establish per-hop behavior, not a production multi-site deployment or physical clock-skew experiment.

## Added evidence

`/Users/vonng/tmp/silo-r5-20260915-77ad/matrix_test.go` contains temporary signed HTTP UUID/null tests, exact SDK COPY wire capture, local timestamp inversion, multi-pool deletes and SSE-C rotation. The original matrix fails baseline as expected. `discussion-baseline.log` isolates R2's equal nonempty case, R3's real SDK shape, and R4's commit inversion. A short first compile missed a test import and was corrected; only the subsequent compile/run is behavioral evidence.

The v2 plan, not this commentary, is the next consensus target. Production diff remains empty.
