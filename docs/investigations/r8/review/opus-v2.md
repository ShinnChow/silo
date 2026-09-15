I'll begin by reading the plan and its supporting evidence directly — as an independent reviewer I won't delegate verification.

Now the actual production code — the core of what I must verify independently.

Now the Go 1.27.1 source, to verify zero/negative semantics of `ReadHeaderTimeout` myself.

已完成独立只读复核。我亲自读取了 Go 1.27.1 `net/http` 源码、`minio/cli@v1.24.2` 源码、全部相关生产文件与新测试、以及 r8 目录下的计划/共识/证据文档。

---

# R8 完整方案 v2 评审 — Opus 5.0（独立、只读）

## 结论：**CONSENSUS**

我对 **`docs/investigations/r8/plan-v2.md`（v2，SHA256 `426127ed9fb08aeddf8259ebdc4b1c24ebec8cda751a970ed99338a44b065f4c`，基线 `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`）的完整范围**（v1 连接层设计 + 新增配置绑定）表示同意，**零条阻塞性分歧**。

**据此授权在该方案范围内实施所提议的配置绑定**：在 `cmd/common-main.go` 的 `buildServerCtxt` 中于 `IdleTimeout` 旁新增 `ctxt.ReadHeaderTimeout = ctx.Duration("read-header-timeout")`。实现与测试验收仍是独立环节，不在本次同意范围内；合并、发布、部署同样不在内。

**B1 判定：已解决。** 全仓库 `.go` 文件 Grep `golang.org/x/net` **零命中**；`internal/http/server_deadline_test.go:20-35` 只引入标准库与 `internal/deadlineconn`。h2 改由标准库 `stdhttp.Protocols` + `SetHTTP2(true)`（`:409-411`）强制，断言 `NegotiatedProtocol=="h2" && Proto=="HTTP/2.0"`（`:420`）与 `ProtoMajor!=2` 保护（`:383-384`）均保留——即使协议回落也会响亮失败而非静默降级。由于主模块无任何包（含测试）直接导入 `golang.org/x/net/*`，`go mod tidy` 没有理由把 `go.mod:297` 的 `// indirect` 提升为直接依赖，`Makefile:49-58` 的 `check-gen` 门禁不会被打红，也不会因此丢失该 require（它仍被其他模块间接需要）。N1 亦已落实：`deadlineconn_strict_test.go:165-178` 跨 3 个真实 300ms 周期、全程不重设上限地断言 clamp 不外推。N3 仅对 h2 夹具放宽 keep-alive idle（`server_deadline_test.go:406` `srv.IdleTimeout = 3 * time.Second`），未触及其他用例。

> **诚实性声明**：① 本会话仅有 Read/Grep/Glob，**无 shell**。因此我**没有重算 v2 的 SHA256**，也没有运行任何 `go test`/`go vet`/`go mod tidy`/`git`；我核验的是该路径下实际文件的**内容**（116 行，与下文逐条引用一致）。② 所有"日志显示"均为我**阅读记录**的转述，非我运行。③ 未使用子代理。④ 未验证当前 HEAD 是否等于所述基线。

---

## 一、最小充分性：我独立确认的闭链（C1–C8）

