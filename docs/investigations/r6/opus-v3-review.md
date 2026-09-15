I'll start by reading the key evidence documents and the current implementation state.

I have everything I need. Let me verify one last interaction — whether the delta is pinned by any existing test — and then write the review.

Write is disabled in this session (consistent with your read-only constraint), so the review is delivered here in full.

# Verdict: **GO_WITH_NONBLOCKING_NOTES** — `plan-v3.md`

**Blockers: none.**

Digest caveat: no shell tool in this session, so I could not compute SHA256 and cannot attest `dc9a67fc91b3113fa35218a2f903455807430cc4daf9c78a88d0be6b8fc27058`. Consensus attaches to the content I read — 70 lines, opening `# R6 plan v3 — marker purge operation and MRF recovery`, closing `...including the proof now corrected by the executable counterexample.` — the same procedural caveat the v2 reviewer recorded. I ran no tests; `v2-unrecorded-purge.log` is treated as reported evidence, not as my own execution.

Source reviewed: worktree = `9ebe81c1` + `implementation-v2.diff`, v3 delta **not** applied.

---

## 1. Counterexample verified — and I am withdrawing the general form of the earlier proof

The v2 consensus rested on: empty per-target status ⇒ `"arn=;"` ⇒ no regex match ⇒ empty `Targets` ⇒ empty composite ⇒ guard at `cmd/xl-storage-format-v2.go:1438` never fires. **That holds only for |targets| ≤ 1. It is false for ≥ 2.** The reviewers' multi-target regex proof was too strong; the executable counterexample is correct.

Trace against `replStatusRegex = ([^=].*?)=([^,].*?);` (`cmd/bucket-replication-utils.go:168`), input `arn1=;arn2=;`:
- group 1 lazily reaches the first `=` → `arn1`
- group 2's `[^,]` **accepts `;`** (the class excludes comma, not semicolon), then `.*?` runs to the next `;` → `;arn2=`
- one match spans the whole string → `{arn1: ";arn2="}`

Then `CompositeReplicationStatus` (`:356-379`): non-empty internal, not a legacy literal → `default` → `getCompositeReplicationStatus` sees one bogus entry → **`Pending`**. The `ReplicaTimeStamp` fall-through at `:366-371` cannot rescue it (it only returns `ReplicaStatus` when `replStatus == Completed`). Generalizes: N=1 → `""`; every N ≥ 2 → `Pending`, always via a `;`-prefixed value, so it can never accidentally land on a real status.

**Why the ordinary multi-target tests passed** — plan §v3 is right, with one mechanism refinement: the masking is not in the guard, it is in `FileInfo.DeleteMarkerReplicationStatus()` (`cmd/erasure-metadata.go:670-675`), which returns `""` whenever `!fi.Deleted`, regardless of the composite. `erasureObjects.DeleteObject` sets `deleteMarker=false` when `versionFound && !goi.VersionPurgeStatus.Empty()` (`cmd/erasure-object.go:2100-2101`) — the normal state after the handler's PENDING write. A **second independent masking condition** sits at `:2102` (`else if !goi.DeleteMarker`), so purges of ordinary object versions are never exposed: the hole is confined to delete-marker versions whose on-disk purge status is absent.

Unrecorded path: disk marker has creation metadata only → `deleteMarker` stays `opts.Versioned=true` → `fi.Deleted=true` → `DeleteMarkerReplicationStatus()` = bogus `Pending` → guard at `:1438` fires → `default` branch rewrites `ReplicationStatus` = `arn1=;arn2=;` and `ReplicationTimestamp` = `UTCNow()` (supplied by `:576`, since FAILED ≠ PENDING). Exactly the recorded log: statuses emptied, stamp jumping `15:06:15Z` → `16:06:15.612037Z`.

The fixture is sound: the recreate at `replication-delete-mrf_test.go:258` genuinely removes the marker (`updateVersion=false` → removal branch at `:1457`), the reseed at `:261-264` writes a creation-only block, and `deletion` — captured from the real signed handler DELETE at `:249` — still carries PENDING purge state. `before` is sampled at `:304`, after the reseed.

**Scoping, unweakened:** the normal producer does not demonstrably race. `DeleteObjectHandler` enqueues only after a successful write and derives the task from the returned `objInfo` (`cmd/object-handlers.go:3164, 3222-3242`); `queueReplicationHeal` derives it from current disk state (`cmd/bucket-replication.go:3808-3821`). Reaching the unrecorded shape needs disk/task divergence — e.g. a partial-quorum PENDING write later healed back from a stale shard while the in-memory task survives. Narrow, not impossible, unproven. Two facts bound the blast radius: the damage is **one-shot** (the same failed write records the purge status, masking every later round), and it is erased if the purge ever completes. It remains real metadata corruption while a marker is stuck — garbage per-target creation statuses plus a lost creation timestamp.

## 2. Field selection — correct, with one candid downgrade

| Field | Assessment |
|---|---|
| `ReplicationStatusInternal = ""` | **Load-bearing.** Direct cause; forces the composite to case 3. |
| `Targets = nil` | **Consistency hygiene.** The composite switches on the string, so it changes nothing today; it prevents an internally inconsistent state (empty string, populated map) misleading a future reader. Keep. |
| `ReplicaStatus = ""` | **Defensive, not load-bearing today.** The fallback at `:374-375` is real, but no production producer populates it: `ObjectInfo.ReplicationState()` (`:572-590`) and `ObjectToDelete.ReplicationState()` (`:593-602`) both omit the replica fields, and every delete-task construction site (`bucket-replication.go:2678, 3361, 3813`, `object-handlers.go:3237`, `erasure-object.go:1715, 1758, 1764`) routes through one of them. Keep it — free, and it closes the documented fallback — but it does not repair anything observed. |

