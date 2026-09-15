I'll start by reading the required documents and verifying the plan hash.

Now let me verify the plan hash and inspect the source claims.

I read AGENTS.md, opus-v1-review.md, decisions-v2.md, and plan-v2.md, then re-checked every claim v2 changed against the source at `9ebe81c1b`. I ran nothing; findings are source reading plus the supplied logs.

One honesty note up front: **no shell tool is available in this session, so I could not compute the SHA256 myself.** My verdict is on the exact content I read at `docs/investigations/r6/plan-v2.md` — 59 lines, opening `# R6 plan v2 — marker purge operation and MRF recovery`, dated 2026-09-15, closing with the "v2 changes from Opus v1" section that lists B1/N1–N7 dispositions. If that is `dae51753a3ab4b2ea98b85e720144338fdf52541ca6daf968c7dac5ce564d8c3`, the verdict below attaches to that hash.

# Verdict: GO_WITH_NONBLOCKING_NOTES

**Blockers: none.** B1 is resolved, and resolved by the correct mechanism rather than by wording.

---

## B1 and the coupled exits — resolved, mechanism verified end-to-end

§A now says purges leave `rinfo.ReplicationStatus` empty on every exit. I traced the preservation chain to make sure that is actually load-bearing and not just an absence:

1. `replicatedInfos.ReplicationStatusInternal()` (`cmd/bucket-replication-utils.go:92-101`) emits `"arn1=;"` — non-empty, so it does reach `ReplicationState`.
2. `replicationStatusesMap` (`:428-439`) uses `replStatusRegex` (`:168`), whose second group `([^,].*?)` requires ≥1 char before `;`. `"arn1=;"` does not match → **empty `Targets` map**.
3. `CompositeReplicationStatus()` (`:356-372`) therefore takes the `default:` branch → `getCompositeReplicationStatus(empty)` → `""`; the `ReplicaTimeStamp` fall-through at `:366-371` also returns `""` because `replStatus == Completed` is false. So the composite is `""` under every sub-case.
4. `xl-storage-format-v2.go:1438` gates the `DeleteType` rewrite on `!fi.DeleteMarkerReplicationStatus().Empty()` → **guard never fires** → `MetaSys[ReplicationStatus]`, `MetaSys[ReplicationTimestamp]` and the Replica pair at `:1441-1442` are left byte-for-byte intact, including ARNs excluded from fan-out. `updateVersion` is still true via `:1390`, so `MetaSys[VersionPurgeStatusKey]` at `:1448-1449` is written as intended.

All three consequences I said were mandatory and non-separable are present:

| Coupled item | v2 |
|---|---|
| Purge exits write only `VersionPurgeStatus` | §A, explicit, "on every exit"; `PrevReplicationStatus` retained for inspection only — harmless, `targetState()` sets it and nothing persists it |
| Resync defer gated on the operation's own success | §A "Do not stamp the current resync reset for a failed purge"; §C1 asserts the reset marker |
| `:556` stats gate replaced by per-target operation-status comparison | §A "Feed per-target old/new operation status into stats rather than selecting changes from the unrelated creation status" |
| Partial-fan-out regression test | §C5, with two creation ARNs, a non-zero timestamp, and fan-out restricted to one ARN |

The zero-timestamp and `ReplicaStatus` sub-findings from v1 are dissolved rather than patched: with the composite empty, `:1444-1445` is never reached, so `drs.ReplicationTimeStamp` (set at `bucket-replication.go:568-570`) cannot land on disk for a purge at all.

I also checked the downstream consumer the change could have silently flipped: `resyncTargetSucceeded` (`bucket-replication.go:3154-3162`) keys the purge branch on `roi.VersionPurgeStatus` and reads `t.VersionPurgeStatus`, never `t.ReplicationStatus`. Leaving `ReplicationStatus` empty makes resync accounting strictly more correct for the old shape (today a resynced old-shape purge sets `ReplicationStatus = Completed` at `:713`). §A's "leave it alone" is right.

## Amended retry queue-full site — correct

`cmd/bucket-replication.go:2411-2448`, `queueReplicaDeleteTask`, `default:` branch of the select at `:2426-2427` → `p.queueMRFSave(doi.ToMRFEntry())`. §B names it exactly ("queueReplicaDeleteTask queue-full fallback"). I grepped every `queueMRFSave(` call: the delete-path sites are precisely `:487` (lock), `:565` (aggregate failure), `:2427` (queue-full) — three, no more. `:1206`, `:1311`, `:2339`, `:2370` are object-path.

