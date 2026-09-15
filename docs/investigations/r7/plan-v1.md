# R7 plan v1: preserve normalized replica object metadata

## Frozen scope and source

- Date: 2026-09-15. Worktree: `/Users/vonng/.codex/worktrees/ad51/silo`.
- Local branch: `codex/r7-replication-content-encoding`.
- Baseline: `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`, also returned by current `gh api repos/pgsty/silo/commits/main` and fetched `origin/main`.
- PR [#187](https://github.com/pgsty/silo/pull/187): OPEN, unmerged, no reviews/checks returned; head `b8f2fdde41dff3dc3b8db669c1d42d30ca5c1d3d`. GraphQL's PR baseRefOid is `89637554d60c27cfc51d2281d0a4fe15e415f06d`; it is not the live main checked above. Snapshot and exact diff: `/Users/vonng/tmp/silo-r7-20260915-ad51/pr187.{json,diff}`.
- Introduction: `56fa63bfd155154157cd7e1fb6dc295a3b3104ed` (2026-04-15), replication-header injection hardening. Keep its trust protections intact.
- Governing scope: PGSTY maintained stack, minimal compatible fix. No dependency, wire-format, credential, encryption algorithm or API changes.

## Root cause and observable contract

`extractMetadata` calls the ordinary extractor, removes disallowed unencrypted-length/MD5 user metadata, and trims the exact `aws-chunked` transport token from `content-encoding`. The trusted-replica restoration currently calls the same broad extractor with `allowReplication=true`. That replays all supported headers and user metadata, reversing normalization and redaction.

Expected mappings are `aws-chunked` -> absent Content-Encoding, `aws-chunked,gzip` -> `gzip`, and `gzip` -> `gzip`. Object bytes are not transformed by this fix. AWS documents this behavior in [SigV4 streaming](https://docs.aws.amazon.com/AmazonS3/latest/developerguide/sigv4-streaming.html). The helper reproduction is `/Users/vonng/tmp/silo-r7-20260915-ad51/baseline_test.go` with Go overlay and `baseline.log`; its failing expectations are evidence of the current defect, not implementation validation.

Persistence/read path: `erasure-metadata.go` reads `fi.Metadata["content-encoding"]` into ObjectInfo.ContentEncoding; `api-headers.go` exposes it on GET/HEAD. Outbound `putReplicationOpts` and metadata-only replication copy also use the object's content encoding. Preventing raw request metadata from being replayed at ingress is sufficient for this defect and avoids read-path masking.

## Input and trust boundary audit

The helper does not authenticate; callers own authentication and authorization. `evaluateReplicationTrust` requires an authenticated principal, the exact single replication marker `true`, and `s3:ReplicateObject`; restoring replica-only metadata also requires `REPLICA`. Unauthorized declared replicas are rejected; marker-only/untrusted requests retain their existing sanitized behavior. Do not move restoration earlier or make headers themselves establish trust.

| Actual caller | Metadata before restoration | Trust gate and intended result |
| --- | --- | --- |
| PutObjectHandler | extractMetadataFromReq before trust evaluation | after successful signature verification, evaluate/apply trust; only replicaTrusted restores six fields |
| CopyObjectHandler via getCpObjMetadataFromHeader | REPLACE calls extractMetadataFromReq; COPY uses source metadata | authenticated source/destination checks; allowReplicationMetadata=replicaTrusted; REPLACE preserves normalization, COPY retains existing semantics |
| NewMultipartUploadHandler | extractMetadataFromReq after trust/sanitization | replicaTrusted restores six fields into initiation metadata; parts and completion reuse saved metadata |
| PutObjectExtractHandler, outer Snowball headers | only storage class and per-entry transform metadata, not generic extractMetadata | per-entry PutObject and ReplicateObject authorization; only replicaTrusted restores six fields. No-PAX entries must not inherit ordinary outer archive metadata |
| PutObjectExtractHandler, PAX entry metadata | extractMetadata on minio.metadata.* records | reuse per-entry trust; merge normalized entry metadata plus six allowed fields. Outer ordinary archive encoding must not leak even if the PAX map omits it |

PutObjectPart, CopyObjectPart and CompleteMultipartUpload do not call this helper; no additional restoration is needed there. Validate completion persistence to catch assumptions at this boundary. POST form upload does not restore replication metadata. Metadata COPY does not normalize historical source metadata; that is deliberately outside this preventive fix.

## Proposed production patch

Adopt the production change in PR #187, adjusted only if current-context application requires it:

1. Remove the `extractMetadataFromMimeWithReplication` boolean-mode helper.
2. Ordinary `extractMetadataFromMime` keeps header canonicalization and supported/user metadata extraction, always skips the replication-only mapping keys.
3. `extractReplicationMetadataFromMime` keeps nil-input error behavior and canonical header lookup; loops only over `replicationToInternalHeaders` and joins multi-values exactly as before.
4. Never re-read ordinary supported or user metadata in the restoration helper. Preserve keys already normalized, defaulted, redacted, or set by the caller.
5. Preserve all six mappings: sealed SSE-C key, seal algorithm, IV, encrypted-multipart marker (including its empty value), actual object size, and ReplicationSsecChecksumHeader (identity mapping). Preserve canonicalized/lowercase input header compatibility.
6. Clarify the comment to cover Snowball: ordinary metadata is owned by the caller; the common normalizing path runs before restoration, while outer archive metadata is not per-entry object metadata.

No normalizer/token grammar rewrite. The current exact-token trimming semantics, malformed duplicate-cased headers, and validation of SSE field payloads are outside this bug; retain existing behavior rather than expanding accepted formats or validation rules.

## Verification matrix and acceptance

Use temporary overlay reproductions before consensus. Promote focused regressions only after recorded Opus agreement. Run targeted tests with bounded Go parallelism because sibling tasks share this host.

1. Helper pipeline: absent encoding, bare aws-chunked, aws-chunked,gzip, gzip, gzip,aws-chunked, multi-valued encoding; ordinary vs restoration; key absence for bare encoding; legitimate gzip retained; ordinary/user metadata sentinel values and redacted unencrypted metadata not restored. Exact expected six-field map, canonical/lowercase headers, empty multipart marker, nil input handling. Repeat restoration should not change ordinary metadata.
2. Real signed HTTP PUT -> persisted ObjectInfo -> GET and HEAD on the existing single-drive and 16-drive erasure fixtures. Use real streaming chunk signatures for transport cases and a valid gzip payload for gzip cases. Compare raw response bytes and content encoding; bare transport must have no header. Test authenticated ordinary, trusted replica, and marker without replication permission; declared replica without permission returns 403 and creates nothing.
3. Signed COPY REPLACE and multipart initiate/part/complete -> persisted ObjectInfo -> GET/HEAD for bare, mixed, and plain gzip. Include ordinary controls. Initiation carries object metadata; part/completion carry contrasting content encoding to prove they cannot replace it. COPY preserves existing source metadata semantics.
4. Snowball trusted entry tests with and without PAX, including PAX no Content-Encoding and PAX mixed encoding; outer aws-chunked never leaks. Existing per-entry trust test stays green.
5. Existing SSE-C single PUT and multipart replication round trips plus replication-header poisoning regressions. Helper matrix verifies all six field mappings; actual SSE-C tests verify readable ciphertext replicas, encryption metadata, checksum and multipart layout. Preserve bucket default encryption/compression behavior. Run relevant SSE-KMS/SSE-S3 option/replica tests if available without broadening R4 scope.
6. Targeted package tests, focused race run, build and repository verifiers. If environmental failures (e.g. disk free-space threshold) prevent existing tests from reaching the path, retain the original failure and use an explicitly documented temporary capacity adapter already used by the repository, keeping actual object I/O on test disks. Do not mislabel that as an unmodified pass.
7. Baseline regression must fail for raw/mixed trusted metadata and fixed code must pass identical expectations. Record exact commands, exit status, baseline/diff hashes and fixture limits. No full distributed sites/deployment acceptance claim from local handler tests.

## Stored-object remediation proposal (separate, no execution)

Upgrade only prevents new pollution. Existing source/COPY metadata can remain wrong, and rollback reopens ingress pollution without undoing repairs. Do not rewrite production objects or private xl.meta files.

A separate operator-reviewed job must inventory bucket/key/version, original Content-Encoding and complete metadata, source/replica provenance, version/ETag/size/checksum and encryption/retention settings. Identify exact aws-chunked tokens and preserve other encodings/order. Verify source bytes/encoding before deciding; gzip must not be guessed or decompressed merely from the broken label. Keep an immutable manifest and metadata backup. Test a version-preserving supported metadata operation on a local replica of the relevant setup; ordinary S3 self-COPY can create a new version/change metadata timestamps and is not a universal version-preserving repair. Resolve object-lock, SSE-C keys, concurrent changes and replication ordering before approving the concrete write plan. Apply a small approved batch with concurrency guards, re-read exact versions, verify GET/HEAD and raw bytes/checksums, then reconcile replicas. Skips/conflicts need explicit reporting and a tested rollback. This task supplies the reviewable design only.

## Effort, delivery and gate

Expected 0.5-1 engineer-day for patch, targeted tests and evidence on a familiar checkout; stored-object repair and release are separate work. Production patch is about 30 added/20 removed lines in one helper file; tests provide most of the new code.

Before implementation: actual Claude Code `--model claude-opus-5 --effort max`, read-only tools, same frozen plan hash + baseline + PR snapshot. Record raw review, actual assistant model(s), objections and dispositions. Any model mismatch/error/rate-limit is not consensus. Resolve substantive findings and get explicit approval of the same plan version before production changes. After consensus, implement and verify without another user permission request.

Deliver research, versioned plan, consensus/dispositions, minimal production diff, tests and verification summary. Local commit may package the reviewable result. No main merge, remote PR mutation, push, release, deployment, or existing-object rewrite is included.
