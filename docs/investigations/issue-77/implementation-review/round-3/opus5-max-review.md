已完成定向核对。以下为最终报告。

---

# SILO #77 第三轮独立对抗性验收（固定 `fcbb93e8957275bfa7ad4e6154e93f4d7b0a7025`）

**结论：GO_WITH_NONBLOCKING_NOTES — 阻断数 0**

范围限定本轮差异（`round-3/change.diff`：2 个生产文件共约 12 行、5 个测试文件、1 个 README 段落）。未重复前两轮已完成的全量审查。

---

## 一、逐条 NB 判定

### NB-2 — 关闭（三个边界经代码级核对成立）

生产改动在 `cmd/site-replication-metadata.go:134-160`。逐边界挑战结果：

1. **`unreachable` 仅在存在候选时输出 —— 行为字节级不变**。改前 `if !found { return nil }` 就在 unreachable 循环之前，因此该日志从来只在 `found==true` 时产生；把它包进 `if found {}`（`:138-144`）是纯重构，没有新增也没有删除任何一条 unreachable。这一点上一轮报告与提交说明都没讲清，值得记录：**这不是行为修改**。
2. **空基线／缺桶安静 —— 由 `newBucketConfigState` 的取值规则保证**（`cmd/bucket-metadata-replication.go:224-227`）：`at.IsZero() → at = created`。因此"字段时间为零但 `CreatedAt` 非零"的合法基线必然 `valid=true`，不触发 `:154`；缺桶（全零）`data` 空且 `at` 零，也不触发。只有三类会响：`created==0 且 at/data 非空`、`at < created`、解码/解析报错。与新测试四个子用例一一对应，无第四种漏网情形。
3. **无有效来源不发 RPC、不落盘 —— 成立**。`!found` 在 `:159` 即 `continue`，位于 `:177`（本地 `updateAndParseMetadata`）与 `:182`（`SRPeerReplicateBucketMeta`）之前。测试用 `len(events()) != 0` 从对端侧反证（`heal_test.go:162-164`）。
4. **去重 —— 成立**。`logOnceIf`（`internal/logger/logonce.go:95-120`）按 `id` **且**错误正文相同才抑制；key 为 `bucket-metadata/<bucket>/<file>/<reason>`，正文只含 reason，时间/peer 全在 ReqInfo 属性里（`site-replication-metadata.go:33-43`）。所以多 peer 同因塌缩为一条，跨因不互相顶替。测试的 `for range 2` 钉住了这一点。

**必要性证据成立**：`round-2/no-source-before.log` 用同一份最终测试叠加旧生产码，`unknown-created`/`malformed`/`before-created` 三个子用例在 `heal_test.go:155` 报 `got 0 diagnostics, want 1`，`empty-baseline` 两侧都安静。行号与当前文件精确吻合，是真实的前后对照，不是草稿产物。

**接受的边界（非阻断，已成文）**：在全站 `Created==0` 的遗留桶上（滚动升级的典型形态），每个"有值字段"现在每小时多出一条 `indeterminate`，此前完全静默。这正是 NB-2 要的可行动信号，README:123-125 与设计记录 §limits-5 都写明了。量级与 NB-3 已接受的 `unreachable` 同阶。

**性能挑战结果：不构成问题**。`!found` 时主循环多做一遍 `bucketConfigStateFromInfo`，但空载荷在 `site-replication-metadata.go:75` 与 `bucket-metadata-replication.go:178-180` 两处短路，连 base64 都不解——而空载荷恰好是 `!found` 的主体人群。上一轮建议的"缓存解码结果"因此确实可以不做。

### NB-8 — 关闭，nil/zero/local 三分支均无错误时间与 panic

`cmd/bucket-metadata-sys.go:186-192`：

- `sourceTime == nil` → `at` 为零值，打印 `0001-01-01`，与改前一致，无解引用；
- `sourceTime != nil && IsZero()` → 同上，不会把零值当作有效来源时间；
- `sourceTime` 非零且为本地时区 → `logBucketConfigReplication:38` 统一 `at.UTC().Format(...)`，不产生偏移时间；
- `meta.Created`：该分支下必为零（`ensureBucketMetadataCreated:278-280` 在非零时提前返回 nil），所以 `created` 属性如实；
- 去重不受影响：错误正文未变（见上）。

