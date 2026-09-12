Write 工具在本会话被禁用，无法落盘评审文件，因此评审报告直接输出如下。

# SILO #77 修复计划 v1 对抗评审

基准：worktree = main `5c576581631561c446f30ae5b566f0aa793adc1c`。本次只做方案评审：未改产品代码、未运行测试、未写 GitHub、未调用其他代理。

**证据分级**：**[源码]** 本次在该 SHA 直接确认（给文件:行）／**[复现]** `issue-77/current-tests.log` 已记录／**[推论]** 由源码事实推导、未执行／**[待验]** 需实现期测试。

---

## 结论：NO_GO

方向正确，但 §3 / §5 / §6 三处**契约文字**按字面实现会产生新的不收敛与潜在错删。计划约 85% 的内容经源码核对成立，不需要推翻结构——**修订 R1、R2、R3 后复审即可进入实现**。

---

## P1 阻断项

### R1 零配额在发送端被改写成删除，本地却保留 live 文档 → off（默认）永不收敛，on 主动删除本地合法文档

**源码位置**
- `cmd/admin-bucket-handlers.go:85` 本地写入用的是**原始请求体** `data`；
- `cmd/admin-bucket-handlers.go:97-99` `if quotaConfig.Size == 0 && quotaConfig.Quota == 0 { bucketMeta.Quota = nil }` —— 发出的事件被改写成**删除**；
- `cmd/site-replication.go:2088-2107` 对端 `quota == nil` 走 `Delete`；
- `madmin-go v3.0.110 quota-commands.go:54-60` `Quota == 0` 时 `IsValid()` 恒真（`{}` 是合法 live 文档），且该版本**只有 `SetBucketQuota`，没有删除端点** —— "清除配额"只能走这条零值 PUT；
- `cmd/site-replication.go:4090-4094` 导出端仅在 `len(QuotaConfigJSON) > 0` 时带出时间，删除时间被隐藏。**[源码]**

**计划条目**：已确定契约"合法的空 quota 对象 `{}`…不能仅因配额为零就改写为 nil"；§3"同时间真实删除大于 live"；提交 3 tombstone 导出；完成条件"四类 DELETE"、T7。

**触发步骤**
1. A 站点执行等价于 `mc quota clear`（零值 SetBucketQuota）。
2. A 落盘 live `{...}`@T；发出的事件是 `Quota=nil`@T。
3. B 按计划落盘 tombstone@T。
4. **off（默认）**：B 的 tombstone 时间不导出 → B 候选被当 baseline 过滤 → 选源永远是 A 的 live@T → 推给 B → B 按"同时间删除优先"拒绝 → **每 30 秒（`site-replication.go:4651`）一次 RPC + 一次拒绝日志，永不收敛**。
5. **on**：B 导出 tombstone@T → 同时间删除胜 → heal 反过来**删除 A 上合法的 live 文档**。

**影响**：这是清除配额的常规路径。主干今天同样不收敛（到达时间更大导致拒绝），所以**不是回归，但计划声称会修而按 v1 修不了**，T1/T7 不可达成。**[源码]+[推论]**

**同类第二处**：`cmd/bucket-policy-handlers.go:87-109` 本地存 live 的"语义为空"策略，`cmd/admin-handlers-site-replication.go:233-237` 在接收端判为删除。Policy 仍能收敛，只因 `site-replication.go:4069-4070` **无条件**导出 `PolicyUpdatedAt`。可见根因是"发送端语义 ≠ 本地落盘语义"，不是导出策略。

**最小修正（二选一，必须成对，不能只改一侧）**
- (a) 解析后 quota 为零值结构时本地也走 `Delete`（parse=false），事件仍为 nil；或
- (b) 取消 `Quota = nil` 改写，事件携带真实文档。

并把"同一次本地操作，落盘状态与发出事件状态必须是同一状态"写成契约不变量；T1 增加三元组 (kind, payload, time) 一致性断言。

---

### R2 `saveMetadata` 内部静默归一化：落盘载荷 ≠ 比较载荷 ≠ 事件载荷；§6"无状态变化不保存"按字面不可实现；§3 未定义字节序方向