The supporting claims hold too: `MRFReplicateEntry.RetryCount` already exists with msgp tag `rc` (`bucket-replication-utils.go:792`), so no format change; `DeletedObjectReplicationInfo.ToMRFEntry()` (`:1927-1937`) sets only `Bucket`/`Object`/`versionID`; `versionID` is unexported but survives as the map key (`persistMRF` `:3870`, read back at `:4068`); the `> mrfRetryLimit` drop at `:3883-3887` is therefore currently unreachable for deletes; and `DeletedObjectReplicationInfo` has no generated msgp code, so the new field is genuinely schema-free. The gap's exact location is `queueReplicationHeal:3762` setting `roi.RetryCount` while `dv` at `:3781-3793` drops it — which is what §B closes.

## N7 evidence — checks out

`baseline-mrf.log` shows `TestReplicateDeleteMarkerPurge` both subtests **PASS**; the "Storage reached its minimum free drive threshold" abort from v1 is gone, so §C items 2/3/5/6 have a working fixture. The logs also independently corroborate two plan premises on real storage: `err=Method not allowed` on the source marker lookup (§B's 405 premise), and `legacy=true … MRF=0` on both baseline and PR #184 (observation 2, and #184's residual gap). Your retraction is right, and I'd add that the mechanism is visible: `replicatedInfos.ReplicationStatus()` (`bucket-replication-utils.go:103-119`) counts *every* target including empty-status ones, so `creation=PENDING` for a canonical purge is an artifact of that aggregate, not disk state. The retained logs still carry the old "failure stored in incorrect status field" assertion text on the canonical rows — worth a pointer to decisions-v2's retraction beside them so a later reader doesn't re-derive the wrong conclusion.

---

## Non-blocking notes

1. **Audit `Status` string changes on the live canonical path.** §A folds audit into the COMPLETE→COMPLETED mapping. The audit defer at `bucket-replication.go:434-444` logs `Status: string(replicationStatus)`, so every successful canonical versioned-delete replication goes from `COMPLETE` to `COMPLETED`. That is a user-visible change on the *live* path, not the legacy one. Keep it — `CompletedLegacy` is documented as an error at `internal/bucket/replication/datatypes.go:35-36` — but assert the audit string in §C, reuse `replication.CompletedLegacy` rather than a `"COMPLETE"` literal, and mention it in the PR text.
2. **The stats delta is wider than §C5's Heal/ExistingObject framing.** The `Completed` case gate is right (`bucket-replication-stats.go:189-192` requires `IsDataReplication()`, which excludes the unset OpType on handler-originated deletes — `replication.go:139-145`). But replacing the `:556` gate also changes *which* purges reach `Update`: today `""` vs `COMPLETED` makes that gate fire for nearly every purge of a previously-replicated version, and old-shape purges currently record a spurious `Pending` (aggregate `Pending`, prev `COMPLETED`). Assert that spurious `Pending` disappears too. Note `ri.Size` is never set in `replicateDeleteToTarget`, so `Completed` deltas are count-only, zero bytes.
3. **Make the `ResetStatusesMap` nil-guard unconditional** instead of contingent on tests exposing it (plan line 51). `getReplicationState:419-422` writes into `prevState.ResetStatusesMap` unguarded; `ObjectToDelete.ReplicationState()` (`:590-600`) leaves it nil, unlike `ObjectInfo.ReplicationState()` (`:576`). Today it is unreachable only because the resync defer requires `ReplicationStatus == Completed`, which purges never reach — and §A re-gates exactly that defer. I traced the live producers: the sole `ExistingObjectReplicationType` delete is `:3329-3342`, fed by `getHealReplicateObjectInfo` → `oi.ReplicationState()` → non-nil, so this is robustness and test-fixture safety, not a live panic. Two lines; just do it.
4. **Pin the already-COMPLETE purge under ExistingObject resync in §C1.** The purge early-out at `:628` has no `OpType != ExistingObjectReplicationType` exclusion, unlike the creation early-out at `:624`. Routing old-shape purges through it means a resync of an already-COMPLETE old-shape purge now short-circuits where today it re-sends. That matches canonical behaviour and is probably intended, but §C1's "resync success/failure" row currently leaves the implementer free to pick either.
5. **Scope "Preserve actual target failure in Err, including offline error where appropriate."** The offline exit (`:631-651`) sets no `Err` today, and `Err` flows into `replStat.set(...)` → `srUpdate` → site-replication stats. Say whether creations also start carrying an offline `Err`, or restrict it to purges; otherwise §C6's offline row has no fixed expectation.
6. **Leave `getReplicationState`'s third parameter alone.** `vID` (`bucket-replication-utils.go:402`) is entirely unused in the body. Since §A re-plumbs shape classification, someone will be tempted to wire it up; the empty-composite preservation depends on that function staying shape-agnostic.
7. **§B identity gate: key on `versionID` + `DeleteMarker` + non-zero `ModTime`.** `queueMRFHeal` calls `GetObjectInfo` with `ObjectOptions{VersionID: vID}` and no `Versioned`, so `ToObjectInfo` (`erasure-metadata.go:118-123`) returns `fi.VersionID` verbatim — the gate works. Bucket/object equality is trivially satisfied (they are the request arguments); the one component that can be perturbed is a strict `oi.Name == e.Object` against `decodeDirObject` (`cmd/utils.go:899-904`) for directory objects. `erasure-server-pool.go:1063-1077` does return the populated `oi` with the error upward, so the gate has real data to inspect.
8. **RetryCount type.** `ReplicateObjectInfo.RetryCount` is `uint32` (`:3762`), `MRFReplicateEntry.RetryCount` is `int` (`:792`), `QueueReplicationHeal` takes `int`. Pick one for the new delete-task field and keep conversions in one place.
9. **State the purge-subset loss as pre-existing, not merely excluded.** The asymmetry §C5 carves out is structural: creation status survives partial fan-out *because* the composite is empty (`:1438`), while `VersionPurgeStatusInternal()` is non-empty and unconditionally rewrites `MetaSys[VersionPurgeStatusKey]` from the fan-out subset (`:1448-1449`). Canonical purges already lose excluded-ARN purge status today, so R6 neither causes nor fixes it. Say that, or a reader will assume both were repaired.
10. **§C3 flush mechanics.** `persistMRF` (`:3866-3874`) only writes to disk on the 5-minute timer, ctx cancel, or 1M entries; the re-queued entry lands in an in-memory map first. N6's "persist each failed entry again" covers it, but make explicit that the test drives `saveMRFEntries` directly rather than waiting on the timer — and that `loadMRF` deletes the file after reading (`:4013-4015`), so each round needs a fresh write.

