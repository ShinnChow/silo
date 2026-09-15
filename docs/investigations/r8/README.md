# R8：HTTP 请求头绝对超时

## 当前状态

两条缺陷链均已修复。完整 v2 已与真实 Opus 5.0（max）明确达成同版共识，零阻断分歧；配置与连接两条直接验证链均已闭合。额外 cmd race 曾因主机 ENOSPC 在链接阶段中止，补充旧 S3 脚本受存储最小空闲阈值阻挡；父任务已确认二者为环境未完成的补充检查，不阻断本地交付。交付保存在本地分支提交中，未推送、合并、发布或部署。

- 基线：`9ebe81c1b3611f9cc73e676b5b741c2be62c467a`（开工时本地及实时 origin/main 一致）。
- 分支：`codex/r8-http-header-deadline`。
- 方案：[v1](plan-v1.md)、[完整 v2](plan-v2.md)。
- 最终源码绑定：[final-source-manifest.json](final-source-manifest.json)，包含 5 个生产文件、4 个测试文件、计划与生产 diff 的 SHA256；[最终生产补丁](review/final-production.patch)。go.mod/go.sum 与基线一致。
- 共识与意见处置：[consensus.md](consensus.md)。实际模型、显式 effort、计划/原始输出哈希保存在 [review](review/)。

## 两条缺陷链

### 1. 连接层覆盖绝对截止

`DeadlineConn.Read` 在读之前把 socket 截止更新为“现在 + idle + 250ms”，覆盖 Go 设置的绝对读头截止。直接 TCP 对照中，头部限制 100ms、idle=2s、400ms 才完成请求头，标准 Go 拒绝而旧 SILO listener 返回 204。

修复让 HTTP/1 请求头、keep-alive 等待和 TLS 握手读取阶段保留显式绝对上限；读头结束进入 `StateActive` 后恢复原有滚动读取，避免把 `ReadTimeout=IdleTimeout` 变成上传总时长上限。显式零值仍关闭超时，过去时间仍取消读取。写侧与默认 DeadlineConn 调用者保持原行为。现有 ConnState 回调得到保留。

### 2. CLI/环境配置没有传入服务器

`buildServerCtxt` 复制了 IdleTimeout，遗漏 ReadHeaderTimeout。真实 CLI 已读到默认 30s / 参数 100ms / 环境变量 170ms，context 仍为零。因此，即使连接层已经修复，实际 SILO 进程仍回退到 ReadTimeout=idle。

v2 已在相邻位置补一行赋值，未增加新选项或 YAML 字段。相同进程探针显示：v1 参数和环境变量各设置 100ms 时，400ms 慢头均返回 200；v2 两种入口均拒绝该请求，随后的健康检查仍返回 200。

## 已完成的连接层验证

| 验证 | 直接证据 |
|---|---|
| 原始 TCP/HTTP 100ms/400ms 对照，修复前失败、修复后两者均拒绝 | [baseline.log](evidence/baseline.log)、[original-reproducer-fixed.log](evidence/original-reproducer-fixed.log) |
| 头部持续滴入超过多个 250ms 更新周期、首个/后续请求，明文及 TLS | [darwin-race-final.log](evidence/darwin-race-final.log) |
| Content-Length/chunked/100-continue 持续上传、空闲 body、提前关闭及下一请求、读完 body 后长期处理 | [darwin-race-final.log](evidence/darwin-race-final.log) |
| keep-alive、pipelined 缓冲请求、用户 ConnState、hijack/unwrap、TLS 握手读取 | [darwin-race-final.log](evidence/darwin-race-final.log) |
| 强制仅协商 h2，断言 HTTP/2.0；原生流超时、并发健康流及连接复用 | [darwin-race-final.log](evidence/darwin-race-final.log) |
| macOS/arm64 Go1.27.1：完整两个修改包的 race | [darwin-race-final.log](evidence/darwin-race-final.log) |
| Linux/arm64 Docker Go1.27.1：完整两个修改包的 race，包含可选 DriveOPTimeout 拨号路径 | [linux-race-final.log](evidence/linux-race-final.log) |
| 默认 idle=30s，明文/TLS 的持续上传、下载均用时约 33s 并完成 | [default-30s-transfers.log](evidence/default-30s-transfers.log) |
| grid 实际 roundtrip/disconnect，go vet | [grid.log](evidence/grid.log)、[vet-final.log](evidence/vet-final.log) |

Linux 使用已有本地 `golang:1.27.1-bookworm` arm64 镜像 `sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b`。临时容器仅只读挂载本工作区与 Go module cache；没有发布端口，结束即删除。

## 配置与真实进程验证

- [config-baseline.log](evidence/config-baseline.log)：实际 CLI → buildServerCtxt；默认、参数、环境、参数优先级、YAML 合并、零、负值。
- [config-fixed.log](evidence/config-fixed.log)：10 个常驻配置回归全部通过，既有 YAML 配置测试也通过。包含 idle=0/负值时默认 header=30s、fmt-gen 未注册该选项时仍安全返回零。
- [runtime-v1.json](evidence/runtime-v1.json)：v1 真实编译进程在参数/环境两种配置下均错误接受 400ms 慢头；包含二进制 SHA256。
- [runtime-v2.json](evidence/runtime-v2.json)：修复后同一探针，两种入口均拒绝慢头且服务健康。最终测试二进制 SHA256：`6b982de3262c25e326280c739b4275444cf80c3eacae48f0942aa18fbb7cd654`。
- [quality-checks.json](evidence/quality-checks.json)：执行命令、退出码、平台与日志哈希。`go mod tidy -diff`、`go vet`、scoped golangci-lint 与 diff 空白检查均通过，go.mod/go.sum 未改变。
- [runtime_probe.py](evidence/runtime_probe.py)：可复现的进程探针。只启动回环地址上的临时单盘服务器，使用临时测试凭据和数据，结束时终止自己的子进程。

