# v2 对抗复审意见与 v3 处置

第二轮由相同 Claude Code / `claude-opus-5 --effort max` 执行，仅有 Read/Grep/Glob 工具，耗时约 11 分钟。结论 **NO_GO，1 个新增阻断**；明确确认首轮 R1/R2/R3 已全部关闭，并承认首轮 R3 关于 RawMessage 的部分依据错误。见 [第二轮原文](opus5-max-v2.md)、[调用证据](opus5-max-v2.metadata.json)。

## P1-1：历史桶字段时间等于 Created

接受阻断。源码核对确认历史配置可合法带 `UpdatedAt == Created`，MakeBucketHook 还会把同一 Created 传给新站点。v2 把等号也拒绝，会阻断初次同步，并因 heal 过滤 baseline 而无法补救。

对评审中的小处事实作校正：当前 `applyLegacyConfigs` 末尾已经调用 `defaultTimestamps()`，因此“迁移后磁盘字段仍为零”不普遍成立；**字段等于 Created 的反例成立，不影响阻断判断**。

v3 的最小修订是保留 baseline 的弱初值语义，不引入新 wire 字段或重盖到达时间：

| 输入/目标 | v3 行为 |
| --- | --- |
| 历史 live@Created → 空 baseline | 接受初始化，保存相同源时间；初次同步和 heal 一致 |
| baseline-live → baseline-live | 稳定比较键较大者胜；全 baseline-live 也能收敛 |
| baseline-live → 真实 live/tombstone | 不覆盖；真实状态优先级先于时间比较 |
| 空/nil@Created → 任意已有配置 | 不能作为删除，不清空 |
| 非零时间严格早于目标 Created | 原有创建保护，成功/no-op 与有界诊断 |
| 全为空 baseline | found=false，安静不写入 |

T7/T8 新增上述用例，特别是历史桶从初次同步到完整一轮 heal 的验收；六类类型约束、删除 parse=false、旧事件不能复活 tombstone 均保留。

## 四条 P2

1. **Policy 稳定键：接受目标，不叠加第二种判等。** v3 明确对已解析策略完整 JSON 树做递归排序，覆盖 NotAction/NotResource 与 Condition，保留数字精度，并由两站点独立解析验证同键。未采纳 Equals 优先再用另一种键的建议：当前 `BucketPolicy.Equals` 对 Statement 次序敏感，而 `BPStatement.Equals` 忽略 Sid，两套判等混用可能使同时间冲突的比较不一致。公开统计现用 Equals 不变。当前 BPStatement 不支持 NotPrincipal，不为此新增语法或改 silo-pkg。
2. **off 并非禁止删除：接受。** 直写 Policy 原有墓碑在 off 仍参与 heal；普通删除事件一直复制；只有新增 Tags/SSE/Quota 墓碑导出与初次发送受开关控制。
3. **off 期间过时 RPC：接受。** 隐藏墓碑使部分旧 RPC 仍被尝试、在接收端被排序拒绝；这是升级阶段明确代价，不要求零 RPC，不加每轮日志。稳定无额外 RPC 的验收要求明确放在完整状态可见之后。
4. **空策略 API 行为：接受。** T1 明确带合法 Version 的空策略 PUT 成功，随后 GET NotFound，落盘与专用删除事件三元组一致；不能误改为 4xx。

三个实现提交没有增加，也没有新增协议、框架、依赖或长期集成门槛。v3 只需对本次 baseline 规则与以上说明做最后针对性复审；已关闭的首轮问题不重开全仓库审查。当前仍是设计文件，没有实现或执行实现期测试。
