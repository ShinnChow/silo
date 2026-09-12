# v1 对抗评审意见与 v2 处置

评审者：真实 Claude Code 2.1.258，审查消息模型为 `claude-opus-5`，CLI 明确使用 `--effort max`。首轮对 main `5c576581631561c446f30ae5b566f0aa793adc1c` 的冻结 v1 给出 **NO_GO**，耗时约 24 分钟。完整输出见 [首轮评审](opus5-max-v1.md)，调用证据见 [元数据](opus5-max-v1.metadata.json)。

本文件记录作者核对源码后的处置，不能代替评审者的后续结论。v2 尚未实现。

| ID | 处置 | v2 中的具体变化与理由 |
| --- | --- | --- |
| R1 / P1 | 接受 | 选择其方案 b：保留合法零 quota 文档，取消普通 quota PUT 的出站 nil 改写。补充落盘/事件状态三元组一致性。Policy 保留既有 peer 空策略=删除语义，本地/导入/bulk 也归一成删除；说明空策略本地 GET 行为变化。导入空 Policy 用现有专用 nil 事件，避免 bulk omitempty 漏发。 |
| R2 / P1 | 接受，缩小实现方式 | 比较有效归一状态，复用已有 Object Lock→Versioning 规则；bulk 按最终接受的 Object Lock 处理 Versioning。saveMetadata 用指针让调用方保留 Save 后快照；公共 Update/Delete 签名保持不变，需要发送归一载荷的本地路径使用内部结果。写明同时间 live 键较大者胜，导入 hook 来自提交快照。 |
| R3 / P1 | 接受“不得缺省即删除”的要求；纠正部分依据 | RawMessage 的 nil/空切片确实被 omitempty 省略，但显式 JSON null 解码为非 nil 的字节 `null`；非空 JSON 的空策略也可明确表达。实际 madmin-go/Go 编解码探针已验证。因此不采纳“bulk Policy 不可能表达语义删除、只有 Tags/SSE 可以”的绝对结论。v2 列出真实 wire 判定：未提供保留、Policy 提供但语义空则删除、Quota 提供的零值/null 仍为 live，Tags/SSE 显式空字符串删除；T5 经真实 marshal/unmarshal 验证。 |
| R4 / P2 | 接受 | 选源和 apply 都使用 <= Created 的 baseline 判定；候选不晚于目标 Created 时逐目标跳过并限频诊断。独立创建世代冲突需运维，不纳入 T9 自动收敛。 |
| R5 / P2 | 接受但保留兼容例外 | 三种内部状态明确化，非零 baseline 事件不覆盖真实配置。零时间专用事件依 O1 保留兼容应用，它是明确例外；修复后的正常发送端不得产生该状态。 |
| R6 / P2 | 接受 | 比较视图统一把零字段时间回退到 Created，bulk 原始读取与 load/defaultTimestamps 不再得到不同裁决。 |
| R7 / P2 | 接受 | 六类 heal 的源/目标循环跳过 unknown/空 deployment ID；单个 peer 失败继续健康目标，覆盖可达站点不被占位项阻断。 |
| R8 / P2 | 接受 | 明确四类清空走现有 parse=false 新对象；bulk 原始读取后保存。不改 Quota getter 或无关解析分支；保留磁盘/缓存/重载回归。 |
| R9 / P2 | 接受 | 显式保留 Object Lock 载荷在 legacy Tags 字段时的回退与排序测试。 |
| R10 / P2 | 接受 | 升级文档点名旧 Tag heal 无 UpdatedAt，说明其到达时间/legacy-zero 污染不能自动修复。 |
| O1 / 缩减 | 接受 | 删除 on 模式拒绝零时间的新行为；两种模式统一兼容。开关改名 METADATA_TOMBSTONES，只控制新增墓碑导出/初次同步。 |
| O2 / 缩减 | 接受 | 沿用现有 env.Get 的 on/off 写法，不加配置框架。 |
| O3 / 缩减 | 接受 | 旧/新真实进程只作一次性升级冒烟；长期回归用真实 wire/SRInfo 模拟。保留修复版双站点实验以验证实际最终收敛。 |
| O4 / 缩减 | 接受 | 明确复用现有 lockBucketMetadataAcquireHook / RMW 屏障，不新增产品测试钩子。 |
| O5 / 缩减 | 接受 | 删除无行为意义的 deployment ID 日志裁决要求；它不参与持久化排序。 |
| O6 / 缩减 | 部分接受 | 删除不必要的 stale-conflict 警告，剩三类。保留 indeterminate 并明确真实触发条件：未知 Created、不可用来源、有实质差异却无合法候选；它不等于 before-created。 |

## 作者补充核验

- **S1：日志去重。** `internal/logger/logonce.go:95-123` 同时依赖稳定 key 和错误正文，变化时间放正文会导致每轮记录。v2 使用固定原因错误，动态详情放 ReqInfo，不新增框架。
- **S2：Policy JSON 编码。** 当前 silo-pkg 的 ActionSet/ResourceSet MarshalJSON 枚举 map。同一个已解析策略编码 100 次得到 4 种结果，因此 v1 的普通 JSON 比较键不稳定。v2 增加 Server 内的策略比较键，对已校验策略的集合数组/对象键排序，不改变 wire 或依赖。探针见 [policy-encoding-probe.go.txt](policy-encoding-probe.go.txt)。
- **S3：R3 的实际 wire 边界。** 使用项目选定的 madmin-go v3.0.110 与 silo-pkg 执行真实编解码，确认显式 null 与 omitted 不同，带 Version 的空 Statement 策略通过现有解析且 IsEmpty=true。源码和输出见 [wire-state-probe.go.txt](wire-state-probe.go.txt)、[wire-state-probe.log](wire-state-probe.log)。`cmd/bucket-quota.go:99-110` 证实 Quota null 由现有解析器接受为零值对象；不把它改成删除。

以上均为方案核查和小型诊断。没有修改产品源文件，没有把首轮 NO_GO 改写为通过；后续复审针对冻结 v2 独立给出结论。
