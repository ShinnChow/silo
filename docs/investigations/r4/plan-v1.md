# R4 plan v1: preserve the replicated tag timestamp for SSE-KMS

## Baseline and ownership

- Baseline: `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`, verified against GitHub main on 2026-09-15.
- Branch: `codex/r4-kms-tag-timestamp`; worktree: `/Users/vonng/.codex/worktrees/a9cb/silo`.
- Live open PRs at inspection: #184 and #187, neither owns this options change.
- The worktree lacks the ignored `AGENTS.md`; the parent explicitly confirms `/Users/vonng/pgsty/silo/AGENTS.md` applies. Maintain the PGSTY product graph and inexpensive compatibility.
- R4 owns only the missing field in `cmd/object-api-options.go` and its regression tests. R5 owns DELETE/empty tags, PUT/multipart receiving, sender propagation and full receiver ordering. R4 will supply a standalone source patch to R5; neither task edits the other's worktree.

## Proven defect and actual trigger

`putOptsFromHeaders` parses the trusted source tag timestamp before selecting encryption. The SSE-KMS branch constructs and returns another `ObjectOptions` carrying mtime, ETag, replication trust and both Object Lock timestamps, but omits `ReplicationSourceTaggingTimestamp`. The normal path retains it. The parser accepts and preserves RFC3339 fractional seconds even though its layout is `time.RFC3339`; the reproduction uses nanoseconds.

`CopyObjectHandler` applies local destination encryption configuration before `copyDstOpts` → `putOptsFromReq` → `putOpts` → `putOptsFromHeaders`. The omission is reached by:

1. Explicit destination SSE-KMS request headers (with or without a key ID/context).
2. A destination bucket with default SSE-KMS, when the request has no explicit SSE choice.
3. Global automatic encryption with no bucket SSE override and no explicit SSE choice.

Explicit AES256/SSE-C takes its existing branch; source encryption alone does not select the destination KMS branch. Remote federation skips local destination defaults. The relevant trigger is trusted metadata entering the destination KMS branch, not every SSE-KMS object or every tag operation.

At `CopyObjectHandler`'s tag decision, a zero source timestamp skips the tag update. Current under-lock reconciliation can preserve the stored tag/timestamp when metadata REPLACE reconstructs the map with no timestamp. In the observed same-version replica COPY, the request succeeds, destination encryption is valid, and the old tags/timestamp remain. A missing field in the options layer is not itself proof of a content-read failure.

PUT and multipart consumers' independent failure to persist a parsed tag timestamp remain R5's responsibility. R4 does not claim to fix all tag replication by correcting this constructor.

## Source and reproduction evidence

- `cmd/object-api-options.go`: trusted parsing at 383–426; KMS construction at 433–460; normal assignments at 469–475.
- `cmd/object-handlers.go`: destination default encryption at 1425–1435; `copyDstOpts` at 1454; tag timestamp consumption at 1807–1834; replica reconciliation enabled at 1851; encryption metadata merge at 1910.
- `internal/bucket/encryption/bucket-sse-config.go:135`: explicit request wins, absent config + auto encryption selects KMS, otherwise configured bucket algorithm/key ID applies.
- `cmd/erasure-server-pool-consistency.go:232`: stored valid timestamp wins over absent, older or equal incoming timestamp; writes preserve the stored tag value alongside its timestamp.
- `cmd/erasure-object.go:136` and `cmd/erasure-server-pool.go:1509`: same-version replica COPY reaches existing under-lock tag reconciliation, including object-data rewrites.
- History: the omission exists in `c4373ef290` (2021-09-18); `b2dca43fda` (2026-09-05) added the two Object Lock timestamps but not the tag timestamp. `cfefc049c` fixed KMS context encoding independently and must remain intact.
- Fresh temporary reproduction: `/Users/vonng/tmp/silo-r4-evidence-20260915-a9cb/r4_repro_test.go` and `baseline-repro.log` (overlay; no production edits).
- Command: `GOMAXPROCS=2 go test -p 2 -overlay /Users/vonng/tmp/silo-r4-evidence-20260915-a9cb/overlay.json ./cmd -run '^TestReviewR4' -count=1 -timeout 5m -v`.
- Result: expected failure. Unencrypted and AES256 options preserve `2026-09-15T01:00:00.123456789Z`; KMS returns zero. Signed metadata REPLACE COPY on ErasureSD and Erasure (16 disks), across explicit/default/automatic KMS, returns 200 but retains `key=old` and the old timestamp for newer events. Actual encrypted metadata and plaintext GET roundtrips pass. Test deltas are 1–3 nanoseconds.
- These are in-process signed HTTP router and real local disk tests. `kms.NewStub` replaces the remote key service; the normal server encryption/decryption code still runs. Existing `tagTestCapacityDisk` avoids the host's free-space percentage threshold; it delegates all object data/metadata I/O to real test disks.

