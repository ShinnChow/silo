# Issue #77 最小充分修复计划

版本：v2，2026-09-12。状态：已处理首轮对抗评审并完成作者复核，待 Claude Code Opus 5 Max 复审。

基准：`pgsty/silo` main `5c576581631561c446f30ae5b566f0aa793adc1c`。本轮交付是方案与评审，不表示已经实现、合并或发布。问题证据见 [当前核验](../../issue-77-current.md)，首轮意见及处置见 [评审处置](decisions-v2.md)。

## 目标与边界

修复 Policy、Tags、SSE、Quota、Versioning、Object Lock 六类桶配置的源时间丢失、锁外判旧、heal 错选源和删除传播不完整。覆盖普通本地写入、专用 peer 事件、bulk/import、local/remote heal、元数据导出与初次同步。

在修复 PGSTY 栈、同一已知桶世代、合法带源时间事件的范围内，使重复、乱序和漏发后的状态能够确定性收敛。开关关闭期间新增删除信息不可见，不承诺完整删除收敛；无时间旧事件、旧版污染时间和桶创建世代冲突需要单独解释，不能自动推断历史真相。

不重做 #91 的计数、#76 的 Object Lock wire 修复、#78 的桶接管、#103/#156 的锁与删除保护。不改变 CORS、Lifecycle/expiry、notification、对象复制、MRF、resync、IAM 的语义；不改存储 schema、SDK、Console、mcli 或 silo-pkg，不新建能力协商、复制框架、锁或重试系统。

## 行为契约

### 1. 明确事件、缺省值与删除

| 类型 | 专用事件 | bulk 字段未提供 | bulk 字段明确提供 |
| --- | --- | --- | --- |
| Policy | nil 为删除；沿用现有解析器 `IsEmpty()` 为删除 | 保留 | 非空 RawMessage 按现有解析器处理；语义空策略归一为删除 |
| Quota | nil 为删除；非 nil 按现有 quota 解析器处理 | 保留 | 非空 RawMessage 按现有解析器处理，零 quota 仍是 live 文档 |
| Tags / SSE | nil 或 base64 解码后空内容为删除 | 保留 | 空字符串为删除，其他内容按原规则解码、校验 |
| Versioning / Object Lock | nil/空内容为 no-op | 保留 | nil/空内容仍为 no-op，不能清空配置 |

“未提供”必须依据真实 wire 类型判定：Policy/Quota 的 `json.RawMessage` 为 nil 或空切片时经 `omitempty` 省略；**显式 JSON `null` 解码后是非 nil 的 `[]byte("null")`**，不能与缺省混淆。按现有解析器，Policy `null` 是语义空策略，Quota `null` 是零值 quota 文档。`*string` 类型的 JSON `null` 则解码为 nil。不得使用统一的 `len(payload)==0 => 删除` 来处理 bulk。

合法 `{}`/零值 quota 保持 live；取消普通 quota PUT 出站时“零配额改写为 nil”的逻辑，保存与发送同一个语义状态。Policy 保留现有专用 peer 的“空策略=删除”解释，本地 PUT、导入和 bulk 统一归一为同样的删除状态；这是需要写入兼容说明的小范围变化：空策略本地立即按删除处理，GET 返回既有 NotFound 行为，不再先保留空文档、等复制后才被清除。

**同一次操作的落盘状态与出站事件，经同一归一规则后，必须具有相同的 `(kind, payload key, source time)`。** JSON 的无意义编码次序不要求字节相同；Object Lock 改写后的有效 Versioning 文档必须来自提交结果。

### 2. 统一状态与排序

内部仅需一个小状态表示：baseline / live / tombstone，以及比较键和字段源时间。复用 CORS 已有设计思路，不改 CORS 本身或扩展为通用框架。

