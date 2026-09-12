# Issue #77 最小充分修复计划

版本：v1，2026-09-12。状态：作者自审完成，待 Claude Code Opus 5 Max 对抗评审。

基准：`pgsty/silo` main `5c576581631561c446f30ae5b566f0aa793adc1c`。本计划只授权设计与评审，不表示已实现、合并或发布。复现材料见 [当前核验](../../issue-77-current.md)。

## 目标与范围

让修复版本间的 Policy、Tags、SSE、Quota、Versioning、Object Lock 桶配置，在合法带源时间的 peer 事件被延迟、重复、乱序或漏发后，能按同一规则应用并由 heal 最终收敛；删除状态必须落盘、能传播，不能被较旧配置复活。桶元数据批量导入的复制入口属于同一范围。

本轮不重做 #91 的计数、#76 的 Object Lock wire 字段、#78 的桶接管、#103/#156 的整记录锁和删除保护。CORS、Lifecycle/expiry、notification、对象数据复制、MRF、resync、IAM 均不改变其语义。支持与验收对象是协调的 PGSTY 栈；不引入为了上游 MinIO 兼容而修改依赖的工作。

## 已确定的行为契约

| 类型 | 专用事件 nil 或解码后空内容 | bulk 字段未提供/null | 真实删除状态导出 |
| --- | --- | --- | --- |
| Policy / Tags / SSE / Quota | 删除；保留源时间，删除走现有 parse=false 路径 | 不修改该字段 | Policy 保留现状；另三类受下述单一开关控制 |
| Versioning / Object Lock | no-op，不能通过 heal 删除配置 | 不修改该字段 | 不新增删除语义 |

合法的空 quota 对象 `{}` 仍按现有 quota 解析语义处理，不能仅因配额为零就改写为 nil。Policy 空策略继续使用现有专用 admin handler 的判空行为。所有既有字段编码、公开 getter 的 NotFound 行为、RPC 返回结构及权限要求保持不变。

1. **读取原始字段时间。** 排序使用 `BucketMetadata` 的原始 `*UpdatedAt` 字段，不能用会隐藏删除时间的公开 getter，也不能用整记录 `lastUpdate()` 代替字段时间。
2. **创建边界与未知值。** `UpdatedAt == 0` 或 `UpdatedAt == CreatedAt` 的记录不能作为 heal 的权威候选。后者沿用既有创建默认值约定，包括无法恢复真实修改时间的旧记录；全部为此类状态时不写入并给出有界诊断。peer 事件早于当前桶 CreatedAt 时不应用。新产生的实际配置写入时间必须严格大于 CreatedAt。
3. **带时间事件的确定性次序。** 先比较源时间；相同时间下，真实删除大于 live，live/live 按稳定载荷字节序比较；内容和时间均相同则 no-op。Policy/Quota 使用既有解析后 JSON 编码作为比较表示；XML 使用解码后的实际文档字节。apply 和 heal 使用同一个比较函数。deployment ID 只在完全相同状态下用于选取日志中的来源，不能作为未持久化的冲突元数据。
4. **本地新操作不倒退。** 对上述六类的实际本地写入，锁内分配 `max(UTCNow(), CreatedAt+1ns, 当前字段时间+1ns)`。其他配置类型保持原有 UTCNow 行为。保留现有 Object Lock 强制 Enabled versioning 的规则，不改变 suspend、prefix exclusion、retention 的合法性。
5. **nil 与删除区分。** 只有可删除类型的专用事件，或 bulk 中明确提供且按既有规则解析为空的可删除字段，表示删除。bulk 的 nil 必须继续表示未提供。Versioning/Object Lock 的 nil/空输入在任何路径均不能清空配置。
6. **写入原子性。** 原始读取、比较、修改及保存全部在现有桶 `metadata.lock` 内。更新用现有 parse=true 路径，删除用 parse=false 路径；bulk 保留当前原始读取、最后统一解析保存的方式，避免 Quota 已解析缓存残留。任何字段校验失败不得保存部分 bulk。无状态变化时不保存、不通知；有变化时成功保存一次，解锁后再通知。

## 实施顺序

### 提交 1：原子排序与本地事件时间

改动主要在 `cmd/bucket-metadata-sys.go`、`cmd/site-replication.go` 和 `cmd/admin-bucket-handlers.go`，只增加六类字段的内部访问/比较/更新时间能力，不引入注册表、接口插件或复制框架。

