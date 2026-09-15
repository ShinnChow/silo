# R6 plan v1 — marker purge operation and MRF recovery

Date: 2026-09-15. Implementation has NOT started; this is the review candidate.
Baseline: `9ebe81c1b3611f9cc73e676b5b741c2be62c467a` (clean detached worktree and freshly fetched origin/main agree).
PR #184: OPEN; head `6addf9eb916b5a4b837480cf534cd1efa5407d3c`, code commit `96b21557a85cd4a615ba8a797bfc4c556e413db4`.
Evidence directory: `/Users/vonng/tmp/silo-r6-20260915-aa3f/` (PR JSON/diff, baseline overlays/probes/logs).
Current PGSTY support policy is read from local AGENTS.md, copied verbatim from the main checkout's ignored AGENTS.md. No dependency or supported-stack change.

## Observed mechanisms and scope

1. `replicateDeleteToTarget`: an old-shaped task has VersionID empty, DeleteMarkerVersionID set, creation target COMPLETED, purge target PENDING/FAILED. The creation early-return suppresses its DELETE. Offline/RemoveObject error/success select the wrong field for this shape. HEAD-not-ready always overwrites creation status, including purges. The resync defer uses creation COMPLETED even when purge fails; purge COMPLETE early-out only recognizes nonempty VersionID.
2. `replicateDelete`: aggregate audit/event/MRF status and the stats change check still select on VersionID/creation status. Fixing only the target routine leaves a failed old-shaped purge reporting Completed and not entering MRF.
3. `queueMRFHeal`: disk entries are consumed, GetObjectInfo(marker version) returns real metadata plus MethodNotAllowed, and every error is discarded. Direct queueReplicationHeal is NOT an MRF test.
4. Current DELETE producer, scanner/heal, and resync already construct the canonical VersionID purge shape. Existing `TestReplicateDeleteMarkerPurge/recover_legacy_true` proves scanner/heal can recover old state. This is NOT evidence that every failed purge is permanently stuck, nor a defect exclusive to >2 sites.
5. PR #184 has the right state-based classification and 405 recovery direction, but misses the outer status, HEAD failure, resync defer, already-complete guard, bounded delete retry propagation, and real MRF coverage. Its existing-marker test confirms one attempt through a helper HTTP endpoint, not a three-site deployment. Its prose makes stronger permanence/topology claims than the current baseline proves.

## Proposed minimal implementation

### A. One operation classification, all exits

Add a small `DeletedObjectReplicationInfo.isVersionPurge()` helper: true when VersionID is nonempty OR DeleteMarkerVersionID is nonempty and the task's composite VersionPurgeStatus is nonempty. Use the task-level decision in both outer and target functions; do not classify a multi-target task differently merely because one target lacks a map entry.

For purges preserve `rinfo.ReplicationStatus = rinfo.PrevReplicationStatus` on every exit. Only creation changes this field; only purge changes VersionPurgeStatus. Use this classification for completed early-outs, offline/error/success and resync success. Do not stamp the current resync reset for a failed purge. Existing creation semantics (HEAD 405 means already created, readiness gate, quorum fall-through) remain.

Route ALL purges through the canonical permanent-delete request already produced by today's handlers: explicit version ID, `ReplicationDeleteMarker=false`. Perform marker HEAD/readiness probes only for creations. Purge authorization/failure is determined by the DELETE itself. This removes the obsolete old-shape HEAD error path, prevents a lost-response retry from re-creating an absent marker, and needs no new wire header or receiver change. Keep current RemoveObject 404/idempotency semantics. Preserve actual target failure in Err, including offline error where appropriate.

In the outer routine use purge outcomes for audit/event/MRF and for per-target change detection. Map internal purge COMPLETE to operation COMPLETED only when passing an operation status to the existing statistics/event/audit logic; stored purge metadata remains COMPLETE. Feed per-target old/new operation status into stats rather than selecting changes from the unrelated creation status. Keep existing stats policy, no new metrics framework.

For pre-fan-out exits: config/decision failures remain not-tracked; lock failure retains MRF scheduling; missing configured clients remain logged/skipped and are a documented separate state-preservation limitation. Do not rewrite general `getReplicationState` target merging in this issue. Do not claim global convergence for nil clients or resync narrowed to one of many targets.

