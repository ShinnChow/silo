# Issue #77 最终方案评审记录

2026-09-12，最终计划为 [issue-77-plan.md](../../issue-77-plan.md)。固定代码基准为 main `5c576581631561c446f30ae5b566f0aa793adc1c`；当日重新查询 #77 仍为 OPEN。

**结论：GO_WITH_NONBLOCKING_NOTES，实施前阻断 0。** 作者已把末轮非阻断说明落实到定稿，可进入实现；本轮没有修改产品代码、提交 PR、关闭 Issue 或发布制品。

## 实际执行的外部评审

使用本机 Claude Code 2.1.258，每轮均明确传入 `--model claude-opus-5 --effort max`，在固定 main 的隔离 worktree 中执行，仅提供 Read/Grep/Glob。所有记录到的评审 assistant 消息模型均为 `claude-opus-5`。CLI 的用量信息另含少量辅助模型调用，已如实保留在 metadata；这不是用其它模型代替 Opus 评审。

| 轮次 | 待审快照 | 结果 | 处置 |
| --- | --- | --- | --- |
| 1，全面方案审查 | [v1](plan-v1.md) | [NO_GO，3 个 P1](opus5-max-v1.md) | 修正零 quota 保存/发送不一致、Object Lock 归一后的提交快照、bulk wire 删除规则；缩减开关及验收范围 |
| 2，修订复审 | [v2](plan-v2.md) | [NO_GO，新增 1 个 P1](opus5-max-v2.md) | 首轮 3 项确认关闭；修正历史 live@Created 的初始化回归 |
| 3，历史初值增量 | [v3](plan-v3.md) | [GO_WITH_NONBLOCKING_NOTES，0 阻断](opus5-max-v3.md) | baseline-live 可初始化、不能覆盖真实状态；默认空值不能删除 |
| 4，接管增量 | [v4](plan-v4.md) | [GO_WITH_NONBLOCKING_NOTES，0 阻断](opus5-max-v4.md) | 作者实测假墓碑后新增最小时间归一分支，经 Opus 确认必要性与充分性 |

逐项记录：[首轮处置](decisions-v2.md)、[第二轮处置](decisions-v3.md)、[接管增量](decisions-v4.md)。每轮相邻的 `opus5-max-vN.metadata.json` 保存命令、模型、工具、耗时、源 SHA、待审计划哈希及结果哈希。

## 末轮非阻断说明的落实

- T8 已明确 Created 前移、后移与不变三种情况；历史 baseline-live 第一轮补齐，第二轮无额外写入/广播。
- 保持只调整 #77 六类字段，在实现注释中说明边界。不采纳扩大到其它配置时间的可选建议：那些时间不是新比较器的输入，没有必要同批改变其行为。
- Policy 稳定键与公开计数的判等职责明确；Sid 差异可以触发一次状态同步，但不重做 #91 的既有统计语义。递归规范化和数字精度由 T2 验证。

定稿相对于第四轮冻结快照只补充上述验收/注释说明和评审状态，没有新增实现范围。

## 证据边界

作者实测使用了实际 Go 编解码、当前 silo-pkg 的 Policy 编码，以及 ErasureSD/默认多盘 ObjectLayer 的接管路径。接管诊断 PASS 表示在未修复 main 上成功观察到假墓碑条件。既有问题复现记录见 [当前核验](../../issue-77-current.md)。

外部 reviewer 做的是源代码与方案核查，没有运行新实现测试；现在不存在本轮产品实现。局部回归、race、完整 CI、修复版双站点收敛、一次性升级冒烟均是后续实现阶段的验收，不得把本次评审结论当成这些验证已经通过。
