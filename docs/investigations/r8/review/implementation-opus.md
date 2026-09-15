I'll start by reading the review artifacts and the plan documents.

已完成只读复核。我独立读取了 Go 1.27.1 的 `net/http` 源码、全部 7 个当前实现文件、既有测试与全部 evidence 日志。

---

# R8 实现评审 — Opus 5.0（只读，实测复核）

## 结论：**REQUEST_CHANGES**

生产代码（`deadlineconn.go` / `listener.go` / `server.go` / `server-main.go`）我**未发现任何行为性缺陷**，与已同意的 v1（SHA256 `7cb6…6366`）逐条吻合。唯一阻塞项在**测试侧引入的依赖，会确定性地打红仓库自身的 CI 质量门**（`make check-gen`），一行 `go.mod` 即可修复，不需要改动任何生产语义。

> 诚实性声明：① 我**没有执行任何测试、`go vet`、`gofmt`、`go mod tidy` 或哈希计算**（本会话仅 Read/Grep/Glob）；所有 SHA256（plan、production diff、implementation-files.json）均沿用你提供的值，**未独立重算**。② 我逐字节比对了 `review/implementation.patch` 与四个生产文件的当前内容，二者一致。③ 下文所有"已核验"均指**我亲自读过的源码**（含 `/opt/homebrew/Cellar/go/1.27.1/libexec/src/net/http/`）；所有"日志显示"均指**我读日志得出的转述，非我运行**。④ 未使用子代理。

---

## 一、对 v1 的实现符合性（逐条，全部达成）

| v1 条款 | 实现位置 | 结论 |
|---|---|---|
| mutex 保护的 explicit 时间 + `readDeadlineStrict`（默认 false） | `deadlineconn.go:37-38,43` | ✅ |
| `SetReadDeadline`/`SetDeadline` 记录 explicit，保留 zero/abort 与立即转发 | `deadlineconn.go:134,152` + `136-139,149-150` | ✅ |
| `SetReadDeadlineStrict(bool)`，同锁、重置 `readSetAt`、带文档 | `deadlineconn.go:79-87` | ✅ |
| 锁内复检 abort/inf；保留节流与 250ms 松弛；strict 下取 `min(idle, explicit)`；绝对上限不加松弛 | `deadlineconn.go:64-66,69-76`（`deadline = c.readExplicit`，未 `Add`） | ✅ |
| 写侧完全不动 | `deadlineconn.go:89-105,161-168` 与基线一致 | ✅ |
| listener 保留具体类型/读写 idle/Unwrap 兼容，返回前开启 strict | `listener.go:73-76` | ✅ |
| Init **组合而非丢弃** 调用方 ConnState；先切模式再调用方 hook | `server.go:127,138-151`（`connState(conn,state)` 在 switch 之后） | ✅ |
| 一层 `*tls.Conn` 用 `NetConn()` 解包；h2 跳过；其他状态忽略 | `server.go:130-137,139-147` | ✅ |
| 不删 ReadTimeout/WriteTimeout、不改 flag；生产超时处加说明注释 | `cmd/server-main.go:904-905`（其余 906-915 未变） | ✅ |

验证矩阵 1–7 项的**内容**也已全部落地（含 N2 pipelined、N5 真 h2 断言、N6 >30s 下载三项补充）。

---

## 二、你点名的 8 项独立核验（结果）