## Proposed production change

Add exactly this field to the existing KMS `ObjectOptions` literal:

```go
ReplicationSourceTaggingTimestamp: taggingtimestmp,
```

Update the neighboring explanatory comment to include tagging alongside retention/legal hold. Do not refactor the common return paths, change parsing/fallback/equal-timestamp semantics, change encryption context encoding, modify trust decisions, add SDK dependencies, or change storage/wire format. Those changes are unnecessary to restore the missing existing contract.

## Required validation after consensus

1. Add an options regression matrix covering unencrypted, SSE-S3, SSE-KMS with no context, SSE-KMS with a context, and SSE-C. Validate trusted/untrusted requests, missing/valid/malformed tag timestamps, nanosecond and timezone/whitespace handling, all three replication timestamps, mtime/ETag/trust, nonnil metadata, and unchanged SSE header serialization (including KMS key/context).
2. Promote the temporary COPY reproduction into a named, isolated regression test. Use actual signed same-version metadata COPY with REPLACE metadata and tagging directives, on single-disk and 16-disk backends. For explicit, bucket-default and automatic SSE-KMS, check newer update, older delivery, duplicate replay, and a second newer update. Verify stored tags, exact timestamp, version ID, encryption kind and plaintext GET after each operation. Include an unencrypted/SSE-S3 control if the fixture can do so without expanding implementation scope.
3. Fail the final regression tests against unmodified baseline using an overlay. Then run them on the fixed source, alongside existing replication-trust/options and bucket-KMS Object Lock tests. Check `gofmt`, `git diff --check`, and `go vet ./cmd`.
4. Run the new focused tests under `-race`. Use `GOMAXPROCS=2` and `-p 2` while sibling tasks share the host. A one-field pure option fix does not justify concurrent full-repository suites in all five tasks; full Linux CI and multi-site validation remain separate delivery gates.
5. If a test exposes a separate handler/storage defect, report evidence and coordinate with R5. Do not broaden R4's production patch to make unrelated tests pass.

## Compatibility, existing state, effort and delivery

- Public API, header names, stored key names, KMS context/key handling and supported dependencies remain unchanged. Untrusted source headers stay ignored; malformed trusted timestamps continue to fail; absent timestamp remains zero. Existing non-KMS behavior remains unchanged.
- No automatic rewrite/backfill. Lost source tag times cannot be reconstructed from the receiver alone. Upgrading permits subsequent properly timestamped events to be consumed. Review source-of-truth and target state before any targeted resync; full historical convergence also depends on R5. Repeated events subject to existing timestamp/tie semantics are not a universal repair guarantee.
- The source fix can land independently; complete deletion/empty-tag and mixed-encryption convergence needs R5 plus its integration evidence.
- Expected effort: approximately 0.5–1 engineer-day including reproduction, review and local validation; key-service deployment, multi-site failures and existing-state remediation are separate.
- After actual Opus 5.0/max agreement on this exact plan hash, implement locally without another user permission prompt. Preserve raw review, assistant model identity, request effort, baseline and plan hash, issue-by-issue disposition and explicit consensus before source edits.
- Deliver a reviewable local diff, tests and evidence. No main merge, remote publication/release, deployment or existing-state rewrite is authorized by this plan.
