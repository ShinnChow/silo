# SILO #77 修正提交独立复审（固定 `62cf066ff529c7d281703daa365f555cebba717a`）

审查方式：只读。以 worktree 生产源码为准（不只读 diff），交叉核对 `silo-pkg v3.13.4-0.20260910091716` 依赖源码、证据目录与设计记录。上一轮报告只作为待验证的命题，不作为已确认结论。

---

## 0. 结论

**GO_WITH_NONBLOCKING_NOTES**

- **阻断项：0**（无条件 0，条件性 0）
- 上一轮的条件性阻断 F1 与 S2 级 F2 均已在生产路径上真正关闭，并有"最终测试 + 旧生产代码覆盖"的复现证据（`round-2/confirmed-before.log`）。
- 新增 8 项非阻断发现（NB-1 ~ NB-8），其中 3 项是行为残留（S3/S4），5 项是证据与文档表述准确性问题。
- 一处需要纠正上一轮的**判断**（不是纠正实现）：F5 中"Policy 规范编码器接入 GET/export/peer 属于超出必要的扩张"这一说法不成立——这三个调用点持有的是**已解析策略**，必须有编码器，而旧编码器对解析器已支持的负集合策略**必然报错**。真正"可选"的只有 PUT/import 两处，且因 `DisallowUnknownFields` 而无损。

---

## 1. F1–F10 关闭状态

| 项 | 上轮判定 | 本轮核实 | 状态 |
|---|---|---|---|
| F1 零 Created 桶六类配置写入硬失败、初次同步静默漏发 | S1 条件阻断 | 生产路径已修复并有真实 ObjectLayer + 旧码覆盖复现 | **关闭**（残留见 NB-1） |
| F2 Policy 状态口径与收敛键冲突导致永久假 mismatch | S2 | `isBktPolicyReplicated` 改用同一 canonical 键，per-site presence 不变，无 SID/ID/Condition/未知值回归 | **关闭** |
| F3 heal 诊断 ERROR 级、key 互相顶替、正常瞬态误报 | S3 | ERROR→Warning、四类原因各自持 key、缺桶不报 = 已关闭；"全无有效来源即完全静默"与"按桶×字段扇出"未处理 | **部分关闭**（NB-2、NB-3） |
| F4 三处语义变更未文档化 | S3 | 上轮前两项系误读（原提交 README 已有，见 diff 中为 context 行）；第三项已补入 `docs/site-replication/README.md:129-134` | **关闭 + 原误读已纠正** |
| F5 Policy 规范编码器接入面 | 最小性争议 | 判断更新：GET/export/peer 非可选；PUT/import 可选但无损。建议保留现状 | **关闭（判断修正）** |
| F6 死代码与过时注释 | nit | 注释已修（`bucket-metadata-replication.go:35-37`）；`isBucketMetadataEqual` 仍为测试专用 | **关闭（保留 nit，见 NB-5）** |
| F7 gate=off 不承诺四类删除自愈 | 范围说明 | README:75-89、设计记录 §rollout/§limits 均如实声明，未说成已自动修复 | **保持（如实）** |
| F8 证据缺口（stub / 旧顺序策略 / 二进制身份） | 证据不足 | stub 已去除、旧顺序用例已补、二进制身份已闭环（`--version` 含 commit-id + 对照基线二进制） | **关闭**（但 NB-4：两份旧日志不可用作证据） |
| F9 桶世代冲突 | 范围外 | 未被声称已修；设计记录 §limits-3 明确 | **保持（如实）** |
| F10 接管 Created 越过真实字段时间 | 覆盖缺口 | 补 `shift=3h` 子用例，钉住"旧世代状态不得升级为有效来源" | **关闭（半边，见 NB-7）** |

---

## 2. 本轮重点逐项核实

### F1 —— 真正修好了，机制比上轮建议更完整

**`NoMetadata=true` 返回物理探测**（`cmd/erasure-server-pool.go:2279-2283`）。我枚举了全仓库 `NoMetadata` 的全部生产调用点，只有三处：

- `cmd/bucket-metadata-sys.go:272`（`saveMetadata` 存在性重查，只看 error）
- `cmd/bucket-metadata.go:306`（迁移锁内存在性重查，只看 error）
- `cmd/bucket-metadata-replication.go:281`（新增的 Created 恢复，正是要物理值）

