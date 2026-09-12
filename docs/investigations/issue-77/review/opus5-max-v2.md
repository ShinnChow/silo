# SILO #77 修复计划 v2 对抗复审（Claude Code Opus 5，只读）

基准 worktree = main `5c576581631561c446f30ae5b566f0aa793adc1c`。本轮只针对 v1→v2 的修订差异做源码核验：未改产品代码、未运行测试、未调用其他代理。

## 首轮三个阻断的关闭判断

**R1（零配额发送端改写 / 落盘≠出站）：已关闭。** v2 取消 `admin-bucket-handlers.go:97-99` 的 `Quota=nil` 改写后，`mc quota clear` 两侧都落 live 零值文档：`bucket-quota.go:99-110` 对 `{}`/`null` 均解析为零值且 `IsValid()` 为真，`isBktQuotaCfgReplicated`（`site-replication.go:3850-3888`）此时 `numquotaCfgs==total`、Type 相同 → 判为已复制，`quotaCfgSet`（`3635`）仍为 false，#91 计数不受影响。接收端 `PeerBucketQuotaConfigHandler:2089` 会重新 `json.Marshal` 导致字节不同，但 v2 §2 规定 Quota 比较键取"解析结果的 JSON 编码"，`madmin.BucketQuota` 是定长结构体、编码确定，不会因此空转。Policy 侧改成本地/导入/bulk 统一按 `IsEmpty()` 删除，与既有 `admin-handlers-site-replication.go:233-237` 一致，三元组不变量成立。

**R2（saveMetadata 静默归一 / §6 不可实现 / 字节序方向未定）：已关闭。** `saveMetadata`（`bucket-metadata-sys.go:232`）全部 6 个调用点（`site-replication.go:961/1708/2049`、`bucket-metadata-sys.go:203/222/354`、`admin-bucket-handlers.go:1095`）传入的都是调用方自有值，改指针回写无别名风险；导入 hook 改用提交快照后，`1110-1127` 与落盘文档不再分叉；比较改用 `parseAllConfigs:391-399` 同一条 Object Lock→Enabled 归一规则，方向也已写明"键较大者胜"且禁止用字节序补偿隐式改写。

**R3（bulk 删除语义）：已关闭，且首轮依据确属错误。** 复核确认：`json.RawMessage` 显式 `null` 解码为非 nil 的 `[]byte("null")`（探针 log 第 3-4 行），`policy/bucket-policy.go:78` 允许 `Version==""`，故 `null` 与 `{"Version":...,"Statement":[]}` 都能通过 `ParseBucketPolicyConfig` 且 `IsEmpty()` 为真；`*string` 的 `null` 才退化成 nil。v2 的逐字段规则与 `PeerBucketMetadataUpdateHandler:1653-1697` 的 `!=nil` 约定一致，也堵住了"`len==0 ⇒ 删除"的错删诱导。唯一 bulk 生产者（导入 `1103-1131`）在 `omitempty` 下漏发空 Policy 的问题，v2 用专用 nil 事件补齐，处置正确。

## P1（新增阻断，1 项）

**P1-1：`fieldTime <= Created` 一律判 baseline，会让历史桶的配置在初次同步中被静默丢弃且 heal 永不修复。**

反例（全部为已修复版本、同一桶世代、合法带时间事件，属计划承诺范围）：

1. A 站点有 2020 年前迁移来的桶。`applyLegacyConfigs`（`bucket-metadata.go:482-513`）不写任何 `*UpdatedAt`，`convertLegacyConfigs` 保存后磁盘上这些字段仍是零值；每次 `loadBucketMetadataParse:236-238` → `defaultTimestamps:534-561` 把 `PolicyConfigUpdatedAt` 补成 **正好等于 Created**。
2. 运维执行 `AddPeerClusters`。`MakeBucketHook:821-823` 把 `createdAt` 传给对端，`PeerBucketMakeWithVersioningHandler:950` 的 `SetCreatedAt` 使 B 的 `Created` 与 A **完全相等**。
3. `syncToAllPeers:2217-2228` 发出 Policy@Created（非零，因此不走 legacy-zero 例外）。
4. 按 v2 §3"非零事件**不晚于**目标桶 Created 时成功/no-op，记录 before-created" → B 直接 no-op，policy 落不下去。
5. 按 v2 §2/提交 2.1，A 的候选是 baseline → 过滤 → "无候选，显式 found=false" → 永远不 heal，只产出 indeterminate。