| # | 结论 | 我核验的依据 |
|---|---|---|
| C1 | 一行赋值**充分** | 全库仅 `cmd/server-main.go:912` 读 `globalServerCtxt.ReadHeaderTimeout`；唯一 HTTP server 构造点也仅 `server-main.go:906`（`xhttp.NewServer` 全库两处命中，另一处在 patch 文档里） |
| C2 | 链路闭合 | `buildServerCtxt`（`common-main.go:370`）是 serverCtxt 唯一填充函数；`serverMain` 在 `server-main.go:799` 用它填 `globalServerCtxt` |
| C3 | **默认行为零变化** | `DefaultIdleTimeout == DefaultReadHeaderTimeout == 30s`（`internal/http/server.go:44-48`）→ `readHeaderTimeout()`（Go `server.go:3752-3757`）绑定前后都返回 30s；`tlsHandshakeTimeout()`（`:969-983`）也都是 30s。plan 第 17 行"两值相等时默认值恰好看起来正常"我独立证实 |
| C4 | YAML 不可能覆盖 | `ServerConfigCommon`/`Opts`（`internal/config/server.go:20-45`）**根本没有超时字段**；`configCommonToSrvCtx`（`server-main.go:274-302`）只处理 RootUser/Pwd/Addr/ConsoleAddr/CertsDir/FTP/SFTP。即便 merge 发生在赋值之后（`common-main.go:450-454`）也无字段可覆盖 |
| C5 | 优先级/错误面**完全不变** | `minio/cli@v1.24.2/flag.go:569-594`：env 作为 flag 默认值、命令行再覆盖 → flag > env > 静态默认；"存在但为空"的 env 在 `time.ParseDuration("")` 处报错，**发生在 flag 解析阶段，与本绑定无关、绑定前已如此**（`config-baseline-env-fixture-failure.log` 正是该现象）。新行不引入任何新错误路径 |
| C6 | h2 不受影响 | `net/http/http2.go:199-211` 只把 `ReadTimeout`/`WriteTimeout`/`IdleTimeout` 交给 h2，`ReadHeaderTimeout` **完全不进入** h2 配置。plan 第 34/71 行正确 |
| C7 | 共享调用方不受影响 | `deadlineconn.New` 仅 `listener.go:73` 与 `dial_linux.go:130`（后者不开 strict）；`SetReadDeadlineStrict` 生产调用仅 `listener.go:75`、`server.go:141/145` |
| C8 | v1 在 v2 中**逐字保留**，实现与之一致 | plan-v1 `13-24/26-31/35-55/57-66/70-76/78-80` ↔ plan-v2 `23-34/36-41/52-72/74-83/94-100/102-104`；当前生产文件 `deadlineconn.go:37-38,56-87,129-154`、`listener.go:73-76`、`server.go:127-151`、`server-main.go:904-915` 与之吻合。v2 没有夹带修改已同意的 v1 语义 |

关于**证据可信度**：我不采信"已执行"的口头声明，但 `config_baseline_test.go.txt:26,36` 的设计本身具备自证力——同一夹具用 `MINIO_IDLE_TIMEOUT=2s` 并断言 `IdleTimeout==2s` 在全部 7 个子用例成立，这是一个**对照组**，排除了"夹具坏了"的解释；而 `ReadHeaderTimeout` 恒为 0。再叠加我上面对 `flag.go:569-594` 的独立源码核验，`config-baseline.log` 的每一行（30s/100ms/170ms/80ms/-1s）都可从源码推导出来。

**阻塞性发现：0 条。**

---

## 二、非阻塞发现（N1–N9）

1. **N1（新耦合，建议写入交付说明）** 绑定后 RHT 首次参与 TLS 握手窗口：`tlsHandshakeTimeout()`（Go `server.go:964-983`）取 RHT/RT/WT 中**正值的最小者**，并在 `:1962-1968` 同时下发读写 deadline。绑定前 RHT 恒为 0 → 窗口 = idle；绑定后 `--read-header-timeout=1s` 会把 TLS 握手（含 h2 的握手阶段）一起压到 1s。默认下无变化（30s）。

2. **N2（唯一"变松"的组合，最值得单列）** 相对**当前已实现的 v1**：用户只设 `--idle-timeout=2s`、不设 RHT 时，请求头上限从 2s 变为默认 30s。相对**真实基线 v0**（滚动续期、滴入可无限延长）仍是收紧。这是两个独立旋钮的正确语义，但它是全部组合中唯一"看起来放松"的一种，交付说明应明确点名，避免被误读为回归。