其余全部是 `BucketOptions{}`（`NoMetadata=false`），行为不变。被跳过的 `globalBucketMetadataSys.Get()`（`bucket-metadata-sys.go:410-424`）是**纯缓存读、无副作用、不触发加载**，因此跳过它不会改变任何缓存预热或懒加载行为。`Versioning`/`ObjectLocking` 两个字段也只在非 NoMetadata 分支填，与 `ListBuckets`（`:2402/2422` 只在 `!NoMetadata` 时覆盖 Created）语义一致。**无调用者回归。**

**世代一致性链路完整**（这一点比上轮提出的方案更强，值得记录）：
`syncToAllPeers`（`site-replication.go:2145-2153`）恢复 `meta.Created` → 传入 `MakeBucketOptions.CreatedAt` → `MakeBucketHook`（`:821-826`）在缓存值为零时回落到 `opts.CreatedAt`（此时本地磁盘仍是 0，所以这个回落是必需的）→ **本地**分支 `PeerBucketMakeWithVersioningHandler`（`:943-966`）在 `metadata.lock` 内 `SetCreatedAt(opts.CreatedAt)` + `rebaseBucketConfigDefaults` + `saveMetadata`，**远端**分支通过 `optsMap["createdAt"]` 收到同一个值。也就是说本地并非"只在内存里补一下"，而是与全部对端落盘同一个世代值。这条链路 diff 上看不出来，必须读 `MakeBucketHook` 的 `concDo` 第一个闭包才能确认。

四个恢复点的锁位置也确认无误：写路径（`bucket-metadata-sys.go:186`，锁内）、bulk（`site-replication.go:1674`，锁内）、import（`admin-bucket-handlers.go:1092`，锁内）、初次同步（锁外探测但不落盘，落盘由上面的钩子在锁内完成）。

**"无法恢复时显式错误"是否正确：正确。** 静默发明 `UTCNow()` 会造出一个不可撤销、且会向全站点传播的假世代；而显式失败保留原数据、可重试、并留一条有界 `indeterminate`。`syncToAllPeers` 因此让整个 `AddPeerClusters` 失败——这在真实文件系统上基本不可达（`StatVol` 用目录 `ModTime`，存在即非零），且 `AddPeerClusters` 是可重试的交互式管理操作，代价可接受。

**真实用例是否覆盖生产路径：是。** `TestBucketMetadataPhysicalCreatedRecovery`（`site-replication-metadata-gate_test.go:206-237`）通过 `globalBucketMetadataSys.save` 让**缓存持有 Created=0**，再走 `Update` → `updateAndParseMetadata` → `ensureBucketMetadataCreated` → 真实 `erasureServerPools.GetBucketInfo`；`ExecObjectLayerAPITest`（`test-utils_test.go:1766/1795`）两个后端都是真实 `erasureServerPools`，因此缓存覆盖这一段是真跑到的。`setPhysicalBucketCreated` 直接 `os.Chtimes` 每块盘的桶目录，正是 `xl-storage.go:1022-1028` 取值的来源。六类配置 × `missing=true/false` 全覆盖。

### F2 —— 关闭，且口径现在六类全部自洽

`site-replication.go:3782-3809`。状态侧的 `policies[i]` 来自 `:3436` 对**各站点原始字节的解析**，与 heal 的 `bucketConfigPayload`→`canonicalBucketPolicy`（`bucket-metadata-replication.go:191`）是同一个纯函数，因此 peer 重排 Statement 后的永久假 mismatch 被彻底关闭。`numPolicies != total` 的 per-site presence 计数原样保留（`:3783-3792`），并有断言覆盖。

回归面逐条查过：

- **SID**：旧 `BPStatement.Equals`（silo-pkg `bucket-policy-statement.go:148-171`）**不比较 SID**，新键包含 `Sid`。这是方向正确的收紧——heal 的收敛键同样含 SID，所以状态报出的 mismatch 是**真实且会被 heal 自动消除**的，不是永久噪声。用例已钉住。
- **ID / Version**：旧 `Equals` 比较二者，新键在 `ID != ""` 时包含、`Version` 始终包含，等价。
- **Condition**：两侧都走同一 `condition.Functions` 编码再递归排序，顺序无关。
- **未知值**：`ParseBucketPolicyConfig` 用 `decoder.DisallowUnknownFields()`（`bucket-policy.go:173`），未知字段**根本无法通过解析**，因此不存在"键把未建模字段静默吃掉"的风险。
- `isIAMPolicyReplicated`（`:3665-3681`）仍用 `Equals`，那是 IAM 策略、不同子系统，**不应**一并改动。正确地没动。