1. **Go 1.27.1 相位序（实读源码）**：`setState(c.rwc,StateNew,runHooks)` 在 `go c.serve()` **之前**、accept 循环内同步执行（`server.go:3580-3581`、hook 同步调用见 `1881-1883`）；首个 header deadline 在 serve loop 之前（`2038-2040`）；`StateActive` 在 `readRequest` **返回之后**（`2054-2059`）；整请求 deadline **无条件**下发（`1103`，`ReadTimeout<=0` 时下发 zero）；`StateIdle`(`2152`) → idle deadline(`2163-2167`) → `Peek(4)`(`2173`) → 第二个 header deadline(`2177-2181`) **全部落在 strict 窗口内**。结论：v1 的相位切分正确。
2. **mutex/atomic 交互**：`readExplicit`/`readDeadlineStrict`/`readSetAt` 三者只在 `mu` 下读写；`abortReads`/`infReads` 原子量在锁内**复检**（`deadlineconn.go:64`），恰好堵住"`Read` 已过外层门 → 并发 `SetReadDeadline(zero)` → 滚动 deadline 覆盖 net/http 背景读的零值"这一 TOCTOU；锁内无阻塞 I/O（`SetReadDeadline` 只是 runtime 定时器调整），因此 accept 循环里的 hook 不会被卡住。未见数据竞争面。
3. **>250ms 更新不得续期 header 上限**：`deadlineconn.go:69-76` 每次到期重算都会再次 clamp 回同一个绝对 `readExplicit`，绝不外推。推演 trickle 用例（header 650ms、100ms 一字节）：t=0/300/600ms 三次落入更新分支，每次都 clamp 到 t0+650ms，650ms 必断。**不变量成立**。
4. **nonzero/zero/past**：nonzero → `min()`；zero → `infReads` 在外层与锁内双重短路（`58`/`64`），背景读、hijack、h2 全部维持"永不超时"；past → `abortReads` 使 `Read` 直接返回 `context.DeadlineExceeded`（实现 `net.Error.Timeout()`）。strict 下**已过期的未来时间保持过期**（clamp 出一个过去时刻）。
5. **H1 body 滚动**：`1103` 在 `2058` 之前就把 socket deadline 改成 `t0+ReadTimeout` 并同步刷新 `readExplicit`，二者之间**不存在任何读**；`StateActive` 关 strict 且清 `readSetAt`，首个 body 读立刻续期 → 长上传不被硬顶。`ReadTimeout<=0` 时退化为 `infReads`，与补丁前一致。
6. **buffered/pipelined 与 keep-alive**：`StateActive` 的触发条件 `c.r.remain != initialReadLimitSize()` 在**成功路径上恒成立**——`readRequest` 在 `1067` 调 `setInfiniteReadLimit()`（`remain=maxInt64`），故纯缓冲的流水线第二个请求同样会关 strict（opus-v1 N2 得到证实）。keep-alive 侧 `2163` 会立刻覆盖掉上一请求遗留的 `readExplicit`，中间窗口无读，无陈旧值风险。
7. **TLS/h2**：握手读上限 = `min(正 RHT,RT,WT)`（`server.go:969-983,1962-1968`），成功后两侧清零（`1988-1992`）→ 再进入新的 header 上限。协商 h2 时 net/http 走 `setState(...,skipHooks)`（`2002`）且在 `ServeConn` 前把两侧 deadline 清零（`http2.go:100-101`）；h2 自身只上报 Active/Idle（`internal/http2/server.go:568-572,796-800`），我们的 h2 分支跳过它们，即便不跳过也被 `infReads` 短路。h2 的 per-stream `ReadTimeout` 是 `time.AfterFunc` 计时器（`1970-1972` → `onReadTimeout` `1836-1841`，返回包装后的 `os.ErrDeadlineExceeded`，满足 `net.Error`），**未新增任何连接级读超时**。
8. **grid/hijack 与默认调用方**：`hijackLocked` 先 `abortPendingRead` 再 `rwc.SetDeadline(zero)`，之后才 `StateHijacked`（`server.go:322-326,336`）；grid 随后 `deadlineconn.Unwrap(conn)` 取回裸 `*net.TCPConn`（`internal/grid/manager.go:193`），strict 根本触达不到 grid。内节点 dialer 走默认 false（`internal/http/dial_linux.go:126-131`），且 `DriveOPTimeout` 在生产仍被注释（`cmd/server-main.go:421-422`），生产内节点连接压根不经过 DeadlineConn。

---

## 三、阻塞项（1 项，不涉及生产语义）

**B1 — 新测试的直接依赖会打红 `make check-gen` / CI `quality` job**
- `internal/http/server_deadline_test.go:35` 直接 `import "golang.org/x/net/http2"`，而 `go.mod:297` 为 `golang.org/x/net v0.59.0 // indirect`；全仓库（Grep 确认）**只有这一个文件**直接导入 `golang.org/x/net/*`。
- `Makefile:49-58` 的 `check-gen` 会执行 `go mod tidy -compat=1.27`，随后 `git diff --name-only -- … go.mod go.sum` 非空即 `exit 1`；`.github/workflows/go.yml:75-76` 把它作为必跑步骤。`go mod tidy` 会因"主模块的测试直接导入"而把该行提升为直接依赖（去掉 `// indirect`）→ **go.mod 产生 diff → CI 红**。
- 注意这**不是构建失败**：`go.sum:765` 已有完整 `h1:` 哈希，所以本地 `go test` 能过（日志也显示过了），git status 里 go.mod/go.sum 也确实未变——问题只在 tidy 门禁。
- 两种修法任选其一：(a) 把 `golang.org/x/net v0.59.0` 移入直接 require 块（不改版本、不引新模块，零风险）；(b) 去掉该依赖，用标准库 h2 客户端（`stdhttp.Transport` + `TLSClientConfig.NextProtos=[]string{"h2"}`）——被测服务端本就是 net/http 内置 http2，(b) 反而更贴切。
- 声明：我**无法执行** `go mod tidy` 验证，此结论由 `go.mod:297` + `Makefile:49-58` + `go.yml:75-76` + Grep 结果推得。

---

## 四、非阻塞项

