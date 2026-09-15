# R6 research and PR #184 review

## Source identity

- Worktree baseline / freshly fetched origin/main: `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`.
- PR URL: https://github.com/pgsty/silo/pull/184
- PR head at this review: `6addf9eb916b5a4b837480cf534cd1efa5407d3c`; OPEN, no reviews returned by GitHub API. Its recorded PR base OID `89637554d60c27cfc51d2281d0a4fe15e415f06d` is older than the fetched branch; current worktree source wins.
- Production diff is exactly 24 insertions/11 removals in bucket-replication.go relative to this baseline. The PR additionally changes tests and adds a convergence write-up.
- No production edits were made during research. All test overlays and raw logs: `/Users/vonng/tmp/silo-r6-20260915-aa3f/`.

## Reproduced target-path findings

`review_probe_test.go` is a temporary in-package probe using a real minio-go HTTP client and counted HEAD/DELETE requests. `base-overlay.json` adds the probe; `pr-overlay.json` replaces only bucket-replication.go with the exact PR version for comparison.

| Probe | Baseline | PR #184 | Required behavior |
|---|---|---|---|
| old shape, creation COMPLETED, purge PENDING, successful target | DELETE 0; creation COMPLETED; purge PENDING | DELETE 1; creation COMPLETED; purge COMPLETE | send purge and finish purge state |
| old shape, creation PENDING, HEAD 403 | creation FAILED; purge PENDING | same | purge must not overwrite creation |
| old shape, ExistingObjectReplicationType, DELETE 403 | purge still PENDING | purge FAILED, but current reset timestamp recorded | failed operation must not mark successful resync |
| old shape, purge already COMPLETE, creation PENDING | two HTTP calls, creation FAILED | two HTTP calls, purge becomes FAILED | skip completed purge, preserve states |

Raw logs: `baseline.log`, `pr184-probes.log`. The first baseline run also attempted the existing storage tests, which failed at seed PutObject due to the host's free-space percentage threshold. This is a fixture/environment failure, not replication evidence. Follow-up overlays use the existing `tagTestCapacityDisk` adapter: only DiskInfo total/used capacity is adapted; actual storage writes remain on test disks. Host df showed about 10 GiB available but 100% used by filesystem percentage rounding.

## Additional code-review findings

- Outer `replicateDelete` chooses operation outcome using VersionID, so PR's preserved creation COMPLETED can conceal old-shape purge FAILED from ObjectReplicationFailed and queueMRFSave. Per-target stats comparisons also use creation fields.
- `VersionPurgeComplete` is `COMPLETE`, while ordinary replication completion is `COMPLETED`. A raw string cast does not take the completion branch of ReplicationStats.Update.
- The old marker-shape request sends ReplicationDeleteMarker=true for purges. Today's canonical purge tasks use false and explicit VersionID. Reuse that existing wire meaning to avoid creating a marker at the absent-version fallback after an ambiguous successful DELETE.
- MRF persists entries on actual local drives, removes the loaded file, then asynchronously reads each object. MethodNotAllowed accompanies real marker ObjectInfo in erasureObjects.getObjectInfo. Tests must cover save/load/fresh pool and subsequent queueing, not call queueReplicationHeal directly.
- DeletedObjectReplicationInfo currently lacks RetryCount; its ToMRFEntry always serializes zero. Restoring marker MRF makes the existing budget omission reachable on the fast retry path.

## Claims excluded from acceptance

The current DELETE, scanner/heal, and resync producers already build canonical purges. The inherited old-shape test intentionally repairs a legacy PENDING marker with scanner/heal. It therefore contradicts an unqualified claim that all failed purges persist forever or only manual resync repairs them. R6 does not establish the frequency of failures for any site count, nor prove the cited production 405 traffic is entirely caused by these paths.

PR documentation describes missing-client status loss, replica relaying, tombstones and arbitrary-mesh recovery. They are separate mechanisms. This patch's acceptance concerns tracked source fan-out with configured reachable/recoverable targets; it does not establish global multi-site convergence under lost state, nil clients, delayed creation after purge, or process/storage failure at every possible point.

## Real erasure and disk-MRF probes

`review_integration_test.go` reuses the existing signed DELETE/storage fixture, injects a target 403, preserves the intended creation state explicitly, and persists the actual failed entry. A fresh ReplicationPool then loads the disk record and attempts queueMRFHeal. It validates source and target absence after recovery.

- Baseline canonical failure: creation result PENDING (lost prior COMPLETED), purge FAILED, one MRF entry. Real source lookup yields matching marker ObjectInfo and MethodNotAllowed. Disk-MRF replay schedules no task (probe fails as expected).
- Baseline old-shaped failure: creation COMPLETED, purge still PENDING, zero MRF entries (probe fails as expected).
- PR canonical failure: creation result is still empty (disk preservation clarified below). Disk-MRF replay now runs and source/target removal succeeds on both single-drive and 16-drive fixtures.
- PR old-shaped failure: creation COMPLETED and purge FAILED, but zero MRF entries. This directly confirms the outer-function omission.
- The existing baseline old-shape recovery test PASSES using its explicit scanner/heal fallback, on both storage fixtures. Therefore scanner recovery is retained as observed evidence, not only a code inference.

Raw logs: `baseline-mrf.log`, `pr184-mrf.log`. Temporary probe development first hit an unused import and then an uninitialized statistics fixture; those were corrected without changing production code. They do not count as defect evidence. Final logs above contain actual assertion outcomes.

## Correction after independent Opus review

Opus v1 B1 disproved the interpretation that a canonical purge's empty creation result means the disk creation state was lost. It is an intentional no-update signal in xlMetaV2.DeleteVersion: the disk block is retained if the composite creation state is empty. `baseline-mrf.log` and `pr184-mrf.log` include a temporary assertion that was too strong; their empty per-target result is not a disk-loss finding. v2 keeps that existing no-update signal, tests the actual persisted block/timestamp, and avoids introducing the partial-fan-out overwrite that a creation-status pin would cause. See decisions-v2.md for all dispositions.