- 使用 `BucketMetadata` 原始字段时间；字段时间为零时在比较视图中补为 Created，与 `defaultTimestamps()` 一致，不借用会隐藏墓碑时间的 getter 或整记录 `lastUpdate()`。
- 在已知 Created 下，零时间或 `fieldTime <= Created` 是 baseline；它不能成为 heal 权威源，也不能作为带时间 peer 更新覆盖实际状态。Versioning/Object Lock 的空候选无论时间如何都不参与选源。
- 专用 peer 的零时间保留兼容例外：锁内按本地新操作分配时间并限频记录 legacy-zero；不受删除传播开关影响，不在源时间排序保证之内。bulk 零时间仍按现状拒绝。
- 非零事件不晚于目标桶 Created 时成功/no-op，记录 before-created。heal 同时过滤源端 baseline；对候选时间不晚于目标 Created 的目标跳过，不能每轮继续发送必然拒绝的 RPC。不同桶世代不能自动合并，需要运维处理，不纳入收敛承诺。
- 有效状态先按源时间排序；同时间 tombstone 胜 live；同时间 live/live 的**稳定比较键字节序较大者胜**。键与时间相同为 no-op。同状态来源按现有稳定遍历/选源方式取即可，deployment ID 不参与持久化冲突裁决。
- Quota 使用现有解析结果的 JSON 编码作为比较键。Policy 在 Server 内生成确定性比较表示：既有解析器校验/去重后，对 JSON 对象键和集合数组递归排序，包括 Statement、Action、Resource、Principal、Condition 值。普通 `json.Marshal(BucketPolicy)` 不稳定，不能直接作键；不为此修改 silo-pkg 或 wire/schema。
- XML 使用有效文档字节，保留大小写和实际内容；Versioning 先应用下述现有 Object Lock 约束。比较器不能依靠字节序方向来补偿保存阶段的隐式改写。

### 3. 先得到有效状态，再比较与提交

原始读取、类型处理、比较、修改及保存均在现有 `metadata.lock` 内。内部入口必须传递现有 lock context，避免 legacy migration 再次取锁。

- 提取并复用 `parseAllConfigs` 已有的 Object Lock→Enabled Versioning 归一规则，使比较视图和真正保存一致；不得扩大 suspend、prefix exclusion、retention 的限制。bulk 先确定实际接受的 Object Lock，再比较该约束下的 Versioning，并在最终提交前应用同一规则。
- 以归一化后的有效 `(kind, key, time)` 判定变化；更新了时间也算变化。完全重复不保存、不通知；一次 bulk 校验失败不保存部分结果；成功至多保存一次，解锁后通知。
- 可删除字段清空必须基于现有 parse=false 的新加载对象，不能在已经解析且仍持有旧 quota 的对象上执行 Update(nil)。bulk 保留原始读取再解析保存的方式。不要顺手修改 Quota getter 或所有 `parseAllConfigs` 空分支。
- 保存函数必须让需要发送 hook 的调用方拿到**本次提交的最终快照**，不能先解锁再读取“最新”状态拼接旧时间。最小做法是让内部 `saveMetadata` 接收元数据指针并回写 Save 的归一化结果，机械更新现有少量调用；对外 `Update/Delete` 签名不变，新增内部提交结果仅供需要该快照的本地 handler/import 使用。不得原地修改已发布到缓存的引用字段。
- 保留现有物理桶存在检查、删除锁序、迁移、后台通知上下文。真实历史桶 Created 为零时仅走少见的物理桶 Created 补齐路径；物理桶缺失返回现有错误；创建时间仍未知则不伪造到达时间，报告 indeterminate。

## 三个实现提交

### 提交 1：原子 apply、发送一致性与本地时间

主要文件：`cmd/bucket-metadata-sys.go`、`cmd/bucket-metadata.go`、`cmd/site-replication.go`、`cmd/admin-bucket-handlers.go`，以及实际需要提交快照的本地配置 handler。

