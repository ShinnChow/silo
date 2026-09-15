I'll start by reading the required materials to ground my review.

Now let me verify the actual Go 1.27.1 `net/http` behavior rather than trusting the plan's summary.

Verification complete. Writing my review record to the plan file.

Write 工具在本会话被禁用（确为只读评审），故评审记录直接以回复形式交付。

---

# R8 独立评审 — Opus 5.0 reviewer

**结论：CONSENSUS —— 同意 `docs/investigations/r8/plan-v1.md`（SHA256 `7cb609e37e3199ecd93c992f08d123968ba8082682a108f20490e0644d735366`），无遗留阻塞性分歧。** 共 9 条非阻塞意见。

> 诚实性声明（先行）：① SHA256 采用你给出的值——本会话只读工具集不含哈希命令，我**未独立重算**；② `evidence/baseline.log` 对应的临时测试文件已不在 worktree（`git status` 仅 `docs/investigations/r8/` 未跟踪），我**未重跑**该日志，只是从 Go 源码独立推导出的机制与该日志完全吻合；③ 未使用子代理、未模拟工具、未声称运行过任何测试。

## 一、逐条核验（Go 1.27.1 源码，`/opt/homebrew/Cellar/go/1.27.1/libexec/src/net/http/`）

| 核验项 | 事实 | 位置 |
|---|---|---|
| ConnState 相位序 | `setState(c.rwc, StateNew, runHooks)` 在 `go c.serve()` **之前**，hook 同步执行、传入 `c.rwc` | `server.go:3580`, `1865-1884` |
| 首个 header deadline | 在 serve loop **之前**设置，此时 state 仍为 StateNew | `server.go:2038-2040` |
| StateActive 时机 | `readRequest` **返回之后**才触发；成功路径必然触发（`setInfiniteReadLimit()` 把 `remain` 置 `maxInt64`） | `2054-2059`, `1067` |
| whole-request deadline | Go 1.27 **无条件** `SetReadDeadline(t0+ReadTimeout)`（旧版的 `!hdrDeadline.Equal(...)` 条件已删） | `1103`, `1038-1041` |
| TLS 握手 | 读写 deadline = `now + min(正RHT,RT,WT)` = SILO 30s；成功后两侧清零 | `1962-1968`, `964-984`, `1988-1992` |
| zero / background EOF | 有 body → EOF 回调启动 background read；无 body → 立即启动，且 `SetReadDeadline(zero)` | `2123-2127`, `731-743` |
| abort / finishRequest | `abortPendingRead` 设 `aLongTimeAgo` → 等待 → 清零；由 `finishRequest` 调用 | `785-797`, `1694-1707` |
| keep-alive 第二个 header | StateIdle hook → `SetReadDeadline(now+idle)` → `Peek(4)` → `SetReadDeadline(now+RHT)`，**全部落在 strict 窗口内** | `2152`, `2163-2181` |
| Hijack（grid/ws） | `abortPendingRead` + `rwc.SetDeadline(zero)` + StateHijacked | `318-337` |
| 写侧 | 每请求由 `readRequest` 的 defer 重新武装，请求结束清零 | `1042-1046`, `2145` |
| H2 是否启用 | SILO 设了 `TLSConfig` 且 `NextProtos` 含 `"h2"` → `shouldConfigureHTTP2ForServe()` 为真 → `s.h2` 配置，走内置 http2 | `3469-3489`, `http2.go:82` |
| H2 与 hook | h2 **会**调用用户 hook（带 nil 保护）；进入 ServeConn 前把两侧 deadline 清零；除 per-stream 定时器外无任何 conn 级 `SetReadDeadline` | `http2.go:100-101,188-198`; `internal/http2/server.go:571-572, 799-800, 1567, 2100, 1970-1972` |

SILO 侧：`cmd/server-main.go:907-913`（RT=WT=Idle=30s，RHT=30s）、`internal/http/listener.go:73`、`dial_linux.go:126-132`、`grid/manager.go:193`、`cmd/utils.go:970`。全仓库 `*.go` **无任何 `ConnState` 使用**；现有 `internal/deadlineconn`、`internal/http` 测试均不设显式 deadline，故不受 strict 影响。

## 二、相位推导：option 4 为何正确

- **strict ON 覆盖**：accept/StateNew → TLS 握手读 → 首个 header（`2038`）；以及 `2152` StateIdle 到下一轮 `2058` StateActive 之间的 idle 等待（`2163`）**与第二个 header**（`2177`）。
- **strict OFF 覆盖**：`2058` 之后到 `2152` 之前，即 body 读 + handler 全程 → 保留 rolling，长上传不被硬顶。
- **`1103` 与 `2058` 之间**虽仍 strict 且 explicit 已变为 `t0+ReadTimeout`，但该区间**不存在任何读**（只有 `2106` 的 header 检查与 `2118-2127` 的登记）——无副作用。
- **zero 优先于 strict**（`infReads` 提前 return）→ background read、hijack、h2 全部保持原语义；长 handler（如 `mc admin trace` 这类流式 GET）不会被误杀。
- 节流与 `readSetAt` 重置的组合可保证：strict 上限一旦写入 socket 就不会被后续 `Read` 重新拉长；模式切换重置 `readSetAt` 使下一次读立即按新模式重算。