新旧行为是"收紧而非放宽"，与收敛契约一致，判断合理。

### F3 —— 关闭了会吃日志的部分，但静默面仍偏大

已关闭：四种原因各自持 key（`site-replication-metadata.go:145/158/164/189`），真实 heal RPC 失败不再被"缺 peer"顶掉；`logger.WarningKind`（`:42`）；对端"尚未拥有该桶"（`CreatedAt=0` 且载荷空、时间零）不再报。`siteReplicationStatus` 对不可达 peer 填 `DeploymentID=""`（`:3029`）落进 `BucketStats[bucket][""]`，而循环 1 按 `info.Sites` 的真实 ID 判缺失、循环 2 跳过 `id==""`，两条路径正交，语义正确。README:112-119 与实现完全一致。

未关闭的两点见 NB-2、NB-3。另外，"仅报告确实存在、但仍无法排序的状态"（`:157`）这个条件写得准确：`!current.valid && (len(data)!=0 || !at.IsZero())` 恰好把 F9/F10 的世代冲突暴露出来，这是意外的正收益。

### F5 —— 上一轮的最小性判断需要修正

`canonicalBucketPolicy` 的六个生产接入点应分成三类，而不是笼统的"超出必要"：

| 接入点 | 性质 |
|---|---|
| `bucket-metadata-replication.go:191`（比较键） | **必需**，#77 的核心 |
| `site-replication.go:3802`（状态键） | **必需**，F2 的修复本体 |
| `bucket-policy-handlers.go:202`（GET）、`admin-bucket-handlers.go:438`（export）、`site-replication.go:1723`（peer apply） | **非可选**：这三处拿到的都是 `*policy.BucketPolicy`（`globalPolicySys.Get` / `GetBucketPolicy` / `admin-handlers-site-replication.go:228` 解析后传入），必须有编码器；而旧的 `json.Marshal` 对 `NotAction`/`NotResource` 语句**必然失败**——`BPStatement.Actions` 无 `omitempty`（`bucket-policy-statement.go:31`）+ `ActionSet.MarshalJSON` 空集报错（`actionset.go:144-148`）。bulk/import/peer 都能把这类策略落盘，所以缩回任何一处都会重新制造"写得进、读不出/复制不出" |
| `bucket-policy-handlers.go:102`（PUT）、`admin-bucket-handlers.go:922`（import） | **可选的规范化**：比较键不依赖落盘字节。但因为解析器 `DisallowUnknownFields`，规范化**无信息丢失**（唯一差别是重复语句在盘上也被去重，语义等价），而所有读路径都已重新编码，用户观察不到"原样字节"。删掉它只换来与客户端提交字节的一致性，且会让本地写与 peer 写产生两种盘上形态 |

**结论：不建议缩回任何一处。** 真正可删的只有 PUT/import 两处，收益为零、改动风险非零。上一轮把这部分列为"范围外扩张"的说法应当被本轮判断取代；它是一个被同一改动暴露出来的真实读回缺陷的修复，且已在 README:129-134 声明。

---

## 3. 新发现（全部非阻断）

### NB-1（S3）恢复出的物理 Created 不确定，且可能晚于对端事件的来源时间

