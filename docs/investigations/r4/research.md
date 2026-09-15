# R4 research log

## Verified baseline

2026-09-15: local clean HEAD and GitHub main both `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`. Branch created as `codex/r4-kms-tag-timestamp`. Live GitHub open PRs #184 (`6addf9eb916b5a4b837480cf534cd1efa5407d3c`) and #187 (`b8f2fdde41dff3dc3b8db669c1d42d30ca5c1d3d`) concern other tasks. Claude Code reports `2.1.270`; Go reports `go1.27.1 darwin/arm64`.

## Coordination

- Parent task: `01a0a5ab-ee43-7911-bddd-1aca6f8afcc8`.
- R5: `01a0a5b9-602d-7470-9882-4817cf5fdcd1`, `/Users/vonng/.codex/worktrees/77ad/silo`.
- Parent and R5 acknowledged the ownership boundary: R4 options constructor and nonempty KMS COPY tests; R5 producer/receiver ordering and empty values. R5 will consume R4's minimal patch for combined KMS acceptance.
- Initial conservative expectation separated metadata COPY from REPLACE ordering. Inspection of current `ReplicaLockReconcile` and `reconcileStoredObjectTags` shows that stored timestamps are also reconciled under the write lock for REPLACE. The temporary reproduction therefore uses REPLACE directly; the fix must demonstrate the actual sender-shaped path without changing the handler.

## Baseline reproduction

Temporary overlay test source and output are in `/Users/vonng/tmp/silo-r4-evidence-20260915-a9cb/`. The options and HTTP/disk reproductions fail for the expected missing timestamp. Every KMS HTTP request completed with 200; newer tags remained old; encrypted object metadata and subsequent ordinary plaintext GET succeeded. The result is narrower than claiming all KMS replication fails, and stronger than merely comparing options.

The temporary source is not a production implementation. See [plan v1](plan-v1.md) for exact scope and required acceptance. The plan is frozen by SHA-256 before invoking real Opus.