---

## Consensus

I state consensus on **`docs/investigations/r6/plan-v2.md` exactly as reviewed here** (hash `dae51753a3ab4b2ea98b85e720144338fdf52541ca6daf968c7dac5ce564d8c3` as supplied; content verified, digest not independently computed for lack of a shell tool). The classification rule, the all-exits treatment, the canonical-wire choice, the empty-creation-status preservation mechanism, the valid-405 gate, the three-site retry budget, and the §C matrix are technically correct against `9ebe81c1b`, and the scope limits in the "Existing state, compatibility, and delivery" section are accurate rather than overclaimed. The ten notes above are refinements and assertions to add, not conditions on this agreement — none of them requires another review round or a new hash.

Tests in §C remain future obligations. Nothing here asserts they pass; the only executed evidence I relied on is the supplied baseline/PR logs, which establish fixture viability and the two defect premises, not acceptance.

The review above is complete and stands as delivered. Summarizing the required outputs:

**Verdict for `docs/investigations/r6/plan-v2.md`, hash `dae51753a3ab4b2ea98b85e720144338fdf52541ca6daf968c7dac5ce564d8c3`: GO_WITH_NONBLOCKING_NOTES**

**Blockers: none.**

**Consensus:** I state consensus on the exact plan as reviewed. The one caveat is procedural, not substantive: no shell tool is available in this session, so I verified the plan's *content* (59 lines, opening `# R6 plan v2 — marker purge operation and MRF recovery`, closing with the B1/N1–N7 disposition list) but could not independently compute its digest. Consensus attaches to that content.

What I resolved this round:
- **B1** — accepted resolution, and I confirmed the mechanism is real rather than merely absent: `"arn1=;"` fails `replStatusRegex`, so `Targets` is empty, so `CompositeReplicationStatus()` returns `""` under every sub-case including the `ReplicaTimeStamp` fall-through, so the `xl-storage-format-v2.go:1438` guard never fires and the on-disk creation block survives partial fan-out intact — while `updateVersion` still writes `VersionPurgeStatusKey`.
- **Coupled exits** — all three present (purge-only status writes; resync defer gated on the operation's own success; `:556` gate replaced by per-target operation status), plus the §C5 partial-fan-out test. I also checked the one consumer that could have flipped silently, `resyncTargetSucceeded:3154-3162`, and it keys on `VersionPurgeStatus` — so the change makes resync accounting strictly more correct.
- **Queue-full retry site** — `queueReplicaDeleteTask`, `default:` branch, `bucket-replication.go:2427`; the delete-path `queueMRFSave` sites are exactly `:487`, `:565`, `:2427`, and the `RetryCount` disk field already exists with msgp tag `rc`.
- **N7** — the fixture-threshold failure is gone; `TestReplicateDeleteMarkerPurge` passes on baseline, and the logs corroborate the 405 premise and the `legacy=true … MRF=0` gap on both baseline and #184.

The ten non-blocking notes are in my response; the three worth acting on before you branch are the audit-string change on the live canonical path (note 1), the wider-than-stated stats delta (note 2), and making the `ResetStatusesMap` nil-guard unconditional (note 3). None requires another review round or a new hash.

Tests in §C remain future obligations — nothing here asserts they pass. I made no edits and wrote no files.