**对 baseline 的验证**：strict 下 header 上限 = `min(now+2s+250ms, now+100ms)` = `now+100ms` → 400ms 完成的 header 必被拒，与标准 `net/http` 一致。

## 三、非阻塞意见（N1–N9）

1. **N1 措辞订正**：计划 20 行说 header deadline 在 `readRequest` 内设；实际在 `2038`/`2177`。且 Go 1.27 的 `1103` 是**无条件**的——这反而让 option 1 的否决理由更硬：RT=30s 会硬顶「header+body」整请求。
2. **N2 不变量要写死**：StateActive 的触发依据应记为 `1067` 的 `setInfiniteReadLimit()`，而非「读到字节」。pipelined 请求即使 header 全来自 `bufio` 缓冲、零 socket 读也必然触发。建议补一个 pipelined 用例。
3. **N3 可简化**：h2 特判可省——`http2.go:100-101` 已清零两侧 deadline，strict 分支恒被 `infReads` 短路。保留亦正确，只多一次 `ConnectionState()` 加锁；若保留，注意 StateNew 时握手尚未发生，不能依赖其返回值。
4. **N4 可简化**：`Accept` 里开 strict 是冗余的（`3580` 严格 happens-before 任何读）。保留可作纵深防御，但会让「未装 hook 的 httpListener 使用者」隐式获得 strict body 语义（今天不存在，`httpListener` 未导出、仅 `Server.Init` 构造）。两者皆可，请写明这层耦合。
5. **N5 测试有效性（最重要）**：`cmd/utils.go:970` 顺序为 `{"http/1.1","h2"}`，Go 的 ALPN 以**服务端顺序**优先 → 普通 Go HTTP/2 客户端会落回 http/1.1。H2 smoke 必须用只声明 `NextProtos: []string{"h2"}` 的 `tls.Client`，并断言 `NegotiatedProtocol == "h2"` 与 `resp.Proto == "HTTP/2.0"`，否则是假阳性。
6. **N6 回归护栏**：写侧 rolling 是大对象 GET 的命门（`1042-1046` 每请求 `now+30s`，全靠每 250ms 续期）。计划正确地不动写侧；建议在 `setWriteDeadline` 旁注明「不可对称 strict」，并在矩阵补一个 >30s 的持续**下载**用例。
7. **N7 发布说明**：两处用户可见收紧——TLS 握手读侧被 `min(RHT,RT,WT)=30s` 硬顶；请求头被 `ReadHeaderTimeout` 硬顶（即便字节持续到达）。这正是 stock `net/http` 语义，但极慢链路客户端可能受影响，需文档化。
8. **N8 影响面比计划更小**：`cmd/server-main.go:421-422` 的 `DriveOPTimeout` 仍被 FIXME 注释掉 → `dial_linux.go:126` 分支不成立，**生产内节点连接根本不经过 DeadlineConn**。计划结论正确，建议据实补记。
9. **N9 已知项**：握手写侧仍会被续期，慢读 ServerHello 的客户端仍能拉长写侧占用。既有缺陷、本轮不修，建议显式登记而非静默。

## 四、替代方案对比（是否存在更简单的正确解：无）

- **全局 clamp**：`1103` 无条件设 `wholeReqDeadline`，RT=30s 直接破功大对象上传。❌
- **去 ReadTimeout + 把 zero 解释成 rolling**：与 `731-743`（background read）、`326`（hijack）冲突，两处都用 zero 表达「永不超时」。❌
- **body/ResponseController wrapper**：引入 drain/EOF/chunked 记账 + h2 专门处理，范围远超本缺陷。❌
- **由 hook 自行推算 cap（`now+RHT` 等）**：等于在 SILO 侧复刻 stdlib 的 deadline 算术，会随 Go 版本漂移（1.27 相对旧版已改两处）。计划「记住 stdlib 实际要求的值」更稳健。❌
- **按「显式值是否短于 idle 窗口」自动判定**：生产默认 RHT = RT = Idle = 30s 三者相等，无法区分相位，仅在 baseline 的 100ms/2s 配置下看似有效，**不是通用正确解**。❌

所需信息（deadline 由哪一层请求 + 当前 HTTP 相位）只在 `DeadlineConn` API 边界与 `ConnState` 两处可得，**option 4 是最小正确形状**。

## 五、后续（实现仍需实际执行验证，不得以推断代替）

同意计划的 7 项矩阵，并请纳入 N2（pipelined）、N5（真·h2 断言）、N6（>30s 持续下载）三项补充；`-race` 下必须覆盖 `Read` / `SetReadDeadline` / `SetReadDeadlineStrict` 三者并发。Linux 部分若只有编译而无运行时验证，请分别如实报告。
