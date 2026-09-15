# Current-baseline reproduction

Base: `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`. Go 1.27.1, macOS arm64. Production sources unchanged. Temporary overlay injects regression tests; real erasure disks persist object metadata. Capacity adapter changes only reported capacity to avoid the developer machine's unrelated disk-usage threshold.

## Commands and raw evidence

Raw directory: `/Users/vonng/tmp/silo-r5-20260915-77ad/`.

```sh
GOMAXPROCS=2 go test -p 1 -overlay /Users/vonng/tmp/silo-r5-20260915-77ad/overlay.json ./cmd -run '^TestReviewR5' -count=1 -v
GOMAXPROCS=2 go test -p 1 -overlay /Users/vonng/tmp/silo-r5-20260915-77ad/overlay.json ./cmd -run '^TestReviewR5Queued' -count=1 -v
```

Both exit 1, as expected before repair. Files: `baseline.log`, `baseline-extended.log`, `baseline-ack.log`; the full injected source is `baseline_test.go`.

## Observations

| Regression | Observed result |
|---|---|
| Empty source tags with explicit revision | putReplicationOpts returns zero TaggingTimestamp |
| Signed HTTP DELETE on a versioned object with replication selected | 204, empty tags, one queued event, unchanged old timestamp |
| Delayed signed trusted COPY after DELETE | 200 and deleted tags restored |
| Newer empty signed COPY | 200, old nonempty tags/time remain |
| First signed replica PUT carrying tag timestamp | 200, timestamp absent in stored object |
| First signed replica multipart initiation carrying timestamp | 200, timestamp absent in persisted upload metadata |
| Equal empty source/target values, source has newer deletion revision | getReplicationAction returns none |
| Same ETag/version with newer trusted source tag revision | checkPreconditionsPUT skips request |
| Process an old queued tagging event after a stored deletion | replication completes; source ACK restores `key=queued` with the deletion timestamp |

All HTTP/storage cases above ran on both ErasureSD (one real disk) and Erasure (16 real disks). The last case uses a local HTTP protocol peer for replication responses and the real source object layer. It manually persists the deletion revision before processing the old queue snapshot to isolate the ACK defect from the separate DELETE-handler defect. The worker reads current deleted tags, yet its completion callback restores stale queue tags.

These are component/in-process HTTP integration results, not multi-site production acceptance.

## Provenance

Current git history attributes introduction of ReplicationSourceTaggingTimestamp in COPY to upstream `c4373ef29` (2021-09-18, multi-site replication). COPY sender timestamps were added in `3781a0f9a` (2023-12-13), with default tag timestamps in `64a8f2e55` (2025-02-04). Queue-snapshot tag reassignment traces to `fa6d082bf` (2023-09-16). The storage tag reconciliation fix `3ce831925` is already present in this baseline and does not cover the HTTP/transport or queue-ACK omissions.

No historical state can prove a missing deletion time. The planned repair records future revisions; a production backfill would need separate authoritative evidence and authorization.