3. **N3（负值语义在明文/TLS 下不对称）** `--read-header-timeout=-1s` → `readHeaderTimeout()` 返回 -1s（`:3752-3757`），首请求处 `server.go:2038` 的 `d>0` 不成立 → **不下发任何 deadline**：明文连接因 `readExplicit` 为零值而在 `deadlineconn.go:71` 短路 clamp，回落到滚动 idle；TLS 连接则已在 `:1990-1991` 被清零 → `infReads=true` → 首个请求头**真正无上限**。keep-alive 第二个请求处 `:2179-2180` 显式下发零值，两者统一为无上限。这是 Go 文档化的 "negative = no timeout"（`:3072-3078`）且需用户显式选择，不要求改设计；建议测试断言并在说明中写明这一差异。

4. **N4（正面变化，但仍是可见变化）** `--idle-timeout=0`/负值时：绑定前 RT≤0 且 RHT=0 → `readHeaderTimeout()` 返回 0 → **完全没有请求头上限**，`tlsHandshakeTimeout()` 也为 0；绑定后默认 30s 生效。这是修复带来的安全性改善，建议同样纳入说明并补一条用例。

5. **N5（验证矩阵缺口）** v2 矩阵未提及 `buildscripts/test-timeout.sh`（`Makefile:177-179`），而它是仓库现存唯一端到端超时测试且直接使用 `--read-header-timeout 5s --idle-timeout 5s`。我推演结论不变：两值相等 → 绑定前后 `readHeaderTimeout()` 都是 5s，三个用例（20s 慢头 / 40s 慢 body / 1s+1s 正常）判定与 `:69` 的 `<= 11s` 时限均满足。建议验收时实跑并记录。顺带值得写进报告：该脚本用的是"一次长睡眠"而非"持续滴入"（`:44-61`），这正是 R8 缺陷长期未被它捕获的原因。

6. **N6（强烈建议）** 把配置用例固化为仓库内**常驻**回归测试，而非仅留 `evidence/*.txt`。缺陷本质是"结构体少复制一行"，只有常驻断言能防止再次静默回归；夹具可直接落地（`cmd/testdata/config/1.yaml` 存在）。注意 `zero-fallback` 子用例期望值为 0，**绑定前后都通过、不具判别力**，应保留但标注。

7. **N7（备案，无需改动）** 第二调用方 `cmd/fmt-gen.go:80` 也执行 `buildServerCtxt`，而 `fmtGenFlags`（`:30-45`）未注册该 flag。我核验 `minio/cli@v1.24.2/flag_generated.go:141-151`：`lookupDuration` 对未注册 flag 返回 0 且不 panic；现存 `ctx.Duration("idle-timeout")` 已在该路径长期运行，证明模式安全，且 fmt-gen 不启动 HTTP server。

8. **N8（勿顺手清理）** `common-main.go:444` 与 `:448` 重复赋值 `ctxt.UserTimeout`，属既有无害冗余。新行紧邻该处，**请不要在本次改动中一并清理**，以保持 diff 最小。

9. **N9（证据闭环）** `vet.log` 仍为 0 字节（v1 评审 N10 已接受尚未回填）；v2 两条新链建议统一记录命令行、退出码与二进制 SHA256。`runtime-v1.json` 只是**修复前**证据（`expected_rejection=false`，两例均 `HTTP/1.1 200 OK`），必须补一份 `expected_rejection=true` 的同探针输出才算闭环。我另行核验了探针判据 `rejected = not data`（`runtime_probe.py:59`）**正确**：读头超时后 Go 走 `isCommonNetReadError`（`server.go:1915-1926`，`net.Error.Timeout()` 为真）→ `:2090-2091` `return // don't reply`，确实不写任何响应字节，不会出现 408 误判。

---

## 三、边界（不由本次修复覆盖，请在交付说明中重述）

- HTTP/2 既有的**绝对** per-stream `ReadTimeout` 不在 R8 范围（`http2.go:203` 直接透传 `ReadTimeout`）。
- TLS 握手**写**侧仍为滚动截止，属既有边界。
- 我未执行任何测试；`darwin-race.log` / `linux-race.log` / `grid.log` / `default-30s-transfers.log` 等均为我阅读的记录，不构成我的背书。修改过的 h2 与 strict 夹具需在本轮变更后重跑并回填。