- **N1（最值得补的测试缺口）** `deadlineconn_strict_test.go:40-100` 中每次 `Read` 之前都有 `SetReadDeadline*`，而它们会把 `readSetAt` 清零（`deadlineconn.go:86,151`），因此**只覆盖了"第一次更新"的 clamp，从未覆盖"跨 250ms 的第二/第三次更新仍 clamp"**——而这正是本缺陷的核心不变量。目前它只由真实 socket 的 trickle 用例（`server_deadline_test.go:145-151`）间接覆盖。建议加一个确定性用例：strict + 设上限 → `Read` → `sleep(300ms)` → `Read` → 断言 `raw.read` 仍等于上限。
- **N2** `TestConcurrentStrictReadDeadline`（`:140-162`）无断言，且 setter 每轮立刻把 deadline 归零，**从未让 `Read` 与"正在生效的 clamp"并发**；作为 race 探针可以，但别把它当作语义回归。
- **N3（flake 风险）** `TestServerHTTP2Deadlines`（`:376-478`）里 `IdleTimeout=400ms` 会被 net/http 映射为 h2 连接级 idle（`net/http/http2.go:57-61`）；两次初始 GET 之后、以及 `<-done` 到最后一次 GET 之间，连接处于 idle，若 runner 抖动 >400ms，连接会被 h2 自身关闭 → `connections.Load()==1` 假失败。同理 `TestServerContinuousUpload` 每块只有 550ms 余量。建议 h2 fixture 单独用更大的 idle。（fixture 清写定时器的修法本身是对的：`onWriteTimeout` 产生的是 `StreamError/INTERNAL_ERROR`（`internal/http2/server.go:1846-1852`），不满足 `net.Error`，正是初版失败的原因。）
- **N4** `listener.go:73` 的局部 `conn` 遮蔽了具名返回值 `conn net.Conn`（且 `err` 也不再使用）；合法但可读性差，`dc := …` 更干净。
- **N5（微小开销）** clamp 命中时 `deadlineconn.go:74` 会把 socket 已持有的同一时刻再写一遍；加上每次相位切换清 `readSetAt`（`server.go:141,145`），每个 H1 请求多出约 2 次 deadline 重算。相对 net/http 自身每请求 3 次 `SetReadDeadline` 可忽略，无需改。
- **N6** `server.go:131` 每次状态转换都调 `tlsConn.ConnectionState()`（持 `handshakeMutex`）。正确且安全（StateNew 发生在 `go c.serve()` 之前），但如 opus-v1 N3 所述该分支严格冗余（`http2.go:100-101` 已清零，`infReads` 必然短路）。保留可作纵深防御，建议注释点明"仅为防御，非必要条件"。
- **N7（发布说明，已在 consensus N7 登记）** 两处用户可见收紧：TLS 握手读被 `min(RHT,RT,WT)`（生产 30s）硬顶；请求头被 ReadHeaderTimeout 硬顶（即便字节持续到达）。
- **N8（已知边界，非本轮）** 生产 `NextProtos{"http/1.1","h2"}`（`cmd/utils.go:970`）下，只宣告 h2 的客户端仍受 h2 **绝对** per-stream `ReadTimeout`（30s）约束；plan 第 24 行/consensus N3 已声明不修，交付说明请重述。
- **N9（证据对齐，已更正）** 我核对了日志里的 `t.Logf` 归属行（注意 `runContinuousUpload/Download` 调了 `t.Helper()`，报的是**调用点**）：`implementation-focused.log:85` 的 `:259`、`default-30s-transfers.log:24` 的 `:274` 与当前文件 **完全一致**；下载区整体偏移 +36/+37 行，与"仅 H2 fixture 被重写（当前 `:376-478`，初版失败点 `:435` → 现 `:463`）+ 长用例补了 `t.Parallel()`"完全吻合。**结论：现存日志并非整体过期，H2 用例之前的部分与当前源码同版；H2 用例及其后的部分尚无对应通过记录**——与你说的"最终 race 复跑进行中"一致，我不将其计为已通过。
- **N10** `evidence/vet.log` 为 **0 字节**。无输出多半就是干净，但空文件不自证；建议在其中记录命令行与退出码。

---

## 五、未由我执行的部分（请勿当作我背书的通过）

`implementation-focused.log` / `darwin-race.log` / `linux-race.log` / `grid.log`（仅 `TestDisconnect`+`TestSingleRoundtrip`）/ `default-30s-transfers.log`（33.03s 明文与 TLS 上传/下载）/ `vet.log` 均为**我阅读的记录**，非我运行。Linux 的 `dial_deadline_linux_test.go`（`//go:build linux`）在本机 Darwin 上不会运行，我只静态核验了它确实断言了默认滚动语义的三种情形（50ms 显式被续期、zero 禁用、1min 显式仍被 idle 截断）。最终全量 race 复跑结果待你回填。

修掉 B1（或明确判定 tidy 门禁不适用）后，我这边即可转 APPROVE。
