# R5 implementation review disposition

Real reviewer: `claude-opus-5`, explicit `--effort max`, session `599b4759-add2-4a41-b5b2-865af7a2c096`.
Verdict: **GO_WITH_NONBLOCKING_NOTES; 0 blockers**. Raw review is preserved verbatim in `opus-implementation-review.md`; model usage, original plan/patch hashes and raw log location are in `opus-implementation-metadata.json`.

The accepted v2 plan remains immutable. The following implementation notes supplement it; they do not retroactively change the hash on which plan consensus was reached.

## Nonblocking notes

- **N1 accepted:** a scheduled metadata COPY can rewrite object data when the receiver applies bucket-default/automatic KMS encryption. Its cost can therefore exceed metadata I/O. The existing completed-object/scanner and incoming-replica scheduling gates still prevent a feedback loop. No new transfer optimization or HEAD protocol is introduced.
- **N2 accepted:** a malformed recorded source tag timestamp fails sender construction and remains a retry failure until an explicit correct tag mutation/repair supplies a valid revision. A missing revision is different from a present invalid/empty value. No historical time is fabricated, and no automatic production rewrite is performed.
- **N3 retained scope:** existing marker/trust/REPLICA/version predicates are preserved. Production sender requests satisfy the relevant predicates; R5 does not broaden replication trust.
- **N4 accepted compatibility change:** a trusted metadata COPY without a source tag revision preserves stored tags, including the metadata-REPLACE shape. This is the deliberate missing-revision rule in plan C, and is tested under UUID/null versions and unqualified COPY.
- **N5 accepted:** ordinary COPY records its chosen tag state, including an empty REPLACE and unchanged tags during key rotation, as a fresh local event. This is consistent with the accepted last-writer-wins scheme.
- **N6 no change:** all production writers use the lowercase reserved timestamp key. Case-insensitive sender lookup is compatible with those writers and existing lock timestamp handling.

## Coverage notes

- **L1:** the review was supplied a passing run with **13**, not 12, top-level R5 tests. Its verdict explicitly did not claim execution of the wider tests. The wider selection reproduced the same `TestReplicationResync` order-dependent initialization panic on the unmodified production baseline; that test passes in isolation on both baseline and R5. Host-capacity and actual ENOSPC failures are retained, not reported as passes. Final related, race and static/build results are recorded separately in `verification.md`. An unfiltered full `cmd` package run remains an integration check before any later merge; this task delivers a local patch and does not claim that full-package or multi-site production gate passed.
- **L2 addressed:** after every incoming multi-pool replay, the R5 test now rereads the addressed version through normal pool routing and checks its empty value and deletion revision. The per-pool checks still inspect every retained copy. This prevents a vacuous pass if all copies disappear. The test deliberately allows existing duplicate suppression to retain both identical copies; existing pool cleanup/retry tests separately exercise retirement.
- **L3 accepted boundary:** the combined KMS cases exercise destination encryption and plaintext readback; source fixtures are populated through storage APIs. They do not establish encrypted-source-to-encrypted-destination replication across two running sites. SSE-C key rotation has its own signed HTTP and decrypted GET test.
- **L4 confirmed:** both the actual SDK default metadata directive and peer metadata-REPLACE shapes are exercised.

## Changes after review

Production code is unchanged from reviewed patch SHA256 `8f6f76ee874c43b0827fde272e8a947f118efb1c1af92bf4a02ef88f93554c1b`. Test-only follow-up adds the L2 normal-routing read and applies the repository's gofumpt formatting. `implementation-manifest.json` records the exact reviewed files; the final verification manifest records the final files, so the two versions are distinguishable.