### B. Real MRF healing and bounded retries

Accept GetObjectInfo MethodNotAllowed only when returned ObjectInfo is a delete marker with nonempty matching bucket/object/version identity. QueueReplicationHeal still rejects zero ModTime. Other errors and invalid/empty ObjectInfo are not queued. Reuse the existing disk persistence/load/queue path; no timer/backoff redesign or format change.

Carry a RetryCount in the in-memory delete task, pass it from queueReplicationHeal, increment it when a failure/lock error is submitted to MRF, and include it in ToMRFEntry (whose disk format already has RetryCount). Respect the existing mrfRetryLimit and drop accounting; after budget exhaustion the scanner can still start a fresh heal. This closes the retry-count omission exposed by re-enabling marker MRF. No additional persistence schema.

### C. Regression and acceptance matrix

1. Target operation table: create (new/HEAD405/completed/readiness failure/quorum), canonical object purge, canonical marker purge, old marker purge. Pending/failed/completed purge, creation pending/completed/replica; success, DELETE403/405/503, offline, absent version, response lost after real removal, resync success/failure. Verify HTTP method/version/header, exact status fields and reset marker. No HEAD for purges.
2. Full outer call: old/new purge shapes, source/target real erasure metadata, first failure yields purge FAILED while creation state survives; MRF queue entry exists; correct operation status accounting.
3. Persist that MRF entry with saveMRFEntries, create a fresh ReplicationPool with no in-memory entry, then queueMRFHeal -> loadMRF -> real GetObjectInfo(405) -> QueueReplicationHeal -> delete queue -> replicateDelete. Initially still failing, entry reappears with increased retry count. Recover target, reload/replay and prove source AND target marker versions removed; repeated successful purge remains absent. Repeat in single-drive and 16-drive fixtures. No direct queueReplicationHeal substitute for MRF proof.
4. Creation MRF: failed marker creation metadata also travels the real disk MRF path and reaches the target. Negative MRF lookups (missing/corrupt/nonmarker 405/empty identity) do not schedule mutations. Verify retry budget still drops with counters, scanner fallback works.
5. Two independent HTTP targets plus source in local erasure fixtures: one target succeeds, the other fails/offline; source remains with per-target COMPLETE/FAILED purge states and unchanged creation states. Restore failed target via persisted MRF; successful target is not resent, both targets and source are absent at completion. This is 3-endpoint fan-out/metadata evidence, not a production three-daemon SR mesh, process-crash/power-loss or cross-region test. Report that distinction.
6. Run focused replication/delete/MRF/resync tests and race tests, then build/vet appropriate to changed code. Do not run other tasks' whole-repository suites concurrently. Tests must fail on baseline for the repaired paths; existing canonical purge and scanner recovery remain covered.

## Existing state, compatibility, and delivery

Pending/failed old marker metadata is consumed using the normal heal/MRF machinery; no bulk migration or live object rewrite. Disk entries whose marker lookup was previously dropped can be rediscovered by the scanner. Already missing/erased replication tracking or nil target clients cannot be recovered by this patch alone. A durable receiver tombstone for a delayed *creation* after a purge, replica relay behavior, full arbitrary-mesh convergence, and new backoff/observability are separate issues.

Reuse PR #184's operation-state separation and valid-405 intent, and adapt its useful target/legacy convergence tests; do not copy its unproven permanence claims or adopt the entire PR blindly. Expected work: 1 production file plus focused tests and investigation records (helper placement may touch bucket-replication-utils.go if clearer). Branch only after exact-version Opus consensus. Normal implementation/tests authorized; merge, publish, deploy, and live storage rewrites excluded.

## Review request

Verify the classification and every exit against the exact source; challenge the canonical-wire choice, status conversion, MRF validity/retry budget, test sufficiency and scope. Respond with explicit GO/GO_WITH_NONBLOCKING_NOTES/REVISE for plan-v1.md and list blockers separately. Agreement must be on this exact plan hash. No implementation before resolving blockers.