**一处同类残留（本轮之前既有，非本轮引入）**：`site-replication-metadata.go:155` 在 `currentErr != nil` 时 `current` 是零值结构体，于是"解析失败"的日志同样丢掉了对端上报的真实 `at`。最小修复需要一个不经解码就取 `(bucket,file) → at` 的取值函数，约 10 行。可选，不必本轮做。

### NB-4 / NB-5 — 关闭；全仓无漏引用，编译成立；有一项必须如实指出的门禁事实

- **漏引用**：`isBucketMetadataEqual` 在 Go 代码中零引用，仅 `docs/site-replication/CORS-LWW-DESIGN.md:84` 提及，且位于 "Confirmed Failures in the **Pre-Fix** Candidate" 小节，是对既往缺陷的历史陈述，不是对现有代码的断言，无需改。
- **编译**：两个被删用例所在文件的 `encoding/base64` 导入仍在被使用（`site-replication_test.go:21` → `:80/:106`；`bucket-cors-site-replication_test.go:22` → 20 余处），不存在第二次漏删。`round-3/final-build.log`、`final-vet.log` 在本轮固定 SHA 上 `go build ./...` / `go vet ./...` 均 `EXIT=0`。**当前树完整编译成立**。
- **真实 CORS 路径保留**：被删的是纯 helper 单测；同一性质（base64 必须按字节严格比较）现由 `TestPeerBucketCorsRejectsNonCanonicalBase64`（`bucket-cors-site-replication_test.go:782-815`）经真实 `PeerBucketCorsConfigHandler` 与 legacy bulk 两条生产路径覆盖，并断言被拒后元数据未变；heal 侧用 `corsReplicationStateFromInfo`/`equalCORSReplicationStates`（`site-replication.go:4903-4904`）而非被删函数。**无覆盖损失。**
- **logger 全局状态与 race**：`testlogger.T` 是进程级单例但用 atomic（`testlogger.go:49,78-85`），本身无 race；`logger.DisableLog` 是普通全局 bool（`logger.go:407` 读、`test-utils_test.go:105` 默认置 true），两个新用例都 `defer` 还原。`round-3/final-target-race.log` 在本轮固定 SHA 上对包含全部新用例的集合 `-race` 跑通（20.9s，EXIT=0）。**已验证，非阻断。**
- **两项潜在脆弱点（无阻断、目前未触发，仅备案）**：(a) `DisableLog=false` 期间全局 sink 会捕获进程内**任何**系统日志，而 `heal_test.go:97` 对任何非预期行直接 `Fatalf`——若窗口内有后台 goroutine 输出日志即失败。最小加固：分类前先 `strings.Contains(line, bucket)` 过滤外来日志。(b) `logOnce.IDMap` 是进程级、每小时才清（`logonce.go:123-131`），这两个用例之所以安全，是因为 `ExecObjectLayerAPITest` 对两种后端各自 `getRandomBucketName()`（`test-utils_test.go:1556`，分别在 `:1771`/`:1801` 调用）使日志 key 天然唯一——这是隐式依赖，值得在注释里点一句。
- **证据精度**：`round-2/confirmed-diagnostics-before.log` 在 `heal_test.go:80`（**第一条**断言）就 Fatal，因此它只证明了 F3 的"空基线必须安静"这一半；WARNING 级别与按因去重（`:99-105`）在旧码上并未被执行到，**没有**对应的失败演示。应如实这么写，不能说成"F3 已被完整回归保护"。

- **必须指出的门禁事实**：`round-3/pre-style-lint.log` 显示本轮固定提交 `fcbb93e8` 的 `make lint` **失败（EXIT=2，3 issues）**，全部落在本轮新增的两个测试文件：`cmd/site-replication-metadata-heal_test.go:92:5`（gocritic ifElseChain）、`cmd/site-replication-metadata-gate_test.go:282:1` 与 `heal_test.go:138:1`（gofumpt）。我通读了修复补丁 `test-style.diff`：switch 分支顺序与函数体同 if-else 完全一致，另两处仅为结构体字面量换行，**语义等价、仅测试文件**，已在后续提交 `461e9a72` 修掉（该提交的 lint 复跑在证据中仍在进行）。结论：不影响行为验收，但"`fcbb93e8` 本身通过全部仓库门禁"这句话不成立，不应这么写。

### NB-6 — 关闭为**出站 wire 证明**；不得称作双站点证明（已如实区分）

