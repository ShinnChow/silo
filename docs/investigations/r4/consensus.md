# R4 plan consensus and review disposition

## Agreed version

- Plan: [plan v1](plan-v1.md), SHA-256 `ad539f2071155de6955b583991684ed33c4bfe2e29660005840cdc97d7e1a754`. The frozen file remains unchanged.
- Baseline: `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`.
- Actual reviewer: Claude Code 2.1.270, every assistant model in the review stream is `claude-opus-5`; explicit `--effort max`.
- Opus: **GO_WITH_NONBLOCKING_NOTES**, zero blockers; explicitly agrees that this exact plan can enter local implementation. [Unedited returned review](opus-v1-review.md), [machine-readable provenance](opus-v1.metadata.json).
- Codex: agrees that adding the already-parsed timestamp to the KMS literal fixes R4, and accepts the nonblocking dispositions below. **No blocking disagreement remains on plan v1.** No production source edits were made before this record was saved.
- The agreement permits the planned local implementation and tests; it is not implementation acceptance, a merge decision or production release approval.

## Item-by-item disposition

| Opus ID | Disposition |
|---|---|
| R4-01 | Accepted citation correction here, leaving the agreed hash frozen: `ReplicaLockReconcile` is at baseline `object-handlers.go:1847`; encryption merge is at `:1903`. |
| R4-02 | Accepted scope clarification: ErasureSD and Erasure16 are both single-pool local backends. KMS rewrites use PutObject under-lock reconciliation. Multi-pool and multi-site validation are optional and deferred to the wider integration gate. Test comments and the final report will identify this boundary. |
| R4-03 | Accepted wording clarification: source encryption alone does not request destination encryption. Source-only SSE-C copy headers do not prevent destination bucket/default auto-KMS from selecting KMS. The three destination trigger categories stay unchanged. |
| R4-04 | Accepted intent. The regression matrix uses identical expected mtime, ETag, trust and all three source timestamps across all encryption modes, giving field-by-field equivalence without constructing expected values through the production function. The temporary expanded baseline matrix fails only trusted valid KMS tag timestamps. |
| R4-05 | Accepted optional test within the existing scope: a signed KMS COPY with nonempty tags and no source tag timestamp must preserve the stored value/time. This adds evidence, not production behavior. |
| R4-06 | Registered as a separate unverified-impact finding: KMS construction also omits `ProxyHeaderSet`, `ProxyRequest`, `Speedtest` relative to `getDefaultOpts`. No R4 fix or correctness claim for those flags. Send the observation to the parent for separate triage; do not assign it to R5. |
| R4-07 | Accepted. Assertions target final disk state; the REPLACE handler rebuilds metadata, while final stored-tag rejection occurs under the storage write lock. HTTP 200 alone is not acceptance. |
| R4-08 | Resolved provenance uncertainty by Codex: SHA-256 recomputed before/after review, baseline identity and current GitHub main/PR query captured in `baseline-identity.txt`. History was inspected locally with `git blame` / `git show`. Opus's read-only tools did not independently recompute the hash or check GitHub; those facts remain attributed to the local commands. |

## Raw evidence

Directory: `/Users/vonng/tmp/silo-r4-evidence-20260915-a9cb/`.

- `review-prompt-v1.md`, `opus-review-v1.jsonl`, `opus-review-v1.stderr.log`, `opus-review-v1.exit`.
- `baseline-identity.txt`, `r4_repro_test.go`, `overlay.json`, `baseline-repro.log`.
- `options_repro_test.go`, `options-overlay.json`, `baseline-options.log`.

The stream includes an attempted Write to Claude's own plan file. Its tool was disabled; the reviewer returned the full result in text and did not edit production source. The successful result and actual assistant models are checked separately from rate-limit status and auxiliary-model usage.
