# Issue #77 当前核验与最小修复建议

> 历史快照（2026-09-12，实施前）。其中的 Issue 状态、待办和方案约束只描述当时情况；最终实现、修正与验收以 [归档总记录](issue-77.md) 为准。

核验日期：2026-09-12。对象：[SILO #77](https://github.com/pgsty/silo/issues/77)。代码基准：远端 `main` 的 `5c576581631561c446f30ae5b566f0aa793adc1c`，在独立 detached worktree 中运行测试。用户工作目录仍位于 `12f631b50`；两者差异为 #179 的 federation 修复，不涉及本次复制元数据代码。

**结论：问题真实，当前主干仍未修完，应保持打开。原方案的 A/B 核心必要，但不能照搬 8 月的实施清单；已有基础设施可以复用，同时必须补上批量复制入口和相同内容的时间戳同步。**

本次是分析与复现，没有修改产品代码、提交、发布或修改 GitHub Issue。下述测试是本地 ObjectLayer 与进程内 HTTP/RPC 复现，不是线上多站点验收。

## 1. 当前状态

GitHub API 实时返回 #77 为 `OPEN`，未分配负责人或 milestone，最后更新为 `2026-09-08T14:45:10Z`。核验时仓库没有打开的 PR。

| 范围 | 当前事实 | 是否仍属遗留项 |
| --- | --- | --- |
| #77-C，各站点统计 | [#91](https://github.com/pgsty/silo/pull/91) 于 8 月 29 日合并；本次现有统计回归测试通过 | 否，不应重复实现 |
| #77-A，heal 选源与输入 | 仍先用 map 首项初始化，再跳过创建默认值；Tag 远端 heal 仍不携带时间戳 | 是 |
| #77-B，源时间与删除状态 | 六类专用处理函数仍调用到达时间写入路径；部分删除状态不导出 | 是 |
| #77-D，持续不一致诊断 | 已有损坏配置日志；旧事件静默跳过、默认状态无法选源等诊断未完成 | 是 |
| Object Lock 错误 wire 字段、接管桶覆盖已有配置 | [#76](https://github.com/pgsty/silo/issues/76)、[#78](https://github.com/pgsty/silo/issues/78) 已关闭，对应 #89、#90 已合并；相关测试本次通过 | 否 |
| 元数据整记录并发写、删除后重建 | [#103](https://github.com/pgsty/silo/pull/103)、[#156](https://github.com/pgsty/silo/pull/156) 已提供桶锁和保存前物理桶检查；删除后排队写测试本次通过 | 基础已具备，但不能代替同字段时间排序 |
| 评论提及的 MRF 丢弃观测、delete-marker purge | [#152](https://github.com/pgsty/silo/issues/152)、[#153](https://github.com/pgsty/silo/issues/153) 已于 9 月 9 日随 [#162](https://github.com/pgsty/silo/pull/162) 关闭 | 不再列为 #77 的待实现项 |

#162 明确保留“405 表示 marker 仍存在”的语义，没有采用报告人建议的“405 一律判定永久删除完成”；它也未声称复现外部报告的请求量或持续 405 风暴。本次只核验其合并/关闭状态与提交说明，没有重跑那组故障实验。

最新公开 Release 仍是 `RELEASE.2026-09-03T13-18-01Z`。#77 的状态不能用“已有统计修复”或“已有桶锁”推导为已修复，也不能用较晚 PR 的合并推导为已发布。

## 2. 已复现的真实问题

最直接的例子：源端 10:00 PUT 配置，10:01 DELETE；目标端积压到 10:05 才接收 PUT，并把配置时间写成 10:05。随后接收源时间为 10:01 的 DELETE，判断它比本地旧，返回 HTTP 200，却保留配置。这个例子不需要机器时钟偏差，只需要事件延迟。

根因在 [updateAndParse](https://github.com/pgsty/silo/blob/5c576581631561c446f30ae5b566f0aa793adc1c/cmd/bucket-metadata-sys.go#L129)：它在锁内统一使用 `UTCNow()`；专用 peer handler 在调用它之前，用 getter 返回的时间做比较。

| 配置类型 | peer PUT 保留源时间 | 较新源 DELETE | 本地删除后旧 PUT | 删除时间在元数据导出中可见 |
| --- | --- | --- | --- | --- |
| Policy | 否 | HTTP 200，但未删除 | 会复活 | 是 |
| Tags | 否 | HTTP 200，但未删除 | 会复活 | 否 |
| SSE | 否 | HTTP 200，但未删除 | 会复活 | 否 |
| Quota | 否 | HTTP 200，但未删除 | 本次未复活，getter 保留删除时间 | 否 |
| Versioning | 否 | nil 正确地不修改配置 | 不适用 | 不应套用可删除配置规则 |
| Object Lock | 否 | nil 正确地不修改配置 | 不适用 | 不应套用可删除配置规则 |

矩阵每项均在 `ErasureSD` 与 16 盘 `Erasure` 两个本地后端执行。六类专用事件通过实际签名 admin 路由调用，检查落盘状态；导出调用实际 `SiteReplicationMetaInfo`。不能把 Quota 与 Policy/Tags/SSE 的 getter 行为写成完全相同。

另外复现了以下路径：

1. **检查与写入不在同一临界区。** 阻塞旧 Tag 事件的锁获取，在锁内提交较新 Tag，再释放旧事件；旧事件仍覆盖新内容。#103 解决跨字段丢更新，没有解决 handler 的 getter → Update 竞态。
2. **批量元数据入口绕过逐字段排序。** 已持久化较新 Tag 后，发送较旧 bulk 事件，实际 admin 路由返回 HTTP 200，Tag 内容和时间都倒退。该入口被 [桶元数据导入](https://github.com/pgsty/silo/blob/5c576581631561c446f30ae5b566f0aa793adc1c/cmd/admin-bucket-handlers.go#L1106) 使用；[PeerBucketMetadataUpdateHandler](https://github.com/pgsty/silo/blob/5c576581631561c446f30ae5b566f0aa793adc1c/cmd/site-replication.go#L1608) 只检查桶创建时间，非 CORS 字段缺少与已有字段时间的比较。这是旧实施清单漏掉的入口。
3. **默认值能够成为错误的 heal 来源。** 两站点状态中，一个有真实 Policy，另一个只有更晚的 `CreatedAt == PolicyUpdatedAt` 默认值。最后一次复现分别有 5/32、2/32 轮错误删除有效 Policy，取决于 Go map 遍历顺序。该计数只是复现样本，不能推断生产发生率。
4. **远端 Tag heal 丢失源时间。** 实际 HTTP 捕获的 `UpdatedAt` 为零，见 [发送字段](https://github.com/pgsty/silo/blob/5c576581631561c446f30ae5b566f0aa793adc1c/cmd/site-replication.go#L5011)。
5. **同内容、不同时间戳没有同步。** 即便强制 `TagMismatch=true`，内容比较仍使 heal 跳过，较旧排序时间保持不变。两站点看似内容相同，之后却可能对同一个延迟事件做出不同决定。旧方案只统一写入时间、保留全部 heal 跳过条件，仍不完整。
6. **Quota 的 nil heal 留下缓存。** 向现有 heal 传入显式较新删除状态，磁盘 `QuotaConfigJSON` 被清空，但 `GetQuotaConfig` 仍返回 1024 字节的旧硬配额。这里显式构造了删除状态，因为当前 exporter 正在隐藏它；这是新增 tombstone 导出不能直接交给旧 heal 的具体证据。

从当前 `git blame` 和基线比对看，核心错误继承自 MinIO：heal 初始化逻辑来自 2022 年 `3a64580663`，专用 handler 的旧事件检查来自 2022 年 `7cc9286e0f`，bulk 入口来自 2023 年 `0cde37be50`；0806 基线已包含相关逻辑。此次没有发现其由 SILO 最近的 CORS 或统计修改引入。

对于使用 site replication 的部署，建议按 P1 正确性问题处理：撤销的桶策略可能保留或复活，默认加密和配额也可能与源端不一致。没有启用站点复制的正常单站点请求不触发这些复制路径；本次没有证明对象数据本体丢失。

## 3. 最小且必要的修复范围

**第一部分：收束 A 的选源和输入修复。** 提取一个小的内部选择函数，在初始化候选之前排除零时间和创建默认值；无可信候选时不写入。六类 heal 使用它，补齐 Tag 的源 `UpdatedAt`。Versioning/Object Lock 遇到 nil 保持 no-op，Quota 删除使用正确的删除路径。不能重新做 #76/#78 或把 Lifecycle、CORS 一并重构。

**第二部分：收束 B 的状态更新，包含本次发现的入口遗漏。** 在既有 `metadata.lock` 内完成读取原始字段时间、判旧、写入源时间和保存；普通本地写入保留现有契约。复用 `updateAndParse` 的类型分支及现有 `saveMetadata`，不再从公开 getter 获取删除时间，也不改变 getter 的 S3 错误语义。peer 更新和删除保留各自解析规则，内部返回是否应用，重复且相同的状态不重复保存。

本地 heal 必须使用同一排序路径，不能选好源后调用普通 `Update/Delete` 再生成到达时间。批量复制入口也必须在其现有整记录锁内逐字段判旧；bulk 的 nil 继续表示“未提供此字段”，不能改成批量删除。heal 需要同步较新的时间戳，即便内容已经一致；这不要求重写 #91 的计数或增加公开 API。

当前 `saveMetadata` 已在锁内检查物理桶存在，桶删除也使用同一把锁。8 月方案里另加一套 `peer-require-existing` 防幽灵桶机制已没有必要，应保留并复用现有保护。确有 `Created == 0` 的历史桶如何补齐创建时间仍应覆盖，但不能退化当前所有写入都执行的存在性检查。

**第三部分：单独启用删除时间导出，补足轻量诊断。** Policy 已导出时间；实际新增的是 Tags/SSE/Quota 的 nil payload 时间。先使接收与 heal 能正确处理删除，再开放这些状态。混合旧版 SILO 的风险已经有 Quota 缓存复现支撑；这是产品自身的滚动升级问题，不是要求兼容未经修改的上游 MinIO。

可以使用仅针对这项导出的明确 opt-in：默认关闭新增导出，全站点升级到具备 A/B 修复的版本后启用。若项目选择自动能力协商，可后续单独实现；不必为了它阻塞没有新增 wire 字段的选源与接收端修复，也不需要为本 Issue 建一个通用能力框架。具体开关尚未实现，不应把这里的建议当成现有配置。

诊断只需解释“过旧事件”“早于桶创建”“只有默认/未知候选”等跳过原因，使用现有有界去重/限频设施，保持 RPC 的既有成功语义。不需要另建重试队列、每轮日志或大型监控系统。

有三项边界必须在实现中明确：

- 旧版 Tag heal 会发送零时间。新实现不能无说明地全部拒绝；可保留明确的兼容降级路径，在旧节点存在时不承诺完整排序收敛。
- 相同时间戳、不同内容的冲突：只给 heal 增加 deployment ID 平局规则，并不能让到达顺序不同的 peer apply 本身确定。若承诺此类冲突也收敛，apply/heal 应共用一个类型内比较规则；可参考 CORS 的删除优先和载荷排序，不需要新增持久化源站 ID。若不纳入本轮，必须列为剩余边界，不能称全量收敛审计完成。
- 旧版本已经写坏的到达时间无法从现有记录还原。升级不会自动恢复历史操作顺序；应由操作者确认权威状态，并在升级和时钟检查后重新提交相关配置/删除，再验证各站点。

因此，建议交付为有限的选源修复、统一源时间更新、删除导出与诊断三个可审核部分。已有锁、物理桶保护和 C 的计数修复直接复用。预期产品修改集中在 Server 的 site-replication 与 bucket-metadata 路径，无须为核心修复升级 Console/mcli/silo-pkg、改存储格式或重写复制架构。

## 4. 验证证据与关闭条件

本次最后一轮执行：

```text
GOWORK=off go test -tags kqueue,dev ./cmd \
  -run 'TestIssue77Current|TestSiteReplicationStatusAccountsPerSiteAndSurvivesMalformedConfig|TestPeerBucketObjectLockMetadata|TestPeerBucketAdoption|TestQueuedMetadataUpdateAfterDelete' \
  -count=1 -timeout=5m -v
```

四组审计复现测试包含 24 个后端/场景子测试，均暴露预期的现存缺陷；八个现有回归测试通过。总命令退出码为 1，原因是上述针对期望正确行为的断言失败，不代表修复验收通过。未运行全量测试、race suite、真实多站点中断/重启或滚动升级实验。

保留的证据：

- [完整运行日志](issue-77/current-tests.log)
- [源时间、删除和 bulk 审计测试](issue-77/issue77_review_test.go.txt)
- [heal 与锁竞态审计测试](issue-77/issue77_heal_review_test.go.txt)

测试以 `.go.txt` 保存，避免将故意失败的审计用例加入正常 Go 测试集。可在上述 SHA 的独立 worktree 中复制为 `cmd/issue77_review_test.go` 和 `cmd/issue77_heal_review_test.go` 后重跑。创建默认值测试使用重复 map 遍历来观测缺陷，正式回归应在抽出选择函数后改成确定性用例。

关闭 #77 前需要：上述失败场景转为正确行为；分别覆盖六类配置、落盘重载、漏发/重复/乱序、同字段竞态、同内容新时间、批量导入入口、legacy 桶、删除后的桶及混合版本。全站点升级后的实际断线重连/重启验证应保留独立证据。当前结论是“缺陷与实施范围已核实”，不是“修复完成”。