1. 在现有 update/delete 内部路径增加源时间、状态比较和提交结果；公开签名不变。六个 peer handler 移除锁外 getter 判旧，锁内持久化原始源时间。保留 Object Lock 的 legacy Tags 字段载荷回退。
2. bulk 对明确提供的六类字段在已有锁内逐字段比较，再原子保存；未提供字段不动，不能循环调用会重入锁的公开 handler。保留 CORS 独立分支与既有行为。
3. 六类本地实际写入在锁内分配 `max(UTCNow(), Created+1ns, 当前字段时间+1ns)`。其他类型不变。`enablePeerBucketVersioning` 的实际变更也使用它，只有缺失配置的创建 bootstrap 继续 Created 默认值。
4. quota 本地 PUT 保留零值文档并原样表示该语义；Policy 空策略本地与 peer 一致走删除。需要归一化的本地 handler 从本次提交快照生成 hook；其他内容不发生归一变化的路径可保留既有编码，但必须满足三元组一致性。
5. 导入在每桶最终提交锁内，为本次涉及的六类字段生成共同 commitAt，严格大于 Created 和这些字段当前时间且不早于锁内现在。该时间同时用于落盘和 bulk hook，不能沿用 ZIP 开始时间。bulk hook 从最终提交快照构建；若导入的空 Policy 已归一成删除，另外使用现有专用 Policy nil 事件表达它，不能因 `omitempty` 漏发。未导入字段不改，Object Lock 的既有派生 Versioning 修正保留原时间语义；CORS 继续独立时间/事件，其他字段不参与该上界。

完成条件：六类源时间落盘；旧事件不能越过锁覆盖新状态；四类删除不会被旧 PUT 复活；真实 wire、落盘和出站状态一致；重复无写入；bulk 与 import 没有绕过排序或静默遗漏删除。

### 提交 2：heal 选源与应用同规则

主要文件：`cmd/site-replication.go`。

1. 先过滤 baseline、无效来源和 update-only 空配置，再选最大状态；无候选必须显式返回 found=false。消除六处“先 seed map 首项，再过滤默认值”的写法。
2. 选源和目标遍历都跳过 `info.Sites` 中不存在的 deployment ID，包括不可达站点的空 ID 占位项；单一 peer 失败记录后继续其它目标，不因 map 顺序放弃健康站点。不改变状态计数或新建重试机制。
3. 本地 heal 使用提交 1 的源时间 update/delete；远端仍用原有逐类型 RPC，全部携带源时间，补齐 Tag 的 UpdatedAt。
4. 比较完整有效状态，去掉公开 mismatch/payload-only 对写入的门控；同内容较旧时间也同步。对已归一且相同的目标不写入、不发 RPC。Versioning 比较使用与该站点 Object Lock 一致的有效文档；全站点 Lock 状态补齐后不再因旧原始文档产生空转。
5. 保留 #91 的计数与公开字段；创建世代冲突、无可用来源通过有限诊断解释。已知 baseline 且各站点无实质差异时安静 no-op。

完成条件：map 顺序不影响结果；默认值不再删真配置；同内容不同时间、同时间冲突最终一致；Tag 时间完整；Quota 删除后磁盘、缓存、重载一致；不可达占位项不阻断健康目标；第二轮稳定 heal 无写入/广播。

### 提交 3：新增删除传播与有界诊断

只增加一个启动开关，暂定 `MINIO_SITE_REPLICATION_METADATA_TOMBSTONES=off/on`，默认 off；实现沿用现有 env 开关写法，不做能力协商。

| 行为 | off：升级阶段 | on：所有参与节点修复后 |
| --- | --- | --- |
| 带时间 peer apply、锁内排序与 heal | 使用提交 1/2 | 同左 |
| 专用事件零时间 | 兼容应用并记录 legacy-zero | 同左，不新增协议拒绝 |
| Tags/SSE/Quota nil payload 时间导出 | 保留旧版条件导出 | 导出 `time > Created` 的真实删除时间 |
| Policy 时间导出 | 保留已有行为 | 保留已有行为 |
| 初次同步的真实删除 | 保留已有行为 | Policy/Tags/SSE/Quota 都发送专用 nil + source time 事件 |

开关只控制**新增**删除信息的导出/初次发送，普通本地删除事件照常复制。它不检测或证明远端能力。启用条件是所有参与站点的全部节点已经修复，同一站点配置一致，旧请求排空；旧节点仍在线时保持 off。此隔离有实证依据：旧版接到新增 Quota heal 墓碑会留下已解析缓存残留。

日志仅保留三个实际原因：legacy-zero、before-created、indeterminate（未知创建时间、缺失/不可达来源或有实际差异却无可用候选）。精确重复、正常旧事件和成功裁决的同时间冲突不记警告。复用 `LogOnceIf`，以稳定的桶/字段/原因作为 key，**错误正文也必须稳定**；变化的时间与 peer 详情放入日志 ReqInfo，沿用现有每小时清理，不新增限流框架、不输出完整策略。

