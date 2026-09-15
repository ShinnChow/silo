# Conditional multipart completion across pools

## Defect and scope

An unfinished multipart upload can remain in one pool while another pool holds
the logical current object. Evaluating `If-Match` against the upload pool's
local copy can accept a stale ETag or reject the current ETag. The pools object
lock serializes writes, but a local read still does not identify the logical
current object.

This layout does not require rebalance. Commit
`83b2ad418b15ff0fa78175e2014d78d02046edd8` (upstream #21115) made `getPoolIdx`
choose an available pool even when `pinfo.Err == nil`: normal overwrites and
upload initiation can select different pools. This change predates the SILO
multi-pool consistency work. The deterministic regression fixtures place copies
and uploads directly in real erasure pools; they do not claim to run rebalance.

## Minimal correction

For multi-pool conditional completion, retain the existing object lock and use
`objectPoolInfos` to read the logical current object before completing the
upload. An unreadable pool is an error, not proof of absence. The first sorted
copy supplies the ETag and encryption metadata used by the existing callback.
A current delete marker is treated as an absent key. `If-Match` then fails for
an absent object; `If-None-Match: *` may proceed.

Use explicit read options with an empty `VersionID` and `NoAuditLog: true`.
The precondition concerns the logical current object, independently of an
internal completion's destination version. After a successful check, clear
the callback before entering the set layer, so it is evaluated only once.
No new lock, storage format, replica cleanup algorithm or distributed protocol
is introduced. Single-pool and unconditional completion retain their existing
paths.

## Availability and validation

If any pool cannot supply the required metadata, conditional completion fails,
even when GET/HEAD can still read a copy from another pool. The unreadable pool
might hold a newer object, a delete marker, or no copy at all; none of these
possibilities can be assumed. Retry after recovery. This behavior is recorded
in the unreleased changelog.

The regression suite covers both upload-pool directions, stale/current ETags,
`If-None-Match: *`, absent objects and delete markers, read-quorum errors,
explicit destination versions, tied modification times, callback counts,
upload preservation, signed HTTP error bodies and concurrent completions.
The ordinary HTTP routing control accepts every valid placement; deterministic
fixtures provide the cross-pool regression gate.

## Separate follow-up scope

The pool-placement change and conditional checks in `PutObject` and
`NewMultipartUpload` require separate assessment. This completion fix does not
repair those paths. In particular, PUT has live destination-version and
preserved-ETag semantics, so its repair must not copy this completion-specific
empty-VersionID rule without examining that contract. Parallelizing the shared
pool metadata reader is also outside this correctness fix.