- **位置**：`cmd/bucket-metadata-replication.go:277-290`；取值链 `cmd/peer-s3-client.go:324-328`（取第一个不报错的 peer）→ `cmd/peer-s3-server.go:207-258`（`cloneDrives` 遍历 **Go map**，取第一个不报错的盘）→ `cmd/xl-storage.go:1022-1028`（`Created = st.ModTime()`）。
- **最短触发**：桶无 `.metadata.bin`；其顶层目录 mtime 因近期对象写入被推进到接近 now；对端发来源时间早于该 mtime 的事件 → `bucket-metadata-sys.go:198-201` 判 `before-created` → **返回 nil、不写、不报错**，仅一条每小时去重的 warning，直到有人在源站重新提交。
- **影响**：(a) 恢复值不是桶的稳定属性（目录 mtime 随顶层条目增删前进，且逐盘不同）；(b) 两个站点各自独立恢复时会落进不同世代。二者都落在已声明的 F9 限制内，且相对基线（基线给每个站点各自 `UTCNow()`）是改善；本地侧因 `metadata.lock` 串行化 + 首写落盘，站内不会分裂。真正新的只有"首个跨站事件可能被静默跳过"。
- **最小修复（可选）**：让 `ensureBucketMetadataCreated` 返回"本次是否为恢复值"，当 `sourceTime` 非零且早于恢复值时取 `min(recovered, sourceTime)`——"在时刻 T 已持有配置的桶必然在 T 已存在"，约 4 行，不新增字段。或退一步：只在 README 里把恢复值明确为"物理近似值，可能晚于真实创建"。
- **需补证据**：一个用例——缓存 `Created=0`、`os.Chtimes` 把桶目录设为 now、通过 admin 路由投递 `UpdatedAt = now-1h` 的 peer 事件，断言期望行为（写入或明确跳过），把当前语义钉死。

### NB-2（S3）`!found` 时对畸形/不可排序状态完全静默

- **位置**：`cmd/site-replication-metadata.go:134-139`。
- **最短触发**：全部站点的同一字段都无法成为候选（例如各站点都存着一份 base64 可解但内容非法的 XML，或全部世代未知）。`latestBucketConfig`（`:117-120`）对 `err != nil` 静默 `continue`，`healBucketConfig` 直接 `return nil`——**没有任何诊断**。而状态导出侧的 `logInvalid`（`site-replication.go:3375-3378`）只覆盖 base64 失败和 policy/quota/replication 的解析失败，XML 类内容非法不在其中，于是这种状态在两条路径上都无声。
- **影响**：字段永久不可 heal 且无信号。范围窄（要求所有站点同时不可用），但属于"必要诊断"缺口。
- **最小修复**：把每目标的 `bucketConfigStateFromInfo` 结果算一遍并缓存（当前每周期每目标实际解码两次：`latestBucketConfig` 一次、主循环一次），把 `currentErr != nil` 的 `indeterminate` 移到 `if !found` 之前。既补诊断又减一次解码，不引入新框架。
- **需补证据**：`healBucketConfig` 单测——两个站点都给非法载荷，断言返回 nil 且产生一条 `indeterminate`（可用现有 `srStatusInfo` 夹具，参照 `site-replication-metadata-heal_test.go:136-143`）。

### NB-3（S4）`unreachable` 诊断按"桶 × 字段"扇出

- **位置**：`cmd/site-replication-metadata.go:143-147`，被 `cmd/site-replication.go:4750-4760` 每桶调用六次。
- **最短触发**：一个站点掉线。每桶产生 6 个不同 key → 10k 桶集群每小时约 6 万条 warning。
- **影响**：只是噪声（`logOnceIf` 按小时清理，仍算"有界"），但一个站点级事实被放大成桶×字段级输出。
- **最小修复**：把循环 1 上提到 `healBuckets` 里、每桶调用一次（key 去掉 `file` 段），或干脆每周期一次。改动 <15 行。
- **需补证据**：不需要新证据，行数/键数可直接核对。

### NB-4（S4，证据）两份旧日志是草稿测试产物，不能作为产品失败证据；设计记录"三个回归测试"应为两个