Server 文档解释启用顺序和回滚：降级前所有修复节点先关开关，然后滚动降级；旧软件缺陷会恢复。点名旧版 Tag heal 无 UpdatedAt 的来源。旧版到达时间污染、legacy-zero 产生的新本地时间以及创建世代分歧无法自动还原；操作者查看状态后在权威站点重新提交需要纠正的配置/删除。历史世代冲突先处理桶身份，不能靠任意站点强刷绕过创建保护。

## 最小验收矩阵

| 组 | 必须覆盖 | 证据方式 |
| --- | --- | --- |
| T1 | 六类 PUT 源时间；四类 DELETE；旧事件不回退；重复无写入；零 quota、空 Policy 的落盘/出站三元组一致 | 真实 admin/S3 路由、ErasureSD/Erasure16、磁盘重载、RPC 捕获 |
| T2 | 同时间两种到达顺序结果相同、删除优先；Policy 多 Action/Resource/Principal/Condition 集合反复编码与排列后键稳定 | 确定性比较器测试与代表性真实 handler |
| T3 | Versioning/Object Lock nil/空 no-op；legacy Tags 载荷回退；Object Lock + prefix exclusion/ExcludeFolders 在普通写、peer、bulk/import 后有效状态一致，第二轮 heal 无额外写入 | 原有 #76/#78 回归加针对性用例 |
| T4 | 锁前旧事件排队、较新写先提交后旧事件不得覆盖；不同字段并发均保留 | 复用已有 `lockBucketMetadataAcquireHook` / RMW 屏障、两种 ObjectLayer、目标 race |
| T5 | bulk 新/旧/缺省字段混合；真实 JSON 编解码的 nil、空 RawMessage、显式 null、空字符串、空策略、零 quota；非法字段不部分保存；只导入 tags 不修改 Policy/Quota | 真实 bulk 路由、缓存与磁盘 |
| T6 | 本地时间胜过已有未来时间；相邻提交不倒序；ZIP 导入期间插入写入，最终落盘和发出事件的状态与时间一致，空 Policy 删除不漏发 | 本地 API、import 路由与 RPC 捕获 |
| T7 | 多站点排序排列、baseline/全无候选、空 update-only；同内容新时间；Tag 源时间；Quota 删除清缓存；空 deployment ID 不阻断其它目标；世代冲突跳过且不反复发 RPC | 确定性 heal 本地/远端用例 |
| T8 | 墓碑经保存/缓存失效/重启仍有效；缺桶、排队写入、零 Created 历史桶；开关 on/off、legacy-zero 与初次同步删除 | ObjectLayer 与进程内旧版 wire/SRInfo 模拟；复用既有删除/迁移回归 |
| T9 | 修复版双站点短暂断线、漏发/重复/乱序后六类合法配置收敛；删除传播启用后第二轮稳定无写入；日志确实有界 | 隔离双站点实验，不把未知桶世代或零时间事件算成通过 |

固定旧版与修复版混合进程仅作一次性升级冒烟，不新增为长期提交门槛。将已有审计用例改成正式确定性回归，不能把遍历 map 的概率复现直接提交。先记录未修复失败，再验证通过；复用原有 Object Lock、adoption、metadata-lock、计数、CORS 回归，并在实现后运行目标 race、仓库必需检查和完整 Go CI。方案审查、局部测试、双站点结果、合并与发布是不同证据。

## 作者复核结论

三个提交分别处理写入正确性、heal 收敛和新增删除信息的升级边界；每项对应已证实缺陷或本次修复直接触及的实际路径。缩减了拒绝零时间的新协议行为、混合版本长期测试门槛和新锁屏障；保留已有持久化、通知与缓存语义。

首轮三个 P1 意见均已有明确处置：R1 修发送/保存一致性；R2 使用有效状态和提交快照；R3 给出经过实际编解码验证的逐字段规则，而非照搬其不准确的 RawMessage 推断。作者另补充了 Policy 非确定性编码与日志去重问题。外部复审未返回前不标记为可进入实现；后续仍须用实现期测试证明，而非把本计划当作修复完成。
