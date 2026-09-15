# R6 v1 review disposition

Actual reviewer: Claude Code 2.1.270, assistant model `claude-opus-5`, explicit effort max. Original verdict REVISE, one blocker; see opus-v1-review.md and opus-v1.metadata.json. No consensus or production implementation at this stage.

| Item | Disposition | v2 change / evidence |
|---|---|---|
| B1 creation-status pin rewrites a partial target set | Accepted. The disk no-update semantics are preferable and smaller. | Purge results leave ReplicationStatus empty, only VersionPurgeStatus changes; assert full persisted creation state and timestamp across partial fan-out/repeated failure. No generic state merge rewrite. |
| N1 wire version fallback | Accepted. | Existing DeleteMarkerVersionID fallback retained; tests assert query version and false marker flag for every purge shape. |
| N2 queue-full retry budget | Accepted. | All three queueMRFSave sites for deletes increment RetryCount, including queueReplicaDeleteTask. |
| N3 completion statistics change | Accepted. | Check concrete Heal/ExistingObject counter deltas for COMPLETE-to-COMPLETED conversion. |
| N4 null/empty versions | Accepted as current boundary. | 405 recovery requires a real nonempty identity; empty/null special cases remain outside acceptance. |
| N5 live producer/old-shape scope | Accepted. | Old tasks are not serialized; robustness path distinguished from currently active MRF defect. |
| N6 detached MRF execution | Accepted. | Real disk persistence each round, new pool, synchronized queue receives with bounded timeout, no sleeping for presumed completion. |
| N7 fixture threshold | Resolved with actual execution. | Existing capacity adapter; baseline canonical and old scanner recovery pass. PR MRF canonical recovery completes on single/16-drive fixtures; old-shape failure still queues zero entries. |

Correction to our research inference: a canonical purge's empty returned creation result causes replicatedInfos.ReplicationStatus() to report PENDING, but that does NOT show loss of the disk creation block. xlMetaV2 skips that block update when the composite creation status is empty. The temporary probe's in-memory assertion was too strong; do not promote it into a disk-state defect. The PR old-shape missing-MRF observation and all target-branch observations remain valid.

The local-source metadata-write error path and missing-client/purge-subset merge behavior are explicitly documented acceptance limits. They are not new claims of convergence. v2 does not broaden the repair into those independent mechanisms.
