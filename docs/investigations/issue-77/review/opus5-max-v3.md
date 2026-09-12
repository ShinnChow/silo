## P1-1：已关闭

v2 的阻断链条有三个环节，v3 逐个断开：

1. **apply 侧边界**：计划 §2 第 39 行改为「非零事件**严格早于**目标桶 Created 才成功/no-op 并记录 before-created」，并明写「等于目标 Created 的 live 事件可更新仍是 baseline 的目标字段，时间仍保存为源 Created」。v2 反例第 4 步（`==` 被判 before-created）不再成立。
2. **heal 候选**：第 37 行只把「空 baseline」排除出源，baseline-live 是初始化候选；提交 2.1（第 72 行）过滤条件同样是「严格早于**自身** Created」，等于 Created 不被滤掉。v2 反例第 5 步（found=false 永不修复）不再成立。
3. **验收**：T8 第 111 行有「历史字段时间全部等于 Created 的桶经初次同步和一轮 heal 后一致」，T7 第 110 行有 baseline-live 初始化与同级收敛。

## 四条反例核验

1. **历史 live@Created → 空字段**：初次同步走 apply 分支（baseline-live 胜空 baseline，时间存原值 Created）；漏发时 heal 也能选到该候选。写入后两侧同为 baseline-live@Created、键相等，按第 40 行「键、状态级别和源时间相同为 no-op」+ 提交 2.4 稳定。✅
2. **较晚空默认值**：第 40 行排序先比状态级别，「任何真实 live/tombstone 都胜 baseline」，且空 baseline 既不是源也不是删除（37/39 行、decisions 表第 18 行）。时间更晚的空默认值无法压过较早真实修改。✅
3. **不复活真实删除 / 同级收敛**：baseline-live「永远不能覆盖真实 live/tombstone」在 heal 选源与 apply 两侧同一套规则（提交 2 标题即「选源与应用同规则」），因此 off 模式下墓碑隐藏时，被选中的 baseline-live 推到持有墓碑的站点仍被接收端拒绝——是过时 RPC，不是复活。全 baseline-live 不同载荷按同一稳定键较大者胜，两侧独立计算同键（第 41 行纯函数不变量），单轮收敛且不回摆。✅
4. **四条 P2**：均落到契约或验收行（41 行取消双判等、92 行 off≠禁止删除且不加每轮日志、T1 空策略 PUT 成功/GET NotFound）。剩余风险是 Policy 键的实现细节（递归排序与大整数精度），已转为 T2 断言，属实现期验证，非方案阻断。✅

## 非阻断注记

- 比较键含 Sid，而公开统计仍用忽略 Sid 的 `Equals`：仅 Sid 差异时 heal 会写一次、状态接口报已复制；收敛后二者一致，不会持续分歧。
- 建议把 T8 历史桶那行的「一轮 heal」补一句第二轮无写入/广播（与第 78 行完成条件同口径），可选。
- 第 40 行末「同状态来源不参与持久化冲突裁决」措辞含糊，实现前口头澄清即可。

**结论：GO_WITH_NONBLOCKING_NOTES。实施前阻断数：0。** 未发现新反例；已关闭范围（R1/R2/R3、六类 handler、导入、计数、调用图）未重审。