**源码位置**
- `cmd/bucket-metadata.go:391-399` `parseAllConfigs` 在 `objectLockConfig != nil` 且 versioning 文档解析失败 / `!Enabled()` / `PrefixesExcluded()` 时改写 `VersioningConfigXML` 为常量，**不改 `VersioningConfigUpdatedAt`**；
- `cmd/bucket-metadata.go:581-603` `Save` 每次保存前都调 `parseAllConfigs` → 归一化发生在**每一次** `saveMetadata`；
- `cmd/bucket-metadata-sys.go:232-243` `saveMetadata(…, meta BucketMetadata)` **按值接收**，归一化只作用于被落盘/入缓存的副本，调用方手里的结构仍未归一化；
- `cmd/admin-bucket-handlers.go:1094-1116` 导入把未归一化的 `merged` 赋回 `*meta`，随后 `hook.Versioning = enc(meta.VersioningConfigXML)` —— **发出的载荷与落盘载荷不同，时间戳却相同**；
- `cmd/admin-bucket-handlers.go:823-835` 导入只拒绝 `Suspended`，**不拒绝 `PrefixesExcluded()`**（`internal/bucket/versioning/versioning.go:153-155`：`ExcludedPrefixes` 或 `ExcludeFolders` 均算）；
- `cmd/bucket-versioning-handler.go:68-84` S3 PUT 只在**本地** LockEnabled 时拒绝 prefix-excluded；
- 现有回归 `cmd/site-replication-object-lock_test.go:149-167` 已把该机制固化为断言。**[源码][复现-通过]**

**计划条目**：§3"live/live 按稳定载荷字节序比较""apply 和 heal 使用同一比较函数"；§6"无状态变化时不保存、不通知"；提交 1 完成条件"导入的持久化时间与实际发出的事件一致"。

**为什么阻断**
1. §6 只能在 `saveMetadata` 之前比较，而真正落盘的是归一化之后的结构——**规则按字面无法实现**，"无变化"的判定可能与磁盘不符。
2. 计划把到达时间换成源时间后，**同一时间戳 + 不同载荷成为正常可达状态**（Object Lock 在站点间短暂不对称时必然出现）；旧代码靠"到达时间总在前进"意外掩盖了它。
3. §3 **没写明大者胜还是小者胜**。以 `…<Status>Enabled</Status><ExcludedPrefixes>` 对 `…<Status>Enabled</Status></VersioningConfiguration>` 为例，分歧字节是 `E`(0x45) 与 `/`(0x2F)：方向选错，heal 每轮把未归一化文档推给已归一化站点，对方再归一化 → 重复写 + 全节点 `LoadBucketMetadata` 广播；方向选对则一轮收敛。**让正确性取决于未写明的字节序方向不可接受。**
4. 导入路径违反提交 1 完成条件的实质：时间一致、状态不一致。

**诚实边界**：我**没有**证明这会无限发散。Object Lock 不可删除，`healOLockConfigMetadata`（`5379-5447`）通常 1 轮内补齐，之后两侧归一化结果相同并收敛。实际损害是"窗口期重复写/广播 + 实现定义的裁决"。**[源码]+[推论]，永久性未证实。**

**最小修正**：比较器对 Versioning 先做与 `parseAllConfigs` 相同的归一化再比较；`saveMetadata` 回写/返回已持久化结构，导入与所有 hook 用**落盘后的字节**；§6 改为"以归一化后的落盘表示判定状态变化"；§3 写明字节序方向并注明它只用于真并发冲突，不得用来消化序列化差异。

---

### R3 §5 的"bulk 中显式提供且为空 = 删除"对 Policy/Quota 在线路上不可表达；按字面实现存在"缺省即删除"的错删路径

**源码位置**
- `madmin-go v3.0.110 cluster-commands.go:501-530`：`Policy`/`Quota` 是 `json.RawMessage` + `omitempty` —— **nil 与空切片都会被省略**，解码端永远拿到 nil；而 `Tags/SSEConfig/Versioning/ObjectLockConfig` 是 `*string`，`ptr("")` 可表达"提供且为空"；
- `cmd/site-replication.go:1653-1697` bulk 一律按 `!= nil` 判"是否提供"；
- `cmd/admin-bucket-handlers.go:1103-1131` 导入发出的正是 `Type` 为空的 bulk 事件，且只填本次导入涉及的字段。**[源码]**

