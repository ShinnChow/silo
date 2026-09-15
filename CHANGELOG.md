# Changelog

## Unreleased

The entries below describe source changes on main since the latest published Server.
**The latest published Server remains 20260903.** These changes are not in its
binaries, packages or images. See the [component matrix](https://silo.pgsty.com/compatibility/versions/)
and [complete commit range](https://github.com/pgsty/silo/compare/RELEASE.2026-09-03T13-18-01Z...main).

### Authorization and security

- Reject unsigned `x-amz-*` request headers that could turn a signed PUT into a
  copy of another object accessible to the signer (SN-2026-011). The latest
  public Server is affected; the fix is on main. See [the advisory ledger](docs/security/advisories.md).
- Align signed request fields with policy conditions and enforce header-only
  presigned payload checksums. See [the signed-header review](https://silo.pgsty.com/blog/design/signed-header-coverage/).
- **Breaking policy semantics:** separate self-service `admin:ChangeMyPassword`
  from `admin:CreateUser`. Built-in read-only policies follow the split. Preserve
  both denies if the previous combined restriction must survive upgrades or
  rollback. Saved policies are not rewritten. Deploy with the matching Console
  and pkg; see [the migration guide](docs/iam/password-permissions.md).

### Object storage and replication

- Make `ListMultipartUploads` discover quorum-valid uploads from durable state
  across pools, erasure sets and drives, then apply S3 prefix, delimiter,
  marker, ordering and 1,000-entry pagination semantics globally. New uploads
  store their canonical bucket and key as reserved fields in the existing
  quorum-written `xl.meta`; completion removes those upload-only fields. During
  rolling upgrades, detection of any legacy keyless upload retains the prior
  listing behavior until those uploads drain. See [issue #79](https://github.com/pgsty/silo/issues/79)
  and its [design record](https://silo.pgsty.com/blog/design/list-multipart-uploads/).

- Evaluate conditional multipart completion against the logical current object
  across all pools while holding the existing object lock. A stale `If-Match`
  can no longer replace newer data in another pool, and the current ETag is no
  longer rejected because the upload resides next to an older copy. Conditions
  are evaluated once; a current delete marker counts as an absent object.
  **Availability change:** if metadata cannot be read from any pool, conditional
  completion fails even when another pool can still serve GET/HEAD. This also
  applies when the unreadable pool may not hold the object: absence cannot be
  verified. Retry after the pool recovers. Unconditional completion and the
  single-pool path retain their existing behavior.

- Reconcile ordinary single-object version DELETE across all pools, including
  null versions, delete markers and unqualified directory-marker DELETE. This
  applies the deletion to every resolved pool copy under existing quorum
  rules. Pending outbound delete replication retains versions until the
  existing replication worker completes their purge; a successful response
  does not imply immediate physical removal from every drive. Unreadable
  pools now consistently return 503 instead of depending on pool traversal
  order; insufficient read quorum returns `SlowDownRead`. This extends the
  existing failure surface. Retry after recovery.
  Cleanup failures also return an error. Batch deletion already fans out across
  pools; replication and scanner cleanup keep their existing contracts. See
  [scope and limitations](docs/bucket/lifecycle/access-tiering-removal.md#version-deletion-scope).

- Remove the opt-in GET-frequency pool-tiering feature from PR #60, including
  its tracker, mover, scanner hooks, configuration, XML actions and metrics.
  Accept and ignore retired configuration/XML and preserve ordinary statistics
  when reading v9 caches. See [migration notes](docs/bucket/lifecycle/access-tiering-removal.md).
  The [decision record](docs/investigations/access-tiering-revert.md) preserves
  the feature's introduction, subsequent fixes, rollback scope and review history.
- Preserve the independent multi-pool write, metadata, healing and conditional
  deletion fixes from PR #178, including shared remote-tier reference protection.
- Enforce `If-Match` on DELETE, preserve retention and independently ordered
  Object Lock/tag updates, and correctly retransmit encrypted replicas.
- Preserve plaintext part sizes and raw SSE-C replicas; prevent SSE-C
  compression, honor key-rotation checksums, and complete attributes pagination.
- Repair federated CopyObject checksums, destination timestamps, reserved
  metadata, encrypted-object forwarding, legal hold and KMS context.
- Make resync counters, target selection, cancellation and worker lifetimes
  reflect actual work; complete delete-marker purges and report bounded MRF drops.
- Converge bucket metadata with deterministic source state, deletion tombstones,
  creation time recovery and diagnostics. The mixed-version export gate requires
  coordinated upgrades before tombstones are exported. See [the #77 record](docs/investigations/issue-77-current.md).
- Include per-bucket CORS in metadata export/import, close metadata publication
  and logger races, and report effective bucket quotas in metrics.

### Console, dependencies and delivery

- Restore embedded Console login over loopback TLS, trusted-proxy handling and
  all four WebSocket connection limits. Preserve Go TLS defaults across transports.
- Directly require `github.com/pgsty/silo-pkg/v3` v3.14.0; select Console
  `v0.0.0-20260913015128-417559bb2c97` and MC
  `v0.0.0-20260913012246-4f609a4da3bb` with explicit PGSTY replacements.
- Pin upstream minio-go `v7.3.1-0.20260910142817-60bd07042d49`; refresh Go x/*
  modules and security fixes including bounded AMQP frame handling. Keep Go
  1.27.1 and go-systemd v22.6.0's NetBSD compatibility replacement.
- Refresh container base digests and build static curl 8.22.0 from verified
  source for both Linux architectures. Pin the actual mcli 20260913 archives and
  hashes. Helm's client image follows that release; its Server image still names
  the latest published Server 20260903.

The dependency update passed the final candidate's Go, vulnerability and Test
Release workflows; native curl builds passed on both architectures. A local
ARM64 image passed startup, health, S3 transfer and embedded Console checks.
These checks do not publish a Server tag or production image and do not replace
cluster upgrade/rollback acceptance for the next release. Dated investigations
retain the exact source and runtime boundaries they tested.

## RELEASE.2026-09-03T13-18-01Z

Published source: `9b11dc9469e650815b775cb47b039610644f5da4`.
[Complete release notes](https://silo.pgsty.com/blog/release/silo-20260903/) ·
[GitHub release](https://github.com/pgsty/silo/releases/tag/RELEASE.2026-09-03T13-18-01Z)

This release ships Go 1.27.1, silo-pkg v3.13.2, upstream minio-go `0e78d3f18efe`,
mcli 20260903 and embedded Console source `464a59d73ada` (v2.3.0 version identity).
Installing the newer standalone mcli or Console does not replace components
inside this existing Server binary or image.

Earlier releases: [release archive](https://github.com/pgsty/silo/releases).