- **位置**：`findings-before.log:3`、`findings-after-1.log:3` 均为 `site-replication-metadata-gate_test.go:230: real physical creation time unavailable`。该消息在最终测试文件中**不存在**，最终断言在 **227** 行、消息是 `bucket without a recorded creation time cannot update %s: %v`。同理 `findings-before.log:52` 失败在 SID 断言（`:353`），而最终文件 SID 断言在 `:355`、`:353` 是 `t.Fatal(err)`。
- **判断**：这两份日志跑的是早期草稿；其中 "real physical creation time unavailable" 更像**测试自身的前置检查**失败，不能用来证明产品缺陷。真正成立的是 `round-2/confirmed-before.log`：它用**最终测试**叠加 `before-erasure-server-pool.go` / `before-site-replication.go`（我已核对这两份确实是修正前版本：无 `NoMetadata` 早返回、`MakeBucketHook` 无回落、`syncToAllPeers` 仍用 `bucketInfo.Created`、`isBktPolicyReplicated` 仍是 `prev.Equals`），行号 227/348 与最终文件精确吻合，失败文本是真实产品错误 `bucket metadata creation time is unknown` 与 `equivalent legacy and received policy reported as permanently mismatched`，两后端 × 六类 × missing 双态全覆盖；`confirmed-after.log` 通过。
- **另一项**：设计记录第 166 行"三个回归测试在未修复代码上失败、修复后通过"不准确。仓库中只有**两个**这样的测试；F3（heal 诊断）**没有任何自动化回归**（我在全部 `*_test.go` 中检索 `unreachable`/`peer-error`/`unusable peer`/`logBucketConfigReplication`，无命中）。F3 的唯一证据是 `runtime-after-review.log:20` 的驱动自述"40 exceptional events produced exactly one log per reason"。
- **最小修复**：把该行改为"两个回归测试 + 一次运行时观察"，并在证据索引里把 `findings-before.log` / `findings-after-1.log` 标注为 superseded 草稿。
- **需补证据（可选）**：给 F3 补一个日志断言用例（注入 logger target 或计数 hook），否则 F3 的修复在回归套件里是无保护的。

### NB-5（S4）不要为死代码创造兼容理由

`isBucketMetadataEqual`（`cmd/site-replication.go:5172-5181`）仅被 `site-replication_test.go:156`、`bucket-cors-site-replication_test.go:484` 引用。代码里没有编造理由（很好），但设计记录第 186 行写"作为上游血缘保留"是一个不成立的说法。**最小修复**：改成"目前仅测试引用，暂不删除以缩小 diff"，或连同两处测试一并删除。

### NB-6（S4）README 对恢复发生位置的描述偏保守

`docs/site-replication/README.md:106-110` 说"That recovery happens on the write path … 第一次配置写入（本地或复制而来）会记录物理时间"。实际上 `AddPeerClusters` 的初次同步（`site-replication.go:2145` → `MakeBucketHook` → 本地 `PeerBucketMakeWithVersioningHandler`）也会在本地落盘该时间。这是**低估**不是夸大，但会误导运维判断"何时脱离跳过状态"。**最小修复**：补半句"或在把该桶纳入站点复制时"。

### NB-7（S4）F10 只钉住了"来源"半边

`cmd/site-replication-metadata_test.go:473-483` 断言接管把 Created 调晚后，越过的真实字段**不能成为来源**（`!state.candidate()`）。但上一轮指出的另一半——该字段作为 **heal 目标**时，`current.valid=false` 使任何合法 incoming 无条件胜出（`site-replication-metadata.go:174` + `bucket-metadata-replication.go:239-245`）——没有钉住。我确认这半边行为存在且在"远端世代胜出"语义下是自洽的，本地用户 PUT 也总能覆盖回来（`localBucketConfigUpdatedAt` 保证 `> Created`），**不是缺陷**，但期望值应当被固定。**最小修复**：同一子用例再加一次 `applyBucketConfig(&got, file, <远端载荷>, created+shift+1m)`，断言返回 `changed=true`。

### NB-8（S4）恢复失败时的诊断丢掉了事件来源时间

`cmd/bucket-metadata-sys.go:187` 传 `time.Time{}` 作为 `at`，即使本次是携带 `sourceTime` 的 peer 事件，日志里也显示 `0001-01-01`。**最小修复**：`sourceTime != nil` 时传 `*sourceTime`（一行）。

---

## 4. 证据分级