**风险**：对 Policy/Quota，这条规则要么是走不到的死代码，要么诱导实现者写 `len(item.Policy) == 0 ⇒ 删除` —— 那样**每个只导入 tags 的 bulk 事件都会删掉对端 bucket policy**，正是 #77 要消灭的错删类。这是规范缺陷，不是当前代码缺陷。**[源码]+[推论]**

**最小修正**：§5 改为——bulk 只有 Tags/SSE 能表达删除（显式空 base64 串）；Policy/Quota 删除只走专用事件；bulk 缺省字段任何情况下不改变该字段。T5 补两条断言。

---

## P2（建议同批修订）

- **R4 before-created 扩大到六类，但选源端不做同一判定。** `site-replication.go:1646-1651`（bulk 已有）、`2035-2040`（CORS 先例）、`bucket-metadata.go:190-197`（`SetCreatedAt` 无条件覆盖）、`site-replication.go:5486-5512`（`healBucket` 不修复两站点 Created 分歧）。§提交 2 只过滤 `==0` 与 `==CreatedAt`，**不过滤"早于自身 Created"**：分区期间两站点各自建过同名桶后，较老站点的真实配置被永久拒绝，heal 每轮仍选中它并挡住其它候选（今天会收敛）。**最小修正**：选源与 apply 用同一可用性判定（`time <= 自身 CreatedAt` 视为 baseline）；对 `目标 CreatedAt > 候选时间` 按目标跳过并记一次 `before-created`；计划中明确这是**终态不收敛、需运维介入**，不计入 T9。
- **R5 比较器缺第三种 kind（baseline）。** `site-replication.go:1851-1898` 的 `corsReplicationState` 已有三值模型。§3 只定义"删除 vs live"，把"创建默认/未知"放在选源侧 → 任何异常发送端发出的 `nil + 创建默认时间` 在 apply 侧会被当 tombstone，同时间即可删掉对端 live。**最小修正**：六类复用 CORS 三值模型（baseline = 时间为零或不晚于 Created），apply 对 baseline 一律 no-op；顺带获得 `latestCORSConfig`（`5315-5329`）的稳定选择语义。
- **R6 两个入口读到的"当前字段时间"来源不同。** `site-replication.go:1641` 用 `readBucketMetadata`（**不调** `defaultTimestamps`），`bucket-metadata.go:230-238` 才补默认值 → 同一记录在两条路径上分别是 `0` 与 `Created`，事件时间恰等于 `Created` 时裁决不同。**最小修正**：比较器统一 `if fieldTime.IsZero() { fieldTime = meta.Created }`。
- **R7 heal 中不可达站点占位项会中途终止本轮修复。** `3136-3142` 把不可达站点填 `SRInfo{}`，`3219` 以空 DeploymentID 进入 `BucketStats`；`5006-5009`、`5070-5073`、`5145-5148` 等处 `getAdminClient("")` 失败即 `return`，**放弃该类型剩余站点**，且是否提前退出取决于 map 顺序。与 T9 承诺冲突。**最小修正**：两处循环都跳过 `info.Sites` 中不存在的 dID 并计入 `indeterminate`。
- **R8 parse=true 置空字段留下已解析缓存。** `bucket-metadata.go:401-413`（versioning、quota）与 `334-338`（notification）在载荷为空时**无 else 分支**（即 [复现] `QUOTA_CACHE`）。§6 的"删除走 parse=false"规避有效且最小，但应写成显式不变量并在 T7 加断言。**不建议**本轮补 else —— 会改变 `GetQuotaConfig` 返回语义（`bucket-metadata-sys.go:572-581` 不做 nil 判断），超出范围。
- **R9 Object Lock 的 legacy `Tags` 载荷回退必须保留。** `site-replication.go:1787-1796` + 回归 `site-replication-object-lock_test.go:132-138`。提交 1 的重构容易丢掉；T3 应显式覆盖"Type=ObjectLockConfig、载荷在 Tags 字段"的排序行为。
- **R10 off 模式下 legacy-zero 事件盖上本地新时间后永久胜出。** `site-replication.go:5011-5015` 至今不带 `UpdatedAt`（[复现] `TAG_WIRE_TIME`）。计划已有"升级后由操作者重新提交"的兜底，建议在文档条目里点名这个具体来源。

---

## 过度设计 / 可缩减