`isPurge` is the right gate: one value computed pre-fan-out at `:428`, identical to the one `replicateDeleteToTarget` uses at `:616`. Creations untouched.

## 3. Loss / leak — none found

- **Creation + replica blocks preserved.** Both write sites (`:1396` ventry, `:1438` in-place) sit behind the same now-dead guard, and `:1430-1453` mutates the existing `ver.DeleteMarker.MetaSys` by key, never clearing it. A no-update payload, exactly as the plan says.
- **Purge block / reset map still written** (`:1448-1453`); `updateVersion` unchanged (`:1380` diverts on the non-empty purge status, `:1390` sets it for any non-COMPLETE purge).
- **Successful purge:** composite COMPLETE → `updateVersion=false` → version removed at `:1457`; `:1458`/`:1460` both stay false. Unchanged.
- **ventry path (`:1395-1411`, added at `:1506`) unreachable for purges:** reaching `:1506` requires an `ObjectType` version, and for those `fi.Deleted` is always false via `:2100-2103`.
- **Zero fan-out (nil clients):** two of three assignments are already no-ops; I traced the `ReplicaStatus` case through `:2081/:2084` and `:1380` for replica and non-replica sources — identical outcome with and without the delta. Pre-existing behavior, already an explicit exclusion.
- **No re-derivation risk:** `SetDeleteReplicationState` runs only under `opts.EvalMetadataFn != nil` (`erasure-object.go:2030-2038`, `erasure-server-pool-consistency.go:342-351`); `replicateDelete` never sets it, so emptied fields are not refilled with a PENDING decision.
- **`ReplicationState.Equal`** is used only by `FileInfo.ReplicationInfoEquals` comparing two on-disk infos. No interaction.
- **Only observable change:** `dobjInfo` handed to `sendEvent` at `:604-610` carries empty `ReplicationStatusInternal`/`ReplicationStatus` instead of bogus `PENDING` for multi-target purges (`erasure-metadata.go:160-162`). Strict improvement; that payload never reflected disk state. Worth one line in the PR description.
- **Stats / audit / MRF** are computed from `rinfos` before `drs` (`:548-573`). Untouched.

## 4. Sufficiency — yes, for the class

- `getReplicationState` has exactly **one** production caller (`bucket-replication.go:574`). No second write path to patch.
- With the composite forced empty, the guard is dead for all purges under every (`fi.Deleted`, `updateVersion`) combination — not just the fixture's.
- `versionPurgeStatusesMap` is not exposed to the same misparse: `VersionPurgeStatusInternal()` skips empty statuses (`:147-149`), so `arn=;` never appears there.
- After the delta no known producer emits an empty entry at all: purges write `""`; every `!isPurge` exit of `replicateDeleteToTarget` assigns a status (`:640, 664, 681, 695, 715, 725`); the object path pre-initializes `ReplicationStatus: replication.Failed` (`:1338`). Shared parser untouched, as required.
- No existing test asserts a purge writes a non-empty creation status, so no expected regression. `partial-creation-block` restricts fan-out to one ARN → single entry → composite already `""` → unchanged.

## 5. Non-blocking notes

- **N1 (strongest — promote from optional to required).** The plan's "small invariant over the source update payload **if useful**" is the only thing that can pin `Targets` and `ReplicaStatus`; the disk assertion at `replication-delete-mrf_test.go:353` pins `ReplicationStatusInternal` alone. Make it mandatory: wrap the `ObjectLayer` passed to `replicateDelete` and assert, for every purge subtest, that `opts.DeleteReplication` has all three fields empty and `CompositeReplicationStatus() == ""`. A `replicaSource + unrecordedPurge, targets: 2` case is optional — it may pass either way because the replica timestamp round-trips byte-identically, which is exactly why the payload invariant is the better instrument.
- **N2.** Tighten §v3's mechanism sentence: `fi.Deleted=false` protects via `DeleteMarkerReplicationStatus()` returning `""` (`erasure-metadata.go:670-675`), not via the guard; record the second masking condition (`erasure-object.go:2102`) confining exposure to delete-marker versions, and the one-shot nature of the corruption.
- **N3.** State `ReplicaStatus = ""` as defensive (§2), not as a fix for an observed failure.
- **N4.** Residual, explicitly out of R6: `replStatusRegex` still misparses any future two-empty-entry string. No producer remains after this delta; record as a follow-up issue, do not fix here.
- **N5.** `drs.ReplicationTimeStamp = UTCNow()` at `:576` is now provably never written for purges. Harmless — leave it rather than widen a three-line delta that has been reviewed field by field.
- **N6.** For the record: the v2 "empty composite" argument was sound only for a single target; the earlier reviews in this lineage, including the one I am extending, overstated it.

## 6. Consensus

I state consensus on the exact plan content as read, including §A's three-field instruction (line 23) and the §v3 restatement (line 67), subject to the digest caveat. All v2 dispositions and scope limits carry forward unchanged: no merge/publish/deploy, no live storage rewrite, no parser or `getReplicationState` merge change, nil-client and already-erased-tracking states remain excluded, and the 3-endpoint fixture is not a production three-daemon SR mesh. Proceed with the three-field delta plus the N1 payload invariant.
