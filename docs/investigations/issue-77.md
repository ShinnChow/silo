# #77：桶配置复制修复与验收归档

归档日期：2026-09-12。对象为 [SILO #77](https://github.com/pgsty/silo/issues/77)。问题真实；来源时间、删除状态和 heal 选源共同决定是否收敛，不能只补一个删除分支。最终实际 Claude Code Opus 5 Max 实现审查结论为 **GO_WITH_NONBLOCKING_NOTES，阻断 0**，完整 cmd 和最终 lint 随后通过。

本文固定研究与验收时的事实。合并状态以 Issue 关联 PR 为准；主干包含代码不等于镜像、软件包或生产部署已经更新。研究叙述见伴生站的[中文设计记录](https://github.com/pgsty/silo.pgsty.com/blob/main/content/blog/design/bucket-metadata-convergence.zh.md)和[英文设计记录](https://github.com/pgsty/silo.pgsty.com/blob/main/content/blog/design/bucket-metadata-convergence.md)，操作契约见 [Server site-replication README](../site-replication/README.md)。

## 实现为什么最小、必要且足够

源端 10:00 PUT、10:01 DELETE，目标到 10:05 才收到 PUT。旧实现把字段时间写成 10:05，随后把源时间为 10:01 的删除当作旧事件跳过，返回成功但保留配置。删除后没有导出时间的字段，在漏发后还会被对端旧值恢复。另有锁外判旧、批量入口绕过排序、默认值抢占来源、Tags heal 丢失时间和同内容不更新时间等独立入口。

| 必要改动 | 少了它会发生什么 | 复用的边界 |
| --- | --- | --- |
| 在现有整桶锁内读取、比较并保存来源状态 | 锁外判旧仍可覆盖并发的新状态；到达时间继续污染排序 | 既有 `.metadata.bin`、`metadata.lock` 和物理桶存在检查 |
| Policy、Tags、SSE、Quota、Versioning、Object Lock 共用确定性比较 | 等时冲突仍依赖到达顺序；heal 与接收端可能选出不同结果 | 既有载荷、字段时间及 Created，不增加 wire 或持久化字段 |
| 专用事件、bulk、导入、本地写和 heal 都使用提交后的状态 | 只改一个 handler 会留下旁路；归一化后的 Versioning/Quota 与出站事件可能不同 | 既有解析、保存与复制钩子，bulk 仍是一次原子保存 |
| 删除导出开关默认关闭 | 直接对旧版节点导出新增删除状态有已复现的 Quota 缓存风险 | 全部参与节点升级后统一启用，不引入能力协商框架 |
| 物理 Created 恢复与初次同步传播 | 历史无创建时间的真实桶会失去六类配置写入能力 | 现有物理探测；目录 mtime 只是近似值，不推断真实桶世代 |
| Policy 状态键与 heal 一致；异常按原因去重 | 永久假 mismatch 无法修复；正常空基线噪声或无来源异常静默 | 既有每站点统计与 logger，不增加后台协调系统 |

真实状态优先于创建基线；真实状态按来源时间、等时删除优先、规范内容键排序。历史 `live@Created` 可以初始化空目标，但空基线不能删除真实配置。本地纠正时间在锁内取 `max(now, Created+1ns, fieldTime+1ns)`；带时间 peer 事件保留来源时间。Versioning/Object Lock 的 nil 保持 no-op，Quota `null`/`{}` 保持零配额配置，空 Policy 归一为删除。

Policy 编码器保留在 GET/export/peer 是必要的：既有解析器可以保存的负集合策略必须能读回和复制。PUT/import 继续使用同一编码器，避免额外表示分支。没有为了本轮重写 CORS、Lifecycle、对象复制、IAM 或 #91 已完成的计数，也没有修改 SDK、模块依赖或升级协议。

充分性限定在同一桶世代、有效且可排序的来源状态、全部参与节点升级并开启删除导出的范围内。历史时间污染、零时间兼容事件和桶身份冲突不属于自动恢复承诺。

## 计划与实际对抗审查

- [实施前核验与失败复现](issue-77-current.md)、[最终 v4 计划](issue-77-plan.md)。这些文件保留历史状态，不能当成当前待办。
- [四轮计划审查与意见处置](issue-77/review/final-review.md)：前两轮 NO_GO，后两轮零阻断；保留各版计划、完整最终意见、调用元数据和必要探针。
- 三轮实现审查均使用实际 Claude Code `claude-opus-5 --effort max`。模型身份取自实际 assistant 消息，effort 取自显式调用参数。源码在独立快照中审查，审查者读代码和执行结果，没有代为运行这些验收。

| 实现审查 | 固定源码 | 结论与处置 |
| --- | --- | --- |
| [首轮完整意见](issue-77/implementation-review/opus5-max-review.md) / [调用记录](issue-77/implementation-review/session.json) | `4089113e3` | GO_WITH_NONBLOCKING_NOTES，条件性阻断 F1；物理 Created、Policy 假 mismatch、诊断和证据缺口随后修复 |
| [第二轮意见](issue-77/implementation-review/round-2/opus5-max-review.md) / [调用记录](issue-77/implementation-review/round-2/session.json) | `62cf066ff` | 条件性和无条件阻断均为 0；修正首轮对 Policy 编码器的过宽质疑，进一步补齐无来源诊断和初次同步回归 |
| [最终定向意见](issue-77/implementation-review/round-3/opus5-max-review.md) / [调用记录](issue-77/implementation-review/round-3/session.json) | `fcbb93e89` | 阻断 0，确认后续 `461e9a721` 的测试改写等价；读取时尚在运行的 cmd/lint 后续均退出 0 |

最初实现审查 SHA `4089113e3` 补签 DCO 后对应 `1ee64a8d8`，两者树完全相同。生产代码最终固定在 `fcbb93e8957275bfa7ad4e6154e93f4d7b0a7025`；验收源码 HEAD 为 `461e9a721047c63e1a95f54ad4b533a6b89def30`，只在两个测试文件有格式和等价条件改写，见 [tree 对照](issue-77/implementation-review/round-3/final-tree-equivalence.json)与 [patch](issue-77/implementation-review/round-3/test-style.diff)。本次归档不改变生产代码或正式回归测试。

审查原文保留当时的判断，不将后续作者修正倒写成评审者已经观察到的结果。首轮误读 README 中既有的空 Policy / 零 Quota 说明，第二轮修正编码器必要性的判断；最终处置以本文和伴生站的逐项表为准。

## 验收证据

| 检查 | 执行对象与结果 |
| --- | --- |
| [完整 cmd](issue-77/implementation-review/round-3/final-cmd.log) / [命令及退出码](issue-77/implementation-review/round-3/final-cmd.json) | `fcbb93e89`，CGO=0，全部通过，包测试 492.776 秒 |
| [最终目标 race](issue-77/implementation-review/round-3/final-target-race.log) / [命令](issue-77/implementation-review/round-3/final-target-race.json) | `fcbb93e89`，真实创建时间、诊断、初次同步、接管、Policy 状态和全部 CORS 命名用例通过 |
| [测试改写后的 race](issue-77/implementation-review/round-3/final-style-target-race.log) | `461e9a721`，受影响用例通过 |
| [build](issue-77/implementation-review/round-3/final-build.json)、[vet](issue-77/implementation-review/round-3/final-vet.json)、[lint](issue-77/implementation-review/round-3/final-lint.json) | 最终生产树 build/vet 通过，`461e9a721` lint 零问题；可选 typos 未安装，按 Makefile 跳过 |
| internal、S3 Select race、生成文件、兼容检查、六平台编译 | `62cf066ff` 的 `ci-*-after.log` 作为较早阶段的补充证据，不冒充最终树在全部平台运行通过；归档中的空生成日志本身不证明退出状态 |
| [两个真实站点进程](issue-77/implementation-review/round-3/final-runtime.log) / [执行元数据](issue-77/implementation-review/round-3/final-runtime.json) | 干净 `fcbb93e89` 构建：六类历史配置、真实漏发恢复、乱序、四类删除跨重启、两次各 65 秒零 metadata RPC、异常去重、gate=off 与固定旧版的 PUT/DELETE 冒烟均通过 |
| [伴生站构建](issue-77/implementation-review/round-3/docs-check.log) | 提交为 `9fa6248` 的文章内容通过 Hugo 严格构建和站内链接检查，EN 1207 / ZH 1219 页；后续合并状态文案单独检查 |

[归档清单](issue-77/archive-manifest.json)保存每份材料的原始和归档后 SHA-256，以及全部改动源码的 SHA-256。[二进制身份](issue-77/implementation-review/round-3/binary-identity.json)保留实际版本、Go 构建身份和摘要：最终运行二进制来自干净 `fcbb93e89`，SHA-256 为 `4825a801ce0ac48d636d9428ce4cd5c17a20b6cfec0b569de70a124c9c005049`；旧版来自干净基线 `5c5765816`。第三轮审查的 cmd/lint 条件由上述已完成的 JSON 关闭。

### 反向复现与排除的证据

- [confirmed-before.log](issue-77/implementation-review/round-2/confirmed-before.log)：正式测试叠加 `1ee64a8d8` 的 `erasure-server-pool.go` / `site-replication.go`，真实 ObjectLayer 创建时间恢复及旧 Policy 顺序失败；[修复后](issue-77/implementation-review/round-2/confirmed-after.log)和 [race](issue-77/implementation-review/round-2/confirmed-after-race.log)通过。
- [confirmed-diagnostics-before.log](issue-77/implementation-review/round-2/confirmed-diagnostics-before.log)：叠加旧诊断路径，普通空基线误报。第一处断言已经终止测试，不能声称它同时证明后面的 Warning 级别和去重断言在旧码失败。
- [no-source-before.log](issue-77/implementation-review/round-2/no-source-before.log)：直接运行 `62cf066ff`，三种已有无效状态在没有有效来源时缺少诊断；这一次不是旧码 overlay。
- [initial-sync-before.log](issue-77/implementation-review/round-2/initial-sync-before.log)：保留物理恢复修复，仅叠加 `1ee64a8d8` 的 `site-replication.go`，首次同步出站仍传零 Created。该单测的 peer 只确认 RPC，不证明真实对端或本地 peer 分支已经落盘。

`findings-before.log` / `findings-after-1.log` 是早期夹具失败，不能证明产品缺陷；过渡阶段的旧 helper 遗留引用和测试格式失败也不能当成通过记录。这些草稿不进入本归档的验收证据。另一次 Codex 调用因使用限额失败，不计入完成审查次数。

### 重跑真实双站点验收

[独立 Go 驱动](issue-77/runtime/main.go)与原执行内容相同，仅添加 `ignore` 构建标记及说明，避免进入普通包测试。它只启动回环地址上的一次性实验实例，每站四个数据目录，使用文件内声明的专用实验凭据。给它一个全新目录和自行从固定提交构建的两个二进制：

```sh
go run docs/investigations/issue-77/runtime/main.go \
  /absolute/new-lab-directory /absolute/silo-fixed /absolute/silo-before
```

固定构建分别为 `fcbb93e89` 和 `5c5765816`，可在独立干净 worktree 使用 `CGO_ENABLED=0 go build -trimpath`；记录 `--version`、`go version -m` 和 SHA-256。省略旧版二进制参数会跳过混合版本冒烟，不能将其报告为执行过。最终删除收敛快照见 [converged.json](issue-77/runtime/converged.json)，数组顺序由驱动的 `states` 函数定义。

原执行环境为 `go1.27.1 darwin/arm64`。因宿主盘可用空间比例触发存储保留阈值，完整 cmd 和进程实验使用独立 16 GiB APFS 测试卷，没有降低生产阈值。临时卷、审查 worktree 和实例进程均已清理。上述是本地可复核的执行证据，不替代 GitHub Actions、Linux 多节点集群、发布制品或生产验证。

## 剩余边界

1. `MINIO_SITE_REPLICATION_METADATA_TOMBSTONES=off` 是默认值。全部站点全部节点升级、配置一致并排空旧请求后，统一开启并重启，才具有新增 Tags/SSE/Quota 删除的漏发自愈能力。普通删除事件与原有 Policy 删除导出仍保留。
2. 到达时间污染、零时间旧事件和桶世代冲突无法凭现有数据重建。物理 Created 只是目录 mtime 的近似值；较早事件仍跳过，由操作者核对权威状态后重新提交纠正。
3. 日志按每桶/字段/原因去重，仍可能随桶和有值字段数量增长。本轮不增加全站日志调度。
4. 最终审查保留三项非阻断改进：解码失败诊断中的时间可能为零；初次同步单测没有证明本地 peer 分支落盘；未来并发后台日志可能需要更强的测试隔离。生产路径已复核，本轮不为这些建议新增 helper 或 hook。

归档保留最终报告与调用身份，不发布原始模型推理流、二进制、实验数据卷或无效夹具日志。工作站路径、历史文档链接与非必要 JSON 字段经过整理；哈希清单区分原始产物与归档副本，不能将整理后的报告哈希冒充原文件哈希。