- 复用现有 `updateAndParse` 的加载、类型设置、保存和通知边界，增加内部 peer 源时间输入及是否应用的结果。公开 `Update/Delete` 的签名保持不变；专用 peer handler 的公开 error-only 签名也不变。
- 六个 peer handler 删除锁外 getter 判旧，使用内部源时间更新/删除路径。对非零源时间执行上述比较，持久化原始源时间而非到达时间；重复和被拒绝的旧事件不落盘。
- 批量 `PeerBucketMetadataUpdateHandler` 对六类已提供字段在其已有整记录锁内执行同一比较。不能循环调用会再次取得桶锁的公开 handler。保留 bulk 中未提供字段、既有 CORS 分支以及一次保存语义。
- 普通本地六类写入用字段内单调时间。`enablePeerBucketVersioning` 在实际修改现有配置时也用该时间函数；仅缺失字段的创建 bootstrap 保留 CreatedAt 默认值，不给默认配置伪造一次新修改。
- **导入生成端一并修复。** 在每桶最终提交锁内，对本次导入涉及的六类字段分配一个共同 `commitAt`：严格大于 CreatedAt 和这些字段现存时间，并且不早于锁内当前时刻。覆盖导入暂存对象中这些字段的时间后，再调用现有 `applyImportedBucketMetadata`。发送 bulk hook 的 UpdatedAt 必须是同一个 commitAt，不能再用整个 ZIP 请求开始时的 updatedAt。未导入字段不改；CORS 继续自己的时间函数和独立事件，Lifecycle/notification 不参与这个时间上界。
- 原样保留 `saveMetadata` 的物理桶存在检查、桶删除锁序、后台通知上下文和加载/迁移路径。不得另加一套 peer-require-existing 或新锁。
- 对真实历史桶 `Created == 0`，仅在该少见路径取得现有物理桶 Created 并补齐，再按现有默认时间规则加载；物理桶不存在就返回现有错误。若物理创建时间也未知，不能用本次到达时间伪造桶世代：不应用并报告 indeterminate。

提交 1 的完成条件：六类 peer PUT 落盘保留源时间；四类 DELETE 后旧 PUT 不复活；相同内容重复事件不保存；getter→Update 竞态和 bulk 旧事件覆盖被挡住；本地后续写入可以胜过已有未来源时间；导入的持久化时间与实际发出的事件一致。

### 提交 2：heal 统一选源与状态同步

改动主要在 `cmd/site-replication.go`。

- 先过滤未知/创建默认状态和 update-only 的空候选，再从剩余候选中按相同比较器选最新；必须显式返回 found=false。取消“先用 map 首项初始化，再判断是否默认”的写法。
- 为 Policy、Tags、SSE、Quota、Versioning、Object Lock 六类 heal 替换对应选源循环。不触碰 CORS、Lifecycle 或对象数据修复路径。
- 本地 heal 通过提交 1 的源时间更新/删除路径；远端 heal 保留对应事件类型并携带选中状态的源时间，补齐 Tag 的 UpdatedAt。
- heal 是否写入由完整状态比较决定，不再由公开 mismatch 计数或仅 payload 相等决定。即使内容相同，较旧时间也必须同步；完全相同的状态不保存、不发送。
- 保留 #91 的 payload/数量统计语义和字段，不为实现时间戳同步更改 madmin API。持续 indeterminate 或因桶创建边界拒绝而留下的状态差异通过提交 3 的日志解释。

提交 2 的完成条件：map 顺序不影响选源；默认状态不能删除真配置；全默认/未知状态不修改磁盘；nil 不清空 Versioning/Object Lock；Quota 删除后磁盘和缓存一致；远端 Tag 时间完整；同内容不同时间、同时间不同内容都能确定性收敛。

### 提交 3：删除传播与有界诊断

使用一个启动配置开关，暂定名称 `MINIO_SITE_REPLICATION_METADATA_CONVERGENCE`，`off` 为默认，`on` 为显式启用。不加入 madmin/silo-pkg 字段、不新增 RPC、不构建通用能力协商。

| 行为 | off：兼容升级阶段 | on：全站点修复后 |
| --- | --- | --- |
| 有源时间的 peer apply、锁内排序、heal | 使用提交 1/2 的修复 | 同左 |
| 专用事件零源时间 | 保留旧版兼容：锁内按本地新操作时间应用，限频记录兼容降级；此类事件不在排序收敛保证内 | 拒绝为无效事件，不写入 |
| bulk 零源时间 | 维持当前拒绝行为 | 同左 |
| Tags/SSE/Quota nil payload 时间导出 | 维持旧版 payload 条件导出 | 导出真实删除时间；零/创建默认仍不是删除事件 |
| Policy 时间导出 | 保持已有行为 | 保持已有行为 |
| 初次同步的删除状态 | 保持已有行为 | Policy/Tags/SSE/Quota 的真实删除也发送专用 nil + source time 事件 |

这里的开关只隔离新增的删除传播和不再接受无时间事件的行为。它不证明远端能力；启用的明确条件是所有参与站点、每站点全部节点都已部署包含提交 1/2/3 的修复版本，旧请求已排空。配置同一站点内必须一致。任何旧节点仍在线时保持 off。