- **O1 开关承担三件事，只有一件有证据。** 证据（quota 缓存复现）只支持"tombstone 导出需等全站点升级"。"on 模式拒绝零源时间事件"没有对应缺陷，且计划已修复树内唯一的零时间发送端；保留它只会在误开时让 heal 每 30 秒报错。**建议把开关收缩为单一含义**：是否导出/发送真实删除状态（含初次同步）；零时间事件两种模式统一按 legacy-zero 兼容应用 + 限频记录。
- **O2 开关实现不需要新框架。** `cmd/common-main.go:902` 已有 `env.Get(name, config.EnableOff) == config.EnableOn` 的两行先例，照抄即可。
- **O3 T8 的"固定旧版与修复版两个 SILO 进程"不必作为提交门槛。** 旧版对线路的可观测差异只有两点（heal 事件不带 `UpdatedAt`；导出不含 nil 载荷时间），进程内构造 `SRInfo`/`SRBucketMeta` 即可完整覆盖；降级为一次性人工冒烟。
- **O4 T4 不需要新建"确定性锁屏障"。** 已有 `lockBucketMetadataAcquireHook`（`bucket-metadata-sys.go:249-255`）与 `bucket-metadata-lock_test.go` 的 `runBucketMetadataRMWConflict` 模式可直接复用。
- **O5 §3 的 deployment ID 规则可删。** "仅在状态完全相同时用于日志来源"无可观测行为；复用 `latestCORSConfig`（`5322` 的 `cmp == 0 && dID > latestID`）即自动获得稳定性。
- **O6 `indeterminate` 缺少明确产生条件。** 若采纳 R4，`before-created` 与 `indeterminate` 会合并，应减为三类，不要为对称保留空类别。

---

## 经核对成立、不应削减

锁外 getter→Update 竞态（[复现] `TestIssue77CurrentPeerCheckBeforeLock` 失败）；bulk 绕过逐字段排序（`1653-1697` 无字段时间比较，[复现] 失败）；选源前过滤创建默认值（[复现] 5/32、2/32 错删 Policy；六处重复写法 `4966-4982`、`5038-5054`、`5104-5120`、`5178-5194`、`5253-5269`、`5393-5409` 抽函数是净减法）；Tag 远端 heal 缺 `UpdatedAt`（`5011-5015`）；同内容不同时间不同步（[复现] `BARRIER_NOT_HEALED`）；复用 `metadata.lock` 与 `saveMetadata` 的物理桶检查（`232-243`）、放弃 `peer-require-existing`；导入 `updatedAt := UTCNow()`（`723`）在 ZIP 开始时取一次且可能早于随后创建的桶 `Created`，per-bucket `commitAt` 修正必要。另：`lockBucketMetadataWithTimeout` 不可重入，重入会挂到 `globalOperationTimeout`（非永久死锁但同样不可接受），"不循环调用公开 handler"正确；实现内部 apply 时须沿用 `updateAndParse` 传递 lock-ctx，否则 `loadBucketMetadataParse` 的迁移分支（`bucket-metadata.go:252`、`282`）会再次取锁。

---

## 实施前必须修订的条目

1. **R1** 零值配额的发送端/本地写一致性 + "落盘状态 == 发出状态"不变量。
2. **R2** §6 比较基准改为归一化后的落盘表示；`saveMetadata` 回写已持久化结构；导入 hook 用落盘字节；§3 写明同时间载荷序方向及适用边界。
3. **R3** §5 重写 bulk 删除语义（仅 Tags/SSE 可表达；缺省永不改变字段）+ 对应断言。

建议一并吸收 R4、R5 措辞修正与 O1、O3、O4 缩减，再做一次短复审即可进入实现。

**实现阶段需要运行（本次未运行）**：把两份审计用例改为确定性回归并先记录未修复失败；R1 零配额两侧三元组一致 + off/on 各一轮 heal 无重复 RPC；R2 Object Lock 桶 + prefix-excluded/ExcludeFolders versioning 走导入与 peer 两条路径，断言"发出载荷 == 落盘载荷"且第二轮 heal 无写入；R3 bulk 缺省/空串两类断言；既有回归 `TestPeerBucketObjectLockMetadata*`、`TestPeerBucketAdoption*`、`TestQueuedMetadataUpdateAfterDelete`、`TestSiteReplicationStatusAccountsPerSiteAndSurvivesMalformedConfig`、CORS 站点复制用例，加目标 race 与完整 Go CI。
