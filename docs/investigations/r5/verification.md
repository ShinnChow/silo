# R5 local verification

## Scope and source identity

This is a local repair of ordered tag deletion along selected replication requests. It does not authorize or establish a main merge, push, release, deployment, historical-state migration, or production multi-site acceptance.

- Research baseline: `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`.
- Combined verification dependency: R4 `dbcf8dec589deb5d91e17d295cb70997635f5b55`; R5 does not modify `cmd/object-api-options.go`.
- Accepted plan: v2, SHA256 `5a782acf3f285b23d1ae43a73481c4eb772a9a6d917fc5a550ecfc7cbf7446ca`.
- Actual plan reviewer: `claude-opus-5`, explicit max effort; v1 requested changes, v2 approved with nonblocking notes and zero blockers. See `consensus.md`.
- Actual implementation reviewer: the same requested/observed model and effort, `GO_WITH_NONBLOCKING_NOTES`, zero blockers. Original review identity and hashes are in `opus-implementation-metadata.json`.
- `implementation-manifest.json` identifies the reviewed patch. `final-implementation-manifest.json` identifies the final source after a stronger multi-pool test assertion and gofumpt formatting. All seven production file hashes still match the review.

## Executed regression checks

All commands run from this worktree with `GOMAXPROCS=2` and `go test -p 1`, Go `go1.27.1 darwin/arm64`. Raw logs are in `/Users/vonng/tmp/silo-r5-20260915-77ad/`.

| Check | Observed result | Evidence |
|---|---|---|
| New R5 tests before implementation | Reproduced real signed-HTTP deletion resurrection, empty COPY loss, full-write persistence/skip, equal-value sender skip, local revision inversion and stale source ACK | `baseline*.log`, `discussion-baseline.log`; `reproduction.md` |
| Latest complete new R5 selection | 13 top-level tests passed, 9.161s | `fixed-targeted-latest.log` |
| Related selection, host capacity adapted | 134 passed; one POST fixture still failed the host minimum-free threshold, 56.007s | `related-suite-capacity-final.log`; exact 135 names in `related-test-names.txt` |
| Remaining POST test plus strengthened multi-pool replay test | Both passed, 3.232s; completes the 135-name selection across the two batches | `final-post-and-pools.log` |
| Existing `TestReplicationResync` in isolation | Passed on baseline (2.191s) and R5 (1.776s) | `baseline-resync.log`, `fixed-resync-isolated.log` |
| Final R5 plus tag-storage race selection | 16 top-level tests passed, 25.584s runtime, no race diagnostics | `targeted-race-final.log`; exact command/exit in `final-check-results.json` |
| Repository verifiers | Passed: lint 0 issues, generated files unchanged, branding/compatibility and entrypoint checks passed | `make-verifiers-serial.log`, `verifiers-result.json` |
| Repository build and binary invocation | `make build` passed; the resulting `silo --version` exited 0 | `make-build.log`, `build-result.json`, `silo-version.log` |

The selected regressions include existing replication trust/header poisoning, API preconditions, Object Lock, SSE-C retransmission, R4 KMS option/COPY tests, and pool metadata/cleanup/retry checks. The new R5 suite covers:

- Local PUT tags, repeated DELETE, empty PUT and ordinary empty COPY; tag revisions advance even without selected replication, while local tagging preserves object ModTime.
- Empty/nonempty and newer/stale/equal/missing revisions through signed COPY with both metadata directives, PUT and multipart; UUID/null and unqualified COPY; unrelated newer versions survive.
- Multipart deletion committed between initiation and completion, with the upload's saved revision checked at initiation and ordered again at completion.
- Exact SDK sender headers, nanosecond precision, legacy fallback only for nonempty tags, and rejection of malformed recorded times.
- Equal-value metadata resend, failed COPY reporting/retry, no incoming-replica requeue, and a stale queued source ACK preserving the current deletion.
- Uniform local tag revisions beyond every physical pool, deterministic inverted request/commit timestamps, normal-routing readback after replay and inspection of every retained pool copy.
- Destination KMS encryption/readback and signed SSE-C key rotation with decrypted GET. These are local handler/storage fixtures, not an encrypted-source-to-encrypted-destination two-site deployment.

## Baseline and environment failures retained

The first broad selection panics at `TestReplicationResync` before any R5 test executes. Replacing all seven R5 production files with the R4 baseline, and hiding the two new tests in a Go overlay, reproduces the same panic after the same preceding tests (`baseline-related-suite.log`). The test passes alone on both versions. The remaining 135-name selection therefore runs separately; this is not reported as an unfiltered full-package pass.

This host's used-space percentage makes existing allocation tests return `XMinioStorageFull`. The test-only overlays add the existing `tagTestCapacityDisk` via `r5Capacity` at the API/pool fixture boundaries and the final POST fixture. The adapter changes reported capacity only, delegates real I/O and propagates disk errors. The exact overlays, original/modified fixture hashes and diffs are retained as `capacity-fixture*` and `capacity-post*`. No fixture overlay or capacity-policy change enters production code.

The first adapted link and first verifier run also failed actual `ENOSPC` when the volume had about 200–500 MiB available (`related-suite-capacity.log`, `make-verifiers.log`). Regenerable Go cache data untouched for three days was reclaimed with an exact manifest (`cache-reclaim.json`); subsequent successful checks are distinguished from those failures. A subsequent verifier caught gofumpt formatting in the new helper; that formatting was corrected before final validation.

An earlier KMS multipart fixture used a single-PUT ETag with multipart data layout and failed decryption. Seeding a real multipart source fixed the fixture; the subsequent complete run passed plaintext readback. The failed log remains `fixed-targeted.log`, and this is not attributed to a production encryption change.

## Local delivery and remaining integration gates

Required scoped local checks are complete. `validation-results.json` records command results, and `evidence-manifest.json` identifies the raw files and binary by SHA256. The build compiled the working source identified by `final-implementation-manifest.json`; the Makefile stamped its pre-commit dependency ID `dbcf8dec5` into this local development binary. Final source identity is established by the file hashes, not by that pre-commit version label.

An unfiltered full `cmd` run and real multi-site deployment remain future integration gates before any separately authorized merge/release. Known scope limits are retained in plan v2 and `implementation-review-response.md`: tag-filter target eligibility, historical missing revisions, malformed stored source times, legacy peers dropping empty revisions, and arbitrary distributed clock skew.

The verifier uses the repository-pinned golangci-lint v2.13.1 through a local wrapper adding only `--allow-serial-runners`. This waits for the shared host lint lock instead of running another lint process concurrently. The first unscheduled attempt was rejected by that lock (`make-verifiers-success.log`; despite that filename, its recorded exit is 2). The final serialized run passed. The optional `typos` binary is unavailable and was skipped by the Makefile.

R4 has since merged as `af2b1794d38d9e70e1d2c3ee692426e4b6cab4bd` (PR #193). `dependency-handoff.json` verifies that its production options file and both test bodies match the dependency used above. The other differences are test license headers and R4 review/validation documents. The R5 delivery base is this exact merged dependency, with the old unsigned `dbcf8dec5` ancestor removed. The original recorded plan/review baseline remains intact as historical evidence. The final local commit, clean-worktree check and post-rebase file-hash comparison are recorded outside the commit in `/Users/vonng/tmp/silo-r5-20260915-77ad/final-delivery.json`.
