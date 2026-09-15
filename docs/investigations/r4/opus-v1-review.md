## R4 独立评审（Opus 5.0，只读复核）

**计划**：plan v1 — `docs/investigations/r4/plan-v1.md`
**哈希（按任务给定）**：`ad539f2071155de6955b583991684ed33c4bfe2e29660005840cdc97d7e1a754`
**Baseline**：`9ebe81c1b3611f9cc73e676b5b741c2be62c467a`

### 裁定：GO_WITH_NONBLOCKING_NOTES

无阻断项。**我同意这份 exact plan（上述哈希）可以进入本地实现。** 下列 R4-01…R4-08 均为非阻断；其中 R4-02/04/05 的测试建议是**可选**的，不自动并入必做范围。

（说明：本会话 Write 工具被禁用，评审仅以正文返回，未写入任何文件，也未改动任何源码。）

### 我实际核验到的关键事实（支撑"单字段补丁正确且充分"）

1. **缺陷确认**：`cmd/object-api-options.go:449-460` 的 KMS 字面量带了 MTime/PreserveETag/ReplicationRequest + 两个 Object Lock 时间戳，独缺 tagging；默认路径 `:473` 有。补丁片段中的变量名 `taggingtimestmp` 与 `:419` 完全一致，可直接编译；gofmt 对齐由更长的两个 Lock 键决定，不会扰动他行。
2. **影响面封闭**：全仓 `ReplicationSourceTaggingTimestamp` 只在 `cmd/object-handlers.go:1820` 被读取（定义于 `object-api-interface.go:99`）。因此该字段对 PUT/分段路径天然无效果——既印证 R4/R5 的切分合理，也说明补丁不可能回归其他路径。
3. **充分性的关键点（我重点查证的风险）**：`encMetadata` 只有在 SSE-C 轮换分支 `object-handlers.go:1648-1659` 才批量快照全部保留键，而该分支与 KMS options 分支互斥（目的端是 SSE-C 时 `crypto.S3KMS.IsRequested` 为假）。故 `:1903` 的 `maps.Copy(srcInfo.UserDefined, encMetadata)` **不会**覆盖 KMS COPY 新写入的 tags/时间戳 —— 单字段补丁在 R4 边界内充分。
4. **三个触发点准确**：`bucket-sse-config.go:135-153`（显式请求优先 → nil 配置 + AutoEncrypt → KMS → bucket 默认 KMS 写 header+keyID；默认 AES 走 AES 分支），配合 `object-handlers.go:1428-1433` 仅在非联邦时套用目的端默认。
5. **REPLACE 副本路径准确**：`reconcileStoredObjectTags`（`erasure-server-pool-consistency.go:232-243`）语义即"存量有效时间戳胜过缺失/更旧/相等的 incoming，并连同 tag 值一起还原"。KMS 目的端因 `isTargetEncrypted` 使 `metadataOnly=false`，实际落到 `erasure-server-pool.go:1499-1513`（`ReplicaLockReconcile` 经 `:1509` 透传）→ `erasure-object.go:1276-1316`，在 `cloneMSS`(:1324) 之前于写锁内完成对账；纯元数据路径走 `erasure-object.go:136-139`。计划同时引用 `:136` 与 `:1509`，判断正确。
6. **证据可信**：`baseline-repro.log` 中 options 用例非 KMS 保留 `...123456789Z`、KMS 返回零值；COPY 用例 6/6（ErasureSD + Erasure16 × explicit/auto/bucket KMS）失败，且均为 200、`kms=true`、明文 GET 通过、tags 停在 `key=old`。即"请求成功、加密正常，但复制标签被静默丢弃"，与计划表述一致，未夸大。
7. **修复后推演**：newer/stale/duplicate/newer-again 在 handler(:1817-1833) 与写锁对账的双重排序下分别得到 new/new/new/latest，与测试期望吻合；旧发送端不带 `X-Minio-Source-Tagging-Timestamp` 时仍为零值 → 行为不变，兼容性主张成立。