## 补充验收的环境限制

- 额外 `go test -race ./cmd -run TestServerReadHeaderTimeoutConfig` 未进入测试：Darwin 链接器报 `errno=28 (No space left on device)`。这与已经通过的两个网络包 macOS/Linux race、普通 cmd 配置测试不同，不能混为通过。
- 为检验已有 `buildscripts/test-timeout.sh`，制作了临时隔离副本：将全局 pkill 换为只终止自己的 PID、监听地址限于回环、测试末尾清理；使用单独 mcli 配置目录与 BSD nc 的私有 netcat 名称，原三组请求/断言未改。脚本在正常 PUT 阶段未形成对象，整体退出 255，**未通过**。
- 后续立即上传 30 字节的独立诊断明确返回 **HTTP 507 / XMinioStorageFull**，消息为已达到最小空闲空间阈值：[s3-capacity-probe.json](evidence/s3-capacity-probe.json)。这条补充脚本不能在当前宿主容量条件下用于证明 S3 持久化验收，也不是 deadline 回归证据。
- 原脚本与隔离改动、命令、客户端版本、运行时长和退出码见 [isolation patch](evidence/legacy-timeout-isolation.patch)、[metadata](evidence/legacy-timeout.metadata.json)、[log](evidence/legacy-timeout.log)。v2 编译进程的健康端点慢头验证已独立通过。
- 按父任务确认，无需为这两项非阻断补充检查继续大编译或等待；已清理本任务已结束的 v1 旧二进制约 126MiB；保留原哈希、源码方案、日志与 v2 二进制。未清理共享缓存或其他任务数据：[cleanup.json](evidence/cleanup.json)。

## 配置语义与兼容边界

| 配置 | 完整修复后的含义 |
|---|---|
| 默认 idle=30s / header=30s | 有效时限仍为 30s，持续滴入头部现在也受绝对上限约束 |
| 只设 idle=2s | header 独立采用其默认 30s；相对只做 v1 的 2s 回退上限有所放宽，相对原先无限续期则建立了正确上限 |
| header 正值 | 按该值限制 HTTP/1 头部；该值还参与 Go 的最小正 TLS 握手时限，包括 h2 握手 |
| header=0 | 按 Go 规则回退到 ReadTimeout（此服务设为 idle） |
| header<0 | 显式取消 Go 的 header 绝对上限；首个明文请求仍保留原有 socket idle 续期，TLS/keep-alive 因 Go 显式清零而无 header 上限 |
| idle=0/负值，header 未设置 | 独立的默认 header=30s 现在正确生效 |

## 交付说明

- 请求头和 TLS 握手的读取现在遵守绝对上限，即使连接持续有少量字节到达也会到期；这是修复后的预期可见变化。[Go Server 定义](https://pkg.go.dev/net/http#Server)
- HTTP/1 长上传、长下载保留滚动 idle；idle 仍有原有最多约 250ms 的更新时间余量，绝对 header 上限没有这项余量。
- HTTP/2 保留现有原生 per-stream 超时行为；其原有总时长限制不属于本次修复。
- TLS 握手**写**侧仍沿用滚动截止，这个既有边界单列保留，不能将本报告称为完整 TLS 握手资源限制修复。
- 未改磁盘格式、对象元数据、存量状态、依赖或协议。回滚为撤销本次源代码改动并重新构建；本地证据不是生产部署验收。
- 大范围仓库 CI、主干合并、版本发布、镜像和部署是后续独立交付步骤。本任务只做本地修复与针对性验证。

## 已纠正的测试夹具问题

初始 H2 客户端按服务端 ALPN 顺序落回 H1，协议断言正确使测试失败；改用仅 h2 的 TLS 拨号。之后 H2 同时启动同期限读写定时器，谁先到期会改变 body 错误包装；最终夹具在 PUT 内清除写定时器以独立验证读超时，并验证其他流与同一连接存活。无数据竞争报告。

配置夹具最初用了错误的变量名，随后发现“环境变量不存在”与“存在但为空”的 CLI 语义不同；修正夹具后才把非零 context 丢失作为缺陷证据。相关初始失败日志保留，不作为产品回归或通过证据。

新增 H2 测试曾直接引用 x/net/http2，Opus 实现评审因此提出 tidy 门禁阻断项。最终改用标准库 HTTP/2-only Protocols，不引入新直接依赖；v2 Opus 明确认定该阻断项已经解决，实际 tidy 检查也通过。重复 clamp 与 h2 宽裕 keep-alive 的改进在两平台最终 race 中通过。

## 最终交付状态

| 环节 | 状态 |
|---|---|
| 研究与最小兼容方案 | 完成；同时确认连接续期覆盖和配置传递遗漏 |
| 真实 Opus 5.0 max 共识 | 完成；完整 v2 同一 SHA256，零阻断分歧 |
| 实现评审链 | v1 REQUEST_CHANGES 的测试依赖 B1 已修复，v2 实际评审明确认定已解决 |
| 本地实现 | 完成，绑定此提交内源文件哈希 |
| 必要验证 | 配置、真实进程 CLI/env、TCP/TLS、长传输、keep-alive、h2、共享调用方及两平台网络包 race 完成 |
| 补充 cmd race / 旧 S3 脚本 | 环境未完成，分别为链接 ENOSPC 与 HTTP 507 最小空闲阈值 |
| 远端推送 / PR / 合并 / 发布 / 部署 | 未执行 |

工作量估算仍为原计划的 1–3 工程师日级别；本轮实际完成方案、两次方案共识、一次实现复核和上述本地验证。未宣称全仓 CI 或生产验收通过。
