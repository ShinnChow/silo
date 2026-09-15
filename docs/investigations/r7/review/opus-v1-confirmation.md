本轮只做回应核验，未重复独立审计；无 shell，未计算哈希、未运行任何测试。

# R7 v1 第二轮确认（Opus 5）

## 1. 版本与哈希

- 冻结方案：`docs/investigations/r7/plan-v1.md`，任务声明 SHA-256 **`7af5705ebbfb0a375956d38dba059095dc16e24558290b1bd35a6ce85b9e1f96`**。
- 处置附录：`docs/investigations/r7/plan-v1.dispositions.md`，任务声明 SHA-256 **`d32d30f8a420f904da6ee039f0461d6281e7da44bd805d416b4970d071f0af32`**。
- 两个哈希均按任务声明采纳，本会话无法计算，校验仍属调用方责任。产品基线 `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`，工作区确认未改产品代码（`docs/investigations/r7/` 为 untracked）。生产补丁仍读 `pr187.diff`，作为提议代码而非我运行的证据。

## 2. 三个争议点的裁定

**N6：我错了，撤回。** `cmd/erasure-server-pool-tags_test.go:258-264` 确有 `type tagTestCapacityDisk struct{ StorageAPI }`，其 `DiskInfo` 把 `Total=Free, Used=0`，并已在同文件 `:131` 被 `TestReplicaWritesPreserveTagOrdering` 使用。我上轮按 “adapter” 字面 grep 漏掉了该类型，所以 plan 第 56 行 “repository already used 的临时容量适配器” 属实。撤回 N6 的事实判断，保留其被接受的部分：临时 `capacity-test-utils_test.go` 不进交付 diff，原始 507 与适配过程需记录。补充一句非阻塞：若正式回归仍需容量适配，直接复用同包内已有类型即可（不算临时旁路），产品容量策略不得改动。

**N1：接受纠正。** `object-handlers.go:2740` 的 `rawReplica` 是整包级判定，`:2788-2791` 对任何缺 `ReplicateObject` 的条目直接 `ErrAccessDenied` 并中止该条目；因此我建议的“同一 REPLICA 包内 trusted 与 untrusted 条目对比”在机制上不可能成立。Codex 的做法正确：同一 tar 分别发 ordinary 与 replica 两次请求，比较条目元数据（排除 replica 状态/时间戳/ETag）。实质结论不变且已被证据坐实——补丁前 `:2835-2838` 的恢复分支会把外层 `supportedHeaders` + 用户元数据整体灌进仅含 storage class 的 `metadata`（`:2802-2804`），补丁后只剩六个映射；`http-baseline-v2.log:195-201` 的 `Erasure/no-pax` 正是外层 `aws-chunked` 泄漏。可选增补（非必须）：再加一条 untrusted-marker 归档对照。

**N5：接受收窄措辞。** 源码只能支撑到：`bucket-replication.go:987-997` 的逐字符串比较 → `replicationActionForTarget:1131` → 仅在 `replicateObjectToTarget:1598` 的复制任务里求值，且成功后置 `Completed`、不自我重排队。所以“每次 heal/resync/重放对账都会再次选中 `replicateMetadata`” 成立，“不间断热循环” 我上轮说过头，撤回该措辞。补救顺序“先确认并修权威源版本、再协调副本”，以及把重复元数据复制/不一致记为已知影响，均予保留。

## 3. 其余处置确认

- **N2 已用实测兑现**：`http-baseline-v2.log` 是 handler 级证据，单盘 `ErasureSD` 与 16 盘 `Erasure` 均覆盖 put/copy-replace/multipart，失败精确落在 `replica/bare`（persisted/GET/HEAD 均为 `aws-chunked`，期望空）与 `replica/mixed`（`aws-chunked,gzip` vs `gzip`），ordinary、untrusted-marker、gzip、unauthorized-replica 全通过；连同 Snowball 8 例，64 叶 = 44 通过 / 20 失败，与附录计数一致。护栏诉求已满足（该日志由本会话之外产生，我只读未跑）。
- **N3 / N7 / N8 / N9**：接受无异议。N3 额外要求断言 XML 码为 `AccessDenied` 以区分签名失败，正确。
- **N4**：接受，且两条期望值与代码一致——`handler-utils.go:357-368` 按 `,` 精确等值比较，故 `"aws-chunked, gzip" → " gzip"`（保留前导空格）、`"gzip, aws-chunked" → "gzip, aws-chunked"`。仅作现状记录，不得宣传为本次修复。
- **最终验收范围补充**：同意。无双站点调度器/重启/网络故障验收时只报告本地接收链路与既有 SSE 结论；存量修复只出设计文件，不授权扫描或改写现网对象。

## 4. 结论

**APPROVE。阻塞问题 0。** plan-v1 + 本处置附录构成的 v1 组合可直接进入实现，无需 v2；N1/N5 采用 Codex 的修正表述，N6 以我撤回告结。实现时请把 N1（无 PAX 行为变化）、N4（空格现状）、N5（收敛风险与补救顺序）落为断言或文档，并确保临时容量适配文件不出现在交付 diff 中。