### 问题清单

| ID | 阻断 | 内容与建议 |
|---|---|---|
| **R4-01** | 否 | 行号漂移：计划写的 `1851/1910`，实际是 `object-handlers.go:1847`（`ReplicaLockReconcile`）与 `:1903`（encMetadata merge）。建议更正引用。 |
| **R4-02** | 否（建议可选） | `ExecObjectLayerAPITest` 两种后端均为**单 pool**（`test-utils_test.go:216` `mustGetPoolEndpoints(0, ...)`），故 `erasure-server-pool.go:1443` 多池分支未被覆盖；且 KMS 目的端命中的是 PutObject 重写对账而非 `CopyObject:136`。建议在计划或测试注释中点明"单盘/16 盘均为单池"；补多池覆盖**可选**，不必进必做范围。 |
| **R4-03** | 否 | 措辞：`crypto.Requested`（`internal/crypto/sse.go:74`）只检查**目的端** SSE 头，因此仅带 SSE-C *copy-source* 头的请求在 KMS 默认桶/自动加密下仍会进入 KMS 分支（归入触发点 2/3，枚举仍完整）。建议澄清 "source encryption alone…" 一句。 |
| **R4-04** | 否（**可选**） | 建议在 options 矩阵里加一条 KMS 分支 vs 默认分支的**逐字段等价断言**（MTime/PreserveETag/ReplicationRequest/三个复制时间戳）。这是阻止第三次复发最廉价的护栏（2021 漏、2026 补了两个 Lock 时间戳仍漏此项）。计划第 1 条已基本覆盖，此为结构化建议。 |
| **R4-05** | 否（**可选**） | 建议加一例"KMS 目的端 + 有 tags 但无 tagging 时间戳头 → 存量不变"，把兼容性主张钉在 handler 层而不仅在 options 层。 |
| **R4-06** | 否（范围外，仅登记） | 同一 KMS 字面量相对 `getDefaultOpts`(`object-api-options.go:40-44`) 还遗漏 `ProxyHeaderSet/ProxyRequest/Speedtest`；`opts.Speedtest` 在 `erasure-object.go:1625` 被读取，全局自动加密下 speedtest PUT 会丢该标志。**不要在 R4 修**，且当前也不在 R5 声明范围内，建议单列条目登记。 |
| **R4-07** | 否 | REPLACE 时 `getCpObjMetadataFromHeader:1143-1156` 会重建 map，`lastTaggingTimestamp` 为空 → handler 对 stale 事件**恒接受**，真正的拒绝来自写锁内对账。因此回归测试必须断言**最终落盘状态**（现有复现已如此），不要改为断言 handler 层行为。 |
| **R4-08** | 否（不确定性） | 本会话无 shell，无法独立复算计划 SHA-256、验证 `c4373ef290 / b2dca43fda / cfefc049c` 历史归属与 PR #184/#187。可由 `shasum -a 256 docs/investigations/r4/plan-v1.md` 与 `git log -L` 输出消解；均不影响补丁正确性。`cfefc049c` 的 KMS context 编码修复实体（`:436-444` 的 `sdkContext`）仍在，补丁不触碰。 |

### 对计划各主张的逐项裁定

- 单字段补丁**正确且对本 bounded issue 充分**：同意（依据 2/3/7）。
- 三个目的端 KMS 触发点**描述准确**：同意（R4-03 仅措辞澄清）。
- 当前 REPLACE 副本路径**描述准确**：同意（R4-01/02 属引用精度）。
- 回归矩阵与存量状态说明**充分**：同意；存量部分"不自动回填、丢失源时间不可重建、并列/更旧事件不保证修复"的表述与 `reconcileStoredObjectTags` 实际语义一致。
- 信任边界、错误行为、加密 key/context、tie 语义、兼容性：补丁均未触碰，维持不变。

### 交付

本轮为**计划共识**，非实现验收。我未作任何源码或文件修改；R4 可按 plan v1 在本地实施，实施后的差异与测试证据需另行验收。