主干今天这条路径是成功的（`PeerBucketPolicyHandler:1718-1746` 无 before-created 检查，直接应用），bulk 侧 `1646` 也只拒绝 **严格早于** Created。因此这是回归，不是遗留缺陷；Tags/SSE/Quota 同理，Object Lock 因 `955-959` 自举侥幸掩盖。T1–T9 没有"字段时间等于 Created 的历史桶初次同步"用例，实现期不会被发现。

最小修正（两句契约 + 一行验收）：①把 apply 侧边界改成**严格早于** Created 才 before-created；`fieldTime == Created` 保留 baseline 的"弱"语义。②baseline 只是排序上低于任何非 baseline 候选，不是"不可用"：目标该字段自身也是 baseline/缺失时，baseline-live 仍可确立初值，仅在存在非 baseline 候选时被压制；全 baseline 且内容不同时按既定键排序取胜者，而不是报 indeterminate。③T7/T8 增加一行"全部字段时间等于 Created 的历史桶，初次同步与一轮 heal 后各站点一致"。

## P2（仍需改动）

- **Policy 比较键的构造方式。** 递归排序自造规范 JSON 需要覆盖 `Condition` 的 `map[string]map[string]ValueSet`、`NotAction/NotResource/NotPrincipal`，一旦两站点对同一策略算出不同键，同时间平局会各自选出不同胜者 → 每轮互推、不收敛。建议判等直接复用现成的 `BucketPolicy.Equals`（`site-replication.go:3912` 已在用），规范串只用于同时间平局排序，并复用 `ActionSet.String()`（silo-pkg `policy/actionset.go:151-158`，已排序）。同时把"键必须是**已解析策略**的纯函数、跨站点必须一致"写成不变量，T2 补一条"两站点各自字节 → 相同键"断言。
- **开关语义与 Policy 的实际不对称需写进文档。** `site-replication.go:4069-4070` 无条件导出 `PolicyUpdatedAt`，因此提交 1/2 落地后，**开关 off 时 Policy 删除照样会经 heal 传播**，只有 Tags/SSE/Quota 被门控。v2 表格里"初次同步的真实删除：off 保留已有行为"容易被读成"off 完全不传播删除"，运维会据此做错误的升级判断。
- **off 模式下的稳定态 RPC 要写进验收预期。** 持有 Tags/SSE/Quota 墓碑的站点在 off 下导出不带时间 → 每轮仍被选为待修复目标 → 每 30s（`4651`）一次必然被拒的 RPC。提交 2 的"对已归一且相同的目标不写入、不发 RPC"不覆盖该情形。请明确这是 off 期间的预期代价，并禁止为它加每轮日志（会与 §3 的三类有界日志冲突）。
- **空策略 PUT 的对外行为补一条断言。** v2 让本地 `PutBucketPolicy` 对 `{"Version":"2012-10-17","Statement":[]}`（`bucket-policy-handlers.go:98-101` 只拒空 Version，该文档合法）改走删除。T1 请断言"PUT 返回成功、GET 返回 NotFound、出站专用事件与落盘一致"，避免实现时误改成 4xx。

## 作者补查核验

**S1 成立。** `internal/logger/logonce.go:109` 仅在 `prev.Err.Error() == nerr.Error()` 时抑制；正文含变化时间会每轮重新打印（且分支不更新 map，等于完全失效）。固定正文 + ReqInfo 的做法正确。

**S2 成立。** `policy/actionset.go:144-168` 的 `MarshalJSON → ToSlice` 直接遍历 map 不排序，`json.Marshal(BucketPolicy)` 确实不稳定，v1 的字节比较键不可用；v2 另建比较键的结论正确（构造方式见上 P2）。

---

**结论：NO_GO。实施前阻断数：1（P1-1）。** 首轮 R1/R2/R3 三项均已实质关闭，R4–R10、O1–O6 的处置未引入新的明确阻断；余下 4 条 P2 建议与 P1-1 一并修订即可进入实现，不需要再做全量复审。