| 证据 | 评价 |
|---|---|
| `round-2/confirmed-before.log` + `confirmed-after.log` + `before-*.go` 覆盖文件 | **足够**。最终测试 × 旧生产代码，行号与失败文本可逐条核对，F1/F2 为已证实的产品失败 |
| `binary-identity-after-review.json` | **足够**。`silo-final --version` 含 `commit-id=62cf066ff…`，并附基线 `5c5765816` 对照二进制与双方 sha256，上一轮 F8-4 的缺口已闭合 |
| `final-cmd-after-review.log`（cmd 全包 622s, EXIT=0）、`ci-lint-after.log`（0 issues, 品牌基线未变）、`ci-verify-after.log`、`ci-gen/internal/s3select/crosscompile-after.log` | **足够**作为"未引入回归"的门禁证据（只截尾行，可接受） |
| `findings-before.log`、`findings-after-1.log` | **不可用**。草稿测试产物，行号与失败文本均与最终测试不符（见 NB-4） |
| `findings-after.log` | 仅"ok"，无信息量，被 `confirmed-after.log` 覆盖 |
| `runtime-after-review.log` | **作者自述为主**。日志由作者的双站点驱动产生，断言（"six field states equal""zero metadata RPCs""exactly one log per reason"）都由驱动自己判定。时间戳 06:01:50 与最终二进制构建时间 06-01-35Z 吻合，身份链成立；但它是端到端观察，不是独立验证。F3 目前**只有**这一条证据 |

---

## 5. 设计记录（`bucket-metadata-convergence.zh.md`）的准确性

总体克制，未发现把限制说成已修复的地方：§rollout（gate=off 不承诺四类删除收敛）、§limits-1/2/3（历史污染、零时间事件、桶世代冲突）都如实写成限制；第 20 行明确"不能用来证明现有下载包或线上实例已具备这些能力"；第 161 行主动声明交叉编译 ≠ 六平台运行验收。需要修正的只有三处，均已在上面列出：

1. 第 166 行"三个回归测试" → 两个回归测试 + 一次运行时观察（NB-4）。
2. 第 186 行 `isBucketMetadataEqual`"上游血缘保留"是编造的理由（NB-5）。
3. 第 181 行"与 `ListBuckets` 一致"成立（`ListBuckets` 在 `NoMetadata` 时同样不覆盖 Created），但第 185 行把 F5 记为"有意扩张，不是缺陷"仍沿用了上一轮的定性——按本轮判断，GET/export/peer 三处属于**必需**，建议改写，否则会给未来的人留下"这块可以缩回"的错误暗示。

---

## 6. 最小性 / 充分性 / 必要性（独立评价）

**最小性：是。** 本轮修正净增很小，且每一处都能对应到一个已复现的失败或一次被证实的误读：`NoMetadata` 早返回 5 行、`MakeBucketHook` 回落 3 行、`syncToAllPeers` 恢复 4 行、`isBktPolicyReplicated` 换键约 10 行、heal 诊断重排约 15 行、`isReplicatedBucketConfig` 具名化（消除"用一次性零值结构体探测类型"的晦涩写法）、README 两段。没有新 schema、新 wire 字段、新锁、新框架。我找不到可以无损删除的部分——唯一"可删"的 `canonicalBucketPolicy` PUT/import 接入是上一轮遗留，删掉反而制造两种盘上形态。

**充分性：对已声明契约充分，对两个边缘不充分且已如实声明。** 在"同一桶世代、Created 已知或可恢复、gate=on"范围内，六类配置的来源时间、锁范围、删除参与 heal、等时裁决、重复/乱序抑制、状态口径，我没有找到反例。不充分的两处都写进了文档：gate=off 下 Tags/SSE/Quota 漏发删除不收敛（F7）；跨站点桶世代分歧不自动合并（F9，NB-1 属于它的子集）。F3 的静默面（NB-2）是本轮唯一一个"文档声明了、但我认为声明本身偏宽"的地方——"没有任何站点可供传播的字段就不产生诊断"对畸形配置而言不该成立。

**必要性：是。** F1 与 F2 都有"最终测试 + 修正前生产代码"的复现（`confirmed-before.log`），是确证的产品失败，不是理论推演；F3 的必要性来自 `logOnceIf` 按 key+正文去重这一可验证机制，虽无自动化回归但代码级推理成立；F4/F8/F10 是文档与证据补齐，成本近零。F5 的编码器接入经本轮重新论证后，从"可争议扩张"变为"其中三处必需、两处无损"，必要性判断上调。

**处置建议（均非阻断，可合并后处理）**：NB-4 与 NB-5 是文字修正，建议合并前一并改掉（它们会影响后人对证据强度的判断）；NB-2、NB-8 可合成一个约 20 行的提交；NB-1、NB-3、NB-6、NB-7 可进 backlog。