`TestBucketMetadataInitialSyncPhysicalCreated`（`gate_test.go:245-303`）确实驱动真实源 ObjectLayer 走完整出站序列：`syncToAllPeers`（`site-replication.go:2141-2153`）→ `ensureBucketMetadataCreated` 从真实盘目录 mtime 恢复 → `MakeBucketHook`（`:821-826` 缓存零值回落）→ wire 上的 `createdAt` 参数；再经 `initialBucketConfigReplicationEvent` 发出 Tags 事件。断言 `createdAt == physical`、Tags 载荷为 base64(XML) 且 `UpdatedAt == physical`（历史字段以 Created 作源时间）。前后对照成立：`round-2/initial-sync-before.log` 在 `gate_test.go:289` 报 `"0001-01-01T00:00:00Z"`，行号与最终文件精确吻合。

**必须如实标注的范围限制（我独立核出，不在提交说明里）**：该用例的 `c.state.Peers` 只含 `"initial-peer"`（`gate_test.go:282-283`），而 `concDo`（`site-replication.go:2497-2515`）只对 `depID == globalDeploymentID()` 的条目执行 `selfActionFn`。该条目不存在，因此真正把 `Created` 落到**本地盘**的 `PeerBucketMakeWithVersioningHandler`（`site-replication.go:954-966` 的 `SetCreatedAt` + `saveMetadata`）**在本用例中根本没有执行**。也就是说：

- README:110-114 "Recovery happens … during initial site sync … records the physical time" 在生产路径上成立（经 `concDo` 自身分支），但**不被这个测试覆盖**；
- 本用例证明的是出站内容，不是任何一侧的落盘，更不是双站点收敛。

设计记录 §205 已经把这个区分写对了（"它证明出站内容…二者不能混称为同一验收"），README 也没有声称测试覆盖，因此**不构成不实陈述**。若要补齐本地落盘那一半：在该 Peers map 中加入 `globalDeploymentID(): {}` 并在末尾加一次 `readBucketMetadata` 断言 `Created==physical`，约 3 行。

### NB-7 — 关闭，两半都钉住了

`site-replication-metadata_test.go:482-490`。沿 `applyBucketConfig`（`bucket-metadata-replication.go:294-316`）+ `compareBucketConfigStates`（`:239-245`）核对：`incoming.valid=true` 对 `local.valid=false` 直接返回 1，因此 `changed=true`，随后断言指针别名上的 `*at == incomingAt`、`*data == incoming`——载荷与时间都断言了。对 `bucketTaggingConfig` 这是"活标签覆盖被保留的旧世代删除记录"，语义与注释一致；对 `bucketSSEConfig` 载荷恰好与原值相同，那里真正承载证明的是时间戳断言。两点说明：断言层级是 `applyBucketConfig`（即写路径 `bucket-metadata-sys.go:206` 调用的同一决策函数），属单元级钉桩而非端到端，这是可接受的取舍。

### NB-1 — 判定为**如实保留边界**，本轮不存在必须修的回归

本轮对 NB-1 **没有任何生产代码改动**（仅 README:106-114 + 新用例），因此按构造不可能引入回归。我独立复核了被保留的行为与被拒方案：

- 保留跳过：`bucket-metadata-sys.go:202-205` 在 `updatedAt.Before(meta.Created)` 时记 `before-created` 并 `return nil`（不写、不报错）；heal 侧同因在 `site-replication-metadata.go:162-164`。
- 拒绝 `min(recovered, sourceTime)` 的理由我认为**成立**：单个事件的时间戳无法区分"本世代的较早事件"与"上一个已删除世代的事件"，凭一个事件下调 `Created` 会让删除前的状态复活。这里选安全而非活性是对的，且具备自愈性（下一次 ≥ physical 的写入即确立身份）与可观测性（有界 warning）。
- 新用例 `TestPeerBucketMetadataPhysicalCreatedBoundary`（`gate_test.go:305-335`）用真实盘 mtime 同时钉住两侧：`physical-1h` 的 peer 事件 `err==nil` 且 `bucketConfigWriteCounter` 计数为 **0**（是跳过不是失败）；随后本地 `Update` 使盘上 `Created==physical` 且 `at > physical`。断言选得准确。
- 残留（已成文）：唯一持有者的源时间早于恢复值的字段，不经人工介入不会收敛；且跳过路径**不落盘恢复值**（`:204` 早于 `saveMetadata`），每次事件都重新探测 mtime——README:112-113 用 "first **successful** configuration write" 措辞正确地表达了这一点。