- exporter 只把可区分的真实删除状态作为新增导出对象，不把新桶默认配置改成删除。Policy 已有导出保留，选择端过滤默认值。
- `syncToAllPeers` 也使用同一状态判断发送真实删除；不能只修定期元数据查询而遗漏初次同步。保持现有逐类型 RPC 和编码。
- 复用已有日志去重/限频设施，固定为有限的原因集合：legacy-zero、stale-conflict、before-created、indeterminate。精确重复属于正常 no-op，不记警告；普通顺序已知的旧事件不制造每轮噪音；仅实际错误/持续无法收敛的原因记录桶、配置类型、当前/源时间，不输出完整策略或配置。
- stale 与 before-created 保持 RPC 成功/no-op；缺桶、非法 payload 和 on 模式下的零时间仍返回相应错误。不开新重试循环。
- 更新 Server 的 site-replication 文档，写清 off/on、全节点升级条件、零时间兼容降级和回滚限制。回滚/降级前先在所有修复节点关闭该开关，再滚动降级；旧软件原有缺陷会恢复，不宣称回滚后仍具备收敛保证。
- 旧版本已经记录成到达时间的历史值不能推导回真实源时间。升级完成后，操作者在看到各站点状态后选择权威配置，重新提交需要纠正的配置/删除；不得自动把时间归零或选任意站点强制覆盖。

## 最小验收矩阵

以下均为缺陷或上述修复引入的行为边界；不把发布制品、外部上游服务或不相关产品作为本计划硬门槛。

| 组 | 必须验证 | 验证层 |
| --- | --- | --- |
| T1 | 六类 PUT 原样源时间；四类 DELETE；较旧 PUT/DELETE 不回退；精确重复无写入 | 真实 admin 路由 + 两种 ObjectLayer + 磁盘重载 |
| T2 | 同时间不同 live 状态两种到达顺序结果相同；可删除类型 PUT/DELETE 同时间删除获胜 | 比较器确定性测试 + 代表性真实 handler |
| T3 | Versioning/Object Lock nil/空不删除；保留 lock→Enabled versioning 约束；空 quota 对象语义 | 类型回归 + 现有 #76/#78 测试 |
| T4 | 旧 peer 在锁前通过检查，较新写入先提交后旧 peer 再落锁，不能覆盖；并发不同字段均保留 | 确定性锁屏障 + 两种 ObjectLayer，针对性 race |
| T5 | bulk 有旧字段/新字段/未提供字段混合，分别忽略/应用/保留；非法字段不部分保存；不能借 bulk 复活墓碑 | admin bulk 路由 + 磁盘/缓存 |
| T6 | 本地操作的时间大于收到的未来时间；两个相邻本地提交不倒序；ZIP 导入期间插入写入，最终提交字段与发出的 bulk 时间完全一致 | 本地 API、import 路由、捕获 RPC |
| T7 | 多站点排序排列、默认/零时间、全部无候选、空 update-only；同 payload 新时间、同时间冲突；Tag 出站源时间；Quota nil 后缓存清空 | heal + 本地/远端路径 |
| T8 | 保存/缓存失效/进程重启后墓碑仍有效；缺桶/排队写入/真实零 Created 历史桶；on/off 与新旧发送端混合；初次同步携带真实删除 | ObjectLayer、现有删除测试、固定旧版与修复版两个 SILO 进程 |
| T9 | 短暂断线、事件漏发/重复/乱序后重新连通，六类允许状态收敛；开关 on 后旧零时间事件失败且无修改；日志有界 | 隔离的双站点/多节点实验 |

实现期间先把已有审计用例改成确定性正式回归：不能将反复遍历 map 的概率复现直接提交为生产测试。先记录未修复失败，再验证修复后通过。完成局部修复后运行相关现有回归、目标 race、仓库必需的格式/静态检查与完整 Go CI；同一基准通过后不因形式要求反复全量测试。

## 作者自审结论

- 必要：六类共享的是字段排序与锁内写入；删除、编码、bulk 未提供字段和 Object Lock 约束仍按类型处理。
- 充分：普通本地写、专用 peer、bulk/import、local/remote heal、导出、初次同步、重载均在清单内。没有继续保留绕过排序的实际写入入口。
- 最小：复用一把已有锁、现有持久化和通知、一套小的字段比较逻辑；只加一个针对实际混合版本风险的开关。没有存储 schema、SDK、Console、客户端或新协议依赖。
- 明确限制：off 模式、无时间旧事件、任意未升级节点和已污染的历史时间不承诺自动收敛；on 模式的验收仅针对修复 PGSTY 栈的合法状态与已知桶世代。
- 当前只批准进入外部方案评审。任何对抗评审发现的具体遗漏或多余工作，应先修订本文件并复审，再决定进入实现。
