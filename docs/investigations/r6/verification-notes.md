# R6 verification notes and evidence boundaries

## Reproduction evidence before acceptance

Raw evidence directory: `/Users/vonng/tmp/silo-r6-20260915-aa3f/`.

| Log | What it establishes |
|---|---|
| baseline.log | Old task shape skips DELETE; wrong HEAD/outcome fields and already-complete behavior. Initial storage fixture attempts hit the host capacity threshold. |
| baseline-mrf.log | After adapting fixture capacity, existing scanner/heal recovery passes. Actual disk-MRF replay drops a real marker+405. Old-shaped task queues no MRF. The canonical per-target creation-result assertion was later retracted as a disk-loss inference; see decisions-v2.md. |
| pr184-probes.log | Exact PR #184 fixes old successful delivery but leaves HEAD failure, failed resync stamp and already-complete retry errors. |
| pr184-mrf.log | PR canonical disk-MRF recovery works; failed old-shaped purge still queues no MRF. |
| v2-unrecorded-purge.log | V2 can rewrite creation metadata with two empty target statuses when disk purge state is absent. Real source/target storage, no simulated metadata layer. This led to v3 review and the explicit empty update payload. |

The temporary baseline probes were developed with two fixture corrections (unused import, then missing ReplicationStats initialization). Those development failures are not treated as product evidence. Likewise the initial in-memory `creation=PENDING` observation does not establish a disk-state loss; Opus v1 corrected that inference. Opus v3 then corrected the earlier multi-target regex proof after the real storage counterexample.

## What the local storage tests exercise

The suite uses the existing single-drive and 16-drive erasure fixtures, the real signed source DELETE handler, real source/target marker metadata and a real minio-go client over HTTP. Target HTTP adapters call the real ObjectLayer, and can reject a request, be marked offline, or close the connection **after** removing the marker. They are controlled replication target adapters, not three independently booted SILO site-replication daemons. The target adapters do not exercise receiver authentication or the complete target HTTP router; those were not changed by this patch.

`saveMRFEntries` writes real MRF files to registered fixture drives. Tests read and check the encoded record, re-persist it because loadMRF consumes the file, construct a new empty ReplicationPool, and call queueMRFHeal. The real GetObjectInfo(VersionID) returns marker metadata with 405 and the queued task goes through the actual replication worker channel. The test explicitly receives that task and invokes production replicateDelete to control each failure/recovery round; it does not start the long-running background worker loop. Tests drive persistence directly, rather than waiting for the five-minute timer. They prove pool replacement/disk reload, not process crash or power-loss durability.

Negative MRF lookups wait for the actual lookup and assert no task arrives within a bounded observation window. Missing, read-error, nonmarker, wrong bucket/object/version, empty info and zero timestamp responses are covered. Null/empty marker version identities that return ObjectNotFound are explicitly outside the new valid-405 gate.

The multi-target fixture checks one target complete while the other fails/offline, persisted per-target purge states, no extra delivery to the successful target, then recovery and marker removal on source and both targets. The separate partial-fan-out test preserves the complete creation block and its timestamp. It does not claim to fix the pre-existing purge-status subset replacement.

## Environment failures and fixes to test inputs

- During the exploratory broad run, the host reported a high used-space percentage. Six unrelated DELETE tests aborted at seed PutObject with `Storage reached its minimum free drive threshold`: TestDeleteObjectConditional, TestDeleteObjectConditionalWithReadQuorumFailure, TestDeleteObjectConditionalVersioned, TestDeleteObjectsVersioned, TestDeleteObject, TestDeleteObjectVersionMarker. See replication-suite.log. That exploratory broad suite was not a pass. After host free space recovered, all six exact tests passed on the final integrated source, without changing those tests or production capacity policy; see verification/rebased-delete-verification.json and rebased-delete-recheck.log. This recheck is not a full cmd-suite run.
- R6 storage fixtures reuse the repository's tagTestCapacityDisk adapter: Total/Used are adapted to the actual free space; object/MRF metadata and data still go to real disks. This isolates host occupancy from replication semantics.
- A test link later failed with `no space left on device` (head-exits.log). Thirteen old Go cache artifacts containing this exact worktree path, totaling 1495 MiB, were removed after identification. No repository data or other tasks' cache entries were selected. The manifest is owned-cache-cleanup.json in the raw evidence directory. Available space also changed due to unrelated host activity; we do not attribute the whole increase to this cleanup.
- The HEAD quorum test initially used HTTP 503 and expected a quorum-code fall-through. Existing ErrorRespToObjectError classifies 503 as backend-down before its S3 code conversion. Final tests separately cover 503 not-ready failure and a non-503 SlowDownRead code reaching the existing quorum branch. No production change to that classification was made.
- Initial lint reported five test-style issues, subsequently corrected. v2-scope-regression.log is an intermediate failed run, including the still-unfixed v3 counterexample and the initial quorum test expectation. It is not final acceptance.

## Limits retained after R6

No three-daemon/full-mesh SR deployment, process restart/crash, cross-region test, production repair, merge or release is claimed. The following pre-existing mechanisms remain separate: absent configured clients and omitted purge target state, target-scoped resync replacing the purge subset, loss of replication tracking, replica relay behavior, delayed marker **creation** after a purge without tombstones, local source metadata-write failures relying on scanner recovery, generic status parser robustness, and new MRF timer/backoff/observability design.

Current normal producers already emit canonical version purges. The old in-memory task shape is not serialized through restart. Its support is robustness compatibility; marker MRF is the directly active retry-path repair. Do not reuse PR #184's unqualified permanence, request-rate or site-count claims as acceptance conclusions.
