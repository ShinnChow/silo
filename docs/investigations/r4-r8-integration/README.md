# R4–R8 集成核验

## 结论与范围

2026-09-16，R5、R6、R8 的修复在已包含 R4、R7 的 main 基线上完成集成。
本地完整 `cmd`、`internal` 测试、相关 race 检查、仓库 verifiers、构建和
HTTP 超时进程探针均通过。环境中的真实 `claude-opus-5`（effort `max`）独立
阅读合并差异与调用链，结论为 **GO_WITH_NONBLOCKING_NOTES，零阻断项**。

本记录对应 main 合并前的代码核验。最终 PR 的 Linux CI、DCO 和合并结果以该
PR 的实际提交及检查为准；这里的本地结果不代表发布、部署或多站点生产验收。

## 提交对应关系

基线：`9f3037e941a49ab4cd8a0eed7c0f01083fbe4bbe`。

| 问题 | 原修复提交 | 集成提交 | 行为 |
| --- | --- | --- | --- |
| R4 | PR [#193](https://github.com/pgsty/silo/pull/193)，已在基线 | `af2b1794d38d9e70e1d2c3ee692426e4b6cab4bd`（merge） | SSE-KMS 复制保留标签修订时间 |
| R7 | PR [#194](https://github.com/pgsty/silo/pull/194)，已在基线 | `9f3037e941a49ab4cd8a0eed7c0f01083fbe4bbe`（merge） | 复制元数据恢复不再重新写入传输用 aws-chunked |
| R5 | `115fe8b12329d147adbaf817faa1737392ecbf9b` | `680eac66e40b0980bc20e70d7ad34185096e63f5` | 删除标签推进修订，接收端抵御乱序事件，重试及 ACK 保留新状态 |
| R6 | `cf381a7151ef25fc95ace5fedcd767fa19410de2` | `0c61128d23f05ce6b37e7ace713c3ffbfb68f4cb` | 旧形态 marker purge 正确分类，MRF 恢复 marker 并保留重试次数 |
| R6 核验记录 | `d38edb2c46182d3a8fa96e040604493d20a4b478` | `aea3882c95d16ec5598a07b40d593e04054137a9` | 保存 v3 共识及验证边界 |
| R8 | `0d48d32d7e038ae1ea5966f3d7e0cb86780a6311` | `055030ea53ca92ee22ce1e601ef4757c247edde8` | 配置绑定到读头绝对超时，正文继续采用滚动空闲超时 |

各原任务先取得 Opus 方案共识，再实施修复。原始方案、实现复核及验证记录保留在
[R5](../r5/verification.md)、[R6](../r6/README.md)、[R8](../r8/README.md)。
R5、R6、R8 原任务又分别只读核验了集成后的交叉影响，未发现新增生产阻断项。

集成使用 `git cherry-pick -x -s`，保留原作者、来源及 DCO。后续
`80684fed59f556d579e268c5a855d936c1347b68` 仅处理两类贡献规范问题：

- 六个新建测试文件统一使用实际贡献者姓名及 AGPL-3.0-or-later 头部；从
  `package` 开始的内容逐字节不变，Linux build tag 保留。
- 按 CONTRIBUTING 的规则更新兼容标识清单。唯一新增条目是 R6 测试拼接既有
  replication ARN 所用的 `arn:minio:replication::`，没有新增协议名称或生产行为。

22 个源码／测试文件的最终哈希见 [manifest.json](manifest.json)。共享文件中的
R5、R6 补丁与原修复具有相同稳定 patch ID；其余源文件直接比较，六个测试仅允许
上述头部差异。[等价检查](evidence/integration-equivalence.json)全部通过。

## Opus 集成复核与处置

实际 CLI 为 2.1.270，显式指定 `claude-opus-5 --effort max`；只允许 Read、Grep、
Glob，未执行测试或修改代码。实际返回模型为 `claude-opus-5`，进程和结果均成功。
复核基于 `055030ea53ca92ee22ce1e601ef4757c247edde8` 的 22 个文件及完整差异；
此后的代码变化仅为上文已证明等价的头部与兼容清单调整。

原文、调用元数据及提示词分别见 [复核结果](evidence/opus-review.md)、
[metadata](evidence/opus-integration.metadata.json)、[prompt](evidence/opus-integration.prompt.md)。
保留原文中的判断，再用直接证据逐项处置，避免把模型意见当作测试结果：

| 非阻断意见 | 核验与决定 |
| --- | --- |
| 非法或空的历史标签时间戳可能使复制失败并重试 | 保留 R5 共识中的失败关闭行为；历史异常数据修复另行处理 |
| 带标签修订的版本在 resync 时可能多一次 metadata COPY | R5 已接受的可靠性成本；正常 COMPLETED 路径保持原有门控 |
| purge 审计状态由 COMPLETE 规范为 COMPLETED，统计开始记录实际目标结果 | R6 的预期行为；后续发布说明应告知审计／指标使用者 |
| 配置的较短 ReadHeaderTimeout 同时缩短 TLS 握手窗口 | Go net/http 的预期语义，已在 R8 共识中说明 |
| 新增多池标签测试单独运行可能缺少全局初始化 | **未成立**：精确单独运行通过；`consistencyPools` 经 `prepareErasurePoolsWithContext` → `initObjectLayer` → `newTestObjectLayer` 调用 `initAllSubsystems`。保留测试原样 |
| 审计 fixture 重复取消可能输出栈信息 | 本地完整及 race 测试通过；不扩大本次生产修复范围 |
| 新测试文件头部应按实际贡献者整理 | 已在 `80684fed` 修正，测试代码及 build tag 不变 |

## 本地直接验证

下表全部针对 `80684fed59f556d579e268c5a855d936c1347b68`，未使用额外的源码或
容量 overlay；测试代码自身的容量 fixture 保留。限制并行度仅为
`GOMAXPROCS=4`、`GOFLAGS=-p=2`，并使用
仓库 CI 的 `MINIO_API_REQUESTS_MAX=10000`。详细命令、时间和日志哈希在
[validation-results.json](evidence/validation-results.json)。

| 检查 | 结果 |
| --- | --- |
| `make verifiers`：lint、生成文件、rebrand guard | 通过，76.9 秒；可选 typos 工具按现有 Makefile 规则跳过 |
| `make build`、`./silo --version` | 通过，产物为 silo |
| `CGO_ENABLED=0 go test -p 2 ./cmd ./internal/... -count=1 -timeout=30m` | 全部通过，340.2 秒，50 个有测试的包 |
| `CGO_ENABLED=1 go test -race`，cmd/deadlineconn/http 中变更测试的函数集合 | 通过，46.3 秒；Linux build tag 用例由最终 Linux CI 覆盖 |
| `TestAPIPoolsTaggingReplicaDeletion` 精确单独执行 | 通过，无需其他测试预先运行 |
| 实际 silo 进程的 CLI／环境变量读头超时探针 | 两种配置均在 100ms 读头限制下拒绝 400ms 才完成的请求头；空闲超时为 2s，随后健康请求成功 |

进程探针使用二进制 SHA-256
`1cc536f1a3c8d8372ff2d5b140b1fd2bc98a299324fea0f73f67288d48104ce4`。
原始输出见 [runtime-probe.json](evidence/runtime-probe.json)。

各子任务较早遇到的磁盘容量不足或筛选测试初始化问题，不作为这次通过的证据。
本地完整测试已重新执行并成功；原失败记录仍保留在各自调查档案。

## 验收边界

- 最终 PR 必须通过实际提交的全部仓库检查，尤其 Linux internal 测试、完整 cmd
  测试、构建／vet、lint／生成文件、交叉编译、S3 Select race、DCO 和漏洞检查。
- 既有无时间戳对象、异常时间戳、标签筛选的目标选择、任意站点时钟偏差等不由本次
  修复追溯重建。共享状态解析器的历史限制按 R6 v3 共识在写入点规避。
- R8 原先未完成的 S3 长传输脚本不计为通过；本次进程探针验证配置生效，不替代
  S3 长传输、独立多进程、多节点或跨区域生产验收。
- 本次不引入依赖变更或上游 MinIO 兼容硬门槛；R9 不属于这五项修复。
