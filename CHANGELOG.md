# Changelog

## Unreleased

The entries below describe source changes on main since the latest published Server.
**The latest published Server remains 20260903.** These changes are not in its
binaries, packages or images. See the [component matrix](https://silo.pgsty.com/compatibility/versions/)
and [complete commit range](https://github.com/pgsty/silo/compare/RELEASE.2026-09-03T13-18-01Z...main).

### Authorization and security

- Persist IAM deletion revisions and parent revocation boundaries so stale site
  events cannot restore deleted identities, policies or their older grants
  (#191, #192). Peer deletion notifications reload committed storage; deliberate
  recreation requires a newer revision, and credentials issued before the
  parent's revocation remain invalid.
  **Coordinated upgrade required:** upgrade every participating node and site.
  Mixed old/new nodes sharing an IAM backend and rolling downgrade are
  unsupported. Back up complete IAM storage and encryption material; an admin
  export of live records omits deletion history. Reissue credentials for
  recreated parents and explicitly reconcile pre-upgrade revocations whose
  history is already lost. Restoring an older backup can lose later revocations;
  keep affected sites isolated until reconciliation/rekeying is complete. See
  [the operator runbook](https://github.com/pgsty/silo.pgsty.com/blob/7bd2d57c2ce5aaa804d0b1a2fe0e5eed69d15235/content/operations/replication/iam-upgrade.md).
- Enforce an absolute HTTP/1 request-header deadline through the connection
  wrapper (#196). Repeated small reads no longer extend that deadline, and
  `--read-header-timeout` / `MINIO_READ_HEADER_TIMEOUT` now reaches the HTTP
  server. HTTP/1 request bodies retain the rolling idle timeout; this does not
  impose a total upload/download duration. A shorter setting also constrains
  TLS handshake reads. The wrapper's strict header mode is not applied to HTTP/2.
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

- Preserve object tags during multi-pool metadata reconciliation by reading the
  resolved tag field together with its revision (#189). Previously, reconciliation
  could replace existing tags with an empty value.
- Preserve the tag revision on SSE-KMS metadata replication (#193), and advance
  tag revisions monotonically on local PUT/DELETE tagging (#196). Empty tags
  participate in reconciliation as an ordered deletion, preventing older
  events from restoring removed tags. SSE-C key rotation also retains the tag
  revision. Malformed historical revisions can fail and retry; their missing
  history is not reconstructed by the upgrade.
- Complete delete-marker version purges and preserve their identity and retry
  state through MRF recovery (#196). Recovery accepts a 405 marker response only
  when its version, bucket, object name and modification time match the task.
  Purge audit status is normalized from `COMPLETE` to `COMPLETED`.
  Thanks to Julien Laurenceau (@julienlau) for the investigation and proposed
  fix in #184 that helped shape this follow-up.
- Restore only the six replication-specific metadata fields after ordinary
  request metadata extraction (#194). This prevents transport-only `aws-chunked`
  from being stored as Content-Encoding while preserving the signed-header
  protections. Trusted Snowball entries no longer inherit the outer archive's
  ordinary metadata. Thanks to Mikhail Khadarenka (@chodorenko) for the fix in #187.
  **Existing data:** these repairs prevent new errors; they do not scan or rewrite
  historical object metadata, recover lost tags or prove that old purge work has
  converged. Follow the [read-only audit procedure](https://github.com/pgsty/silo.pgsty.com/blob/7bd2d57c2ce5aaa804d0b1a2fe0e5eed69d15235/content/operations/replication/replica-metadata-audit.md)
  before planning any repair of stored state.

- Evaluate conditional multipart completion against the logical current object
  across all pools while holding the existing object lock. A stale `If-Match`
  can no longer replace newer data in another pool, and the current ETag is no
  longer rejected because the upload resides next to an older copy. Conditions
  are evaluated once; a current delete marker counts as an absent object.
  **Availability change:** if any pool's metadata cannot be read, conditional
  completion fails even when another pool can still serve GET/HEAD. This also
  applies when the unreadable pool may not hold the object: absence cannot be
  verified. Retry after the pool recovers. Unconditional completion and the
  single-pool path retain their existing behavior.

- Evaluate ordinary multi-pool conditional PUT against the logical current
  object across all pools, including draining pools, under the existing object
  lock (#207). A stale destination copy no longer accepts a stale ETag or rejects
  the current one; a current delete marker is treated as absence.
  **Availability change:** if any pool's object metadata cannot be verified,
  the condition fails even when GET can use another pool; read-quorum failures
  return 503. Restore readability or heal before retrying. Unconditional PUT,
  single-pool conditions and internal replication retain their existing behavior.
  A public condition with a destination `versionId` compares the current object
  while preserving the requested write version. This change does not retire
  stale copies in other pools, undo historical accepted overwrites or provide
  a new global clock-ordering guarantee. The multipart-completion repair in #190
  neither introduced nor repaired this separate PUT defect.

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
  reflect actual work, and report bounded MRF drops.
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
