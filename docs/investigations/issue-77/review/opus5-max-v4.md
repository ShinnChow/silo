## 复核结论（第四轮，仅针对接管增量）

**必要性成立，证据链闭合。** `loadBucketMetadataParse` 内 `defaultTimestamps`（`cmd/bucket-metadata.go:534`）先把字段时间补成 oldCreated，随后 `SetCreatedAt`（`:190-197`）无条件改写 `Created`，两者之间无补偿；probe 日志两个 ObjectLayer 均得 `nil with field>Created=true`，与 v4 新比较器「时间 > Created 即真实状态、nil 载荷即 tombstone」直接相撞，会伪造删除并向外广播。补丁点选在 `cmd/site-replication.go:946-950` 现有 `lockBucketMetadata` 内，位置正确。

**充分性成立。** 用「为 0 或 == oldCreated」判定默认值，与计划 §39「等于 Created 仍属 baseline」同一口径，重映射不改变任何状态级别；真实（> oldCreated）时间与全部载荷不动，故不覆盖真修改/真删除。不触 payload、不新增写入或广播路径，#78 配置保护不受影响；baseline-live 之间按稳定键而非时间决胜，平移时间不改变胜负。

另补一条支持性事实：`enablePeerBucketVersioning:901` 与 handler `:957` 的 `IsZero` 守卫在此路径上已被 `defaultTimestamps` 提前失效，bootstrap 分支本身无法把时间修正到 newCreated——归一化是唯一补救，必要性比 decisions-v4 描述的更强。

**两点非阻断修正：**

1. `SetCreatedAt` 对方向无约束，newCreated 也可能晚于 oldCreated；此时旧默认时间变成「严格早于自身 Created」，按 §37 被当作无效候选过滤，历史 baseline-live 静默丧失初始化能力。计划正文写的是「如果 Created 改变」（双向，正确），但 decisions-v4 §S4 与 T8 只写「前移」。T8 应补后移方向断言。
2. `defaultTimestamps` 覆盖 11 个字段，补丁只归一六类，其余五类接管后遗留 != Created 的伪真实时间。#77 比较器不读它们，不阻断；实现时请在代码注释写明限定理由，或直接对该函数的全集做同样归一（同等成本，不扩大范围）。

未发现范围外扩、未证实假设或与已关闭项的冲突；独立桶世代合并仍在既定边界外，正确。

**GO_WITH_NONBLOCKING_NOTES；实施前阻断数 0。**