### NB-3 — 接受现状，不要求扩展

`site-replication-metadata.go:138-144` 仍按桶×字段×原因输出，`healBuckets` 每桶调用六次（`site-replication.go:4753-4760` → `:4851-4935`）。设计记录 §limits-5 已明确声明该量级会随桶与有值字段增长。未构成阻断，也未破坏任何既定契约，按约定不要求做站点级聚合/限流改造。

**一处文档精度小瑕**：§limits-5 把"日志量随桶与字段增长"这句只挂在"不可达站点"上；本轮之后，遗留零 `Created` 集群里的 `indeterminate` 同样按桶×字段增长。加半句即可，非阻断。

### F5 — 确认无外溢

本轮 diff 中没有任何 `canonicalBucketPolicy` 接入点的增删；只有设计记录 F5 一行表述更新（`bucket-metadata-convergence.md:182` / `.zh.md:185`），与"上轮已修正原判断、不再改代码"一致。

---

## 二、确证问题清单（均非阻断）

| # | 位置 | 触发条件 | 最小修复 |
|---|---|---|---|
| 1 | `cmd/site-replication-metadata-heal_test.go:92`、`:138`；`cmd/site-replication-metadata-gate_test.go:282` | 在固定提交 `fcbb93e8` 上执行 `make lint` | 已在 `461e9a72` 修掉（语义等价、仅测试文件）；本轮 SHA 需注明 lint 未过 |
| 2 | `cmd/site-replication-metadata.go:155` | heal 遇到解码/解析失败的对端状态 | 日志属性中 `sourceTime` 恒为 `0001-01-01`；需一个免解码的 `(bucket,file)→at` 取值函数，约 10 行。可选 |
| 3 | `cmd/site-replication-metadata-heal_test.go:97` | 日志窗口内任一后台 goroutine 输出日志 | 分类前加 `strings.Contains(line, bucket)` 过滤，1 行 |
| 4 | `cmd/site-replication-metadata-gate_test.go:282-283` | —（覆盖缺口，非缺陷） | 若要覆盖 README 的"初次同步落盘 Created"一半：Peers 加入 `globalDeploymentID()` 并补一次 `readBucketMetadata` 断言，约 3 行 |
| 5 | 设计记录 §limits-5 | — | "随桶与字段增长"补上 `indeterminate` 这一类，半句 |

**证据状态如实说明**：`round-3/final-cmd.log` 只有头行，**完整 `go test ./cmd/` 尚未产生结果**，不可称作已通过；已完成且可复核的是 `final-build`/`final-vet`（EXIT=0）、`final-target-race`（EXIT=0）、`pre-style-lint`（EXIT=2，见上）。`461e9a72` 的 `make lint` 复跑同样在进行中。

---

## 三、最小性 / 充分性 / 必要性（独立评价）

**最小性：是，且是本系列迄今最克制的一轮。** 生产侧净变化只有两处共约 12 行：`found` 条件位移（未引入解码缓存、未新增结构体、未新增日志原因），以及诊断里一个已有指针的解引用。没有新 schema、新 wire 字段、新锁、新框架。我找不到可以无损删除的部分：删掉 `!found` 分支就退回 NB-2 的静默，删掉 `at` 就退回 NB-8。测试删除项（`isBucketMetadataEqual` 及其两个专属用例）经全仓核对确认无生产调用者、无覆盖损失。

**充分性：对本轮声称的目标充分；两处覆盖如实低于文字。** 新增三个用例都有真实前后对照或真实后端驱动，`-race` 通过。低于文字的两处均已在上文点名：F3 的级别/去重断言在旧码上未被演示失败；初次同步的本地落盘半边未被测试覆盖（生产路径本身成立）。二者都是证据强度问题，不是行为问题，且设计记录没有把它们说过头。

**必要性：是。** NB-2 与 NB-6 各有"最终测试 + 旧生产码"的复现日志且行号可逐条核对（`no-source-before.log`、`initial-sync-before.log`）；NB-8 是一行可读性缺陷的直接消除；NB-5 是删除无调用者代码；NB-7 是补一侧期望值；NB-1/NB-3 明确不改代码，只做文档与钉桩。没有一项属于"顺手扩张"。

**放行建议**：以 `461e9a72`（lint 修复后的等价树）为合并基准，并在合入前等 `go test ./cmd/` 与 `make lint` 两条复跑各自出结果；上表 5 项可全部进 backlog。
