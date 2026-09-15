# R6：delete-marker purge 与 MRF 修复

## 当前交付状态

本地实现已完成，v3 已与真实 Opus 5.0 达成共识，原研究基线上的定向回归、race、完整构建、vet、lint 全部通过。正在将隔离分支同步到新主干快照 `af2b1794d38d9e70e1d2c3ee692426e4b6cab4bd` 并复验。

- 研究基线：`9ebe81c1b3611f9cc73e676b5b741c2be62c467a`。
- 分支：`codex/r6-delete-marker-mrf`。
- [PR #184](https://github.com/pgsty/silo/pull/184) 在最终核对时仍为 OPEN，head `6addf9eb916b5a4b837480cf534cd1efa5407d3c`。复用其按 purge 状态识别操作、接纳 marker 405 的方向，补齐实测遗漏；没有直接合并该 PR。
- 新主干只新增 R4 的 SSE-KMS PUT 选项与对应材料，R6 涉及的生产文件、测试文件和依赖未发生交叉修改。
- 本任务没有执行主干合并、远端推送、发布、部署或现网存量改写。

## 修复内容

| 操作 | 远端行为 | 结果与源端写回 |
|---|---|---|
| marker 创建 | 保留 HEAD 已存在/就绪检查及创建语义 | 更新创建状态；405 可以表示已创建 |
| 规范版本 purge | 发送指定 versionId 的永久删除 | 只更新 purge 状态，保留磁盘创建/replica 字段 |
| 旧 marker 形态 purge | 从任务级 purge 状态识别，沿用规范永久删除请求 | 不再被创建 COMPLETED 跳过；失败进入 MRF；已完成 purge 不重发 |

- 离线、DELETE 拒绝、成功与 resync 出口使用操作自己的状态；失败 purge 不写成功 reset 标记。源端 purge 写回显式清空三个“创建更新”字段，避免多目标空状态被旧正则误解析后覆盖磁盘创建记录。
- MRF 接受携带正确 marker、版本、对象、桶和非零时间的 405；其它错误或无效信息不调度删除。
- 删除任务携带重试计数；锁失败、复制失败、工作队列满三个入 MRF 出口都递增。耗尽现有预算后继续保留 scanner 恢复路径。
- purge 的内部 COMPLETE 保持不变；操作审计使用规范 COMPLETED，失败为 FAILED。按目标状态变化更新统计，成功 heal purge 的统计为次数增加、字节数为零。
- 不改 wire 格式、MRF 磁盘格式、共享正则、复制状态合并框架或依赖版本。

## 真实 Opus 共识

使用本机 Claude Code 2.1.270，每轮显式指定 `claude-opus-5 --effort max`。全部记录到的 assistant 模型均为 `claude-opus-5`；实际调用成功，未用模拟评审或限流失败代替同意。

| 方案 | 结论 | 处置 |
|---|---|---|
| v1 | REVISE，1 个阻断 | 接受意见：不能用本次目标子集重写完整创建状态 |
| v2 | GO_WITH_NONBLOCKING_NOTES | 共识后实现；随后用真实存储发现多目标空状态正则反例 |
| v3 | GO_WITH_NONBLOCKING_NOTES，0 阻断 | reviewer 撤回过强的旧证明，确认三字段清空方案；共识后应用并验证 |

最终不可变方案：[plan-v3.md](plan-v3.md)，SHA256 `dc9a67fc91b3113fa35218a2f903455807430cc4daf9c78a88d0be6b8fc27058`。
详见 [完整共识及逐项处置](consensus.md)、[研究与 #184 审查](research.md)、[v3 原始评审正文](opus-v3-review.md)。模型只读权限不允许计算哈希；它核对了具体文件内容，本任务在评审前后计算并确认哈希不变。评审不替代执行验证。

## 验证范围

原基线证据：[机器记录](verification/baseline-verification.json)，以及同目录 scope/race/build/vet/lint 日志。

- 28 个顶层定向测试通过，包含 198 个通过条目（含子测试）。
- 单盘、16 盘真实 erasure 存储；真实源端签名 DELETE、minio-go HTTP、源/目标 marker 元数据。
- 失败 → MRF 文件持久化 → 新 ReplicationPool 读取 → 真实 marker+405 lookup → 工作队列 → 生产 replicateDelete → 恢复。
- 覆盖创建/旧新 purge、部分目标重试、两目标一成一败/离线、远端已删除但响应丢失、重试预算耗尽和 scanner 接管；恢复后核对源和目标 marker 最终状态。
- 两个目标空状态的实际存储反例已转绿；所有 purge 写回均通过断言确认创建字段为空，完整创建/replica 元数据和时间戳保留。
- 验证实际审计 webhook 的 FAILED/COMPLETED，以及失败/成功统计变化；race 未报告数据竞争。

**边界：** 目标为受控 HTTP 适配器，调用真实 ObjectLayer；测试显式消费队列并执行生产复制函数，直接驱动 MRF 保存，没有启动后台定时器和完整 worker 循环。这是三端点 fan-out 与磁盘恢复验证，不是三台独立 SILO 进程的站点复制集群、接收端认证或进程崩溃验收。

6 项无关广义 DELETE 测试在种子数据写入时触发宿主机容量阈值，该次扩大测试未通过；R6 存储夹具使用仓库现有容量适配器，数据仍真实落盘。一次测试链接遇到磁盘空间耗尽，清理可确认属于本任务的旧 Go 缓存后复验。详情和中间失败记录：[verification-notes.md](verification-notes.md)。

## 仍然独立的事项

当前 DELETE/scanner/heal/resync 已产生规范 purge；旧任务形态不会序列化跨重启，不能宣称所有失败 purge 永久卡住。本修复主要恢复活跃的 marker MRF 路径，并完善旧形态兼容。

未覆盖或未修改：缺失客户端导致的目标状态遗漏、目标级 resync 的既有 purge 子集替换、复制跟踪已丢失、replica relay、purge 后延迟创建且无 tombstone、源端元数据写失败依赖 scanner、共享解析器的通用健壮性、额外 backoff/指标设计。完整多进程站点验收应另行安排。

原始大日志与临时复现：`/Users/vonng/tmp/silo-r6-20260915-aa3f/`。每轮 metadata 记录模型、命令、基线、方案及原始输出哈希。
