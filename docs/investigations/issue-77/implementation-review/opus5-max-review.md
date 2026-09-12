# SILO #77 实现深度对抗性代码审查报告

审查对象 `4089113e3`（基线 `5c5765816`）。只读 worktree，全部结论来自直接阅读 `cmd/` 生产代码、`silo-pkg`/`madmin-go` 依赖源码与调用链，并与证据目录交叉核对。测试 PASS 仅当作"已覆盖的观察"，不作正确性证明。

> 说明：本会话的 Write/ExitPlanMode 工具不可用，报告直接输出于此，未落盘。

---

## 0. Verdict

**GO_WITH_NONBLOCKING_NOTES**

- **无条件阻断项：0**
- **条件性阻断项：1**（F1）——条件：支持矩阵中存在 `.metadata.bin` 缺失或 `Created == 0` 的桶
- 其余：F2 (S2)、F3 (S3)、F4 (S3)、F5（最小性）、F6（nit）、F7（范围说明）、F8（证据不足）、F9/F10（残留边界与覆盖缺口）

我逐条读了六类配置在全部入口的生产路径，**没有**找到在"同一桶世代、Created 已知、合法带源时间事件"范围内会发生回退、删除复活、锁外判旧或重复广播的反例。核心机制是正确的。缺陷集中在两处边缘：零 Created 历史桶被新代码判为不可写（F1，且其自带测试用 stub 掩盖了生产行为），以及 Policy 规范编码排序与既有公开 mismatch 统计口径冲突（F2）。

---

## 1. Findings

### F1 — 零 Created / 无 metadata.bin 的桶：六类配置写入全部硬失败；设计中的"物理桶 Created 补齐"在生产中不可达；配套测试用 stub 掩盖

**严重度 S1（条件性阻断）/ 否则 S2 · 已证实（源码级链路完整），需运行定量确认**

位置：
- `cmd/bucket-metadata-replication.go:265-278` `ensureBucketMetadataCreated`
- `cmd/bucket-metadata-sys.go:181-185`（六类分支入口，失败即 `return err`）
- `cmd/erasure-server-pool.go:2280-2285` —— **`GetBucketInfo` 无条件用缓存 `meta.Created` 覆盖物理卷 Created**
- `cmd/admin-bucket-handlers.go:1092`（import）、`cmd/site-replication.go:1671`（bulk peer apply）
- `cmd/site-replication.go:2156-2166` + `cmd/site-replication-metadata.go:45-55`（初次同步静默跳过）
- `cmd/site-replication-metadata-gate_test.go:145-159`（stub）

最短触发链：
1. 桶存在，但 `.minio.sys/buckets/<b>/.metadata.bin` 不存在且无 legacy 配置文件 → `loadBucketMetadataParse` 走 `errConfigNotFound` 分支并**返回 nil error 且 `Created == 0`**（`cmd/bucket-metadata.go:230-297`；注意 `defaultTimestamps()` 只在 `err == nil` 时调用）。
2. 启动时 `concurrentLoad`（`cmd/bucket-metadata-sys.go:737-763`）把这个 `Created == 0` 的 meta 写进 `metadataMap`。
3. `PUT ?tagging`（或 policy/sse/quota/versioning/object-lock）→ `updateAndParseMetadata` → `ensureBucketMetadataCreated` → `objAPI.GetBucketInfo(..., NoMetadata:true)` → `erasureServerPools.GetBucketInfo` 先取到物理卷时间，**随后被缓存里的 0 覆盖** → `info.Created.IsZero()` → `errors.New("bucket metadata creation time is unknown")`。
4. 客户端收到错误。基线版本此处成功（直接赋值 + `saveMetadata`）。

注：`getAllLegacyConfigs` 在 `cmd/bucket-metadata.go:468` 会用 `info.ModTime` 填 `Created`，所以**有** legacy 配置文件的桶反而没事；**从未设置过任何桶配置**的老桶才是高发场景。

用户影响：
- 六类配置 PUT/DELETE 全部返回错误，**无运维恢复路径**；`mc admin bucket import` 对该桶整体失败（`rpt.SetStatus(bucket, "", err)`）；peer bulk apply 返回错误导致该桶复制持续报错。
- `syncToAllPeers` 对这类桶**静默跳过全部五类配置**（`state.candidate()` 要求 `valid`，而 `valid = !created.IsZero() && ...`），且**无任何诊断日志** —— 基线版本会发送。

最小修复（推荐 a）：
- **(a)** `cmd/erasure-server-pool.go:2282` 改为 `if !meta.Created.IsZero() { bucketInfo.Created = meta.Created }`。一行；恢复计划中"物理桶 Created 补齐路径"的本意；顺带修掉"无 metadata.bin 的桶在 `mc ls` 里创建时间为零"这一既有瑕疵。需复核 `bucketExists`、`hasBucket = !bi.CreatedAt.IsZero()` 等消费点 —— Created 由零变非零对它们都是变好。
- **(b)** 另外在 `syncToAllPeers`（`cmd/site-replication.go:2138-2156`）把已在手的 `bucketInfo.Created` 兜底进 `meta.Created`，或在"有内容但 send==false"时打一条 indeterminate，避免静默漏发。

应补验证：
- 把 `TestPeerBucketMetadataUnknownCreated` 的 `"physical-created"` 子用例**去掉 stub**：用真实 ObjectLayer，把 `newBucketMetadata(bucket)`（Created 为零）存盘并让缓存持有它，断言 `globalBucketMetadataSys.Update(ctx, bucket, bucketTaggingConfig, tagXML)` 成功且 `Created` 被补齐为物理时间。**当前实现会失败。**
- 一个 `syncToAllPeers` 用例：`Created == 0` 且 Tags 非空的桶，断言初次同步仍发 Tags 事件（或至少有诊断）。

---

### F2 — Policy 规范编码对 `Statement` 数组排序，与既有公开统计的顺序敏感 `Equals` 冲突：永久假 mismatch，heal 永不修复

**严重度 S2（公开状态/可观测性，非数据损失）· 已证实（源码级），需运行确认**

位置：
- `cmd/bucket-metadata-replication.go:110` —— `sort.Slice` 作用于**每个**数组，含顶层 `Statement`
- `cmd/bucket-metadata-replication.go:124-161` `canonicalBucketPolicy`
- `cmd/site-replication.go:3796` `isBktPolicyReplicated` → `prev.Equals(*p)`（`silo-pkg .../policy/bucket-policy.go:190-194` 按 `Statements[i]` **下标**逐一比较，顺序敏感）
- `cmd/site-replication-metadata.go:174` vs `:179` —— 本地 heal 写**源站原始字节**，远端 heal 经 `PeerBucketPolicyHandler` → `canonicalBucketPolicy` 写**规范字节**

最短反例：
1. 站点 A 存在升级前写入的桶策略，statement 顺序不是规范字节序（极常见，例如 Deny 在 Allow 之前）。
2. 加入新站点 B：`syncToAllPeers` → `initialBucketConfigReplicationEvent` 发送 **A 的原始字节**，`UpdatedAt = A.PolicyConfigUpdatedAt`。
3. B 的 admin 入口解析后调 `PeerBucketPolicyHandler` → `canonicalBucketPolicy` → 存**排序后**字节。
4. 此后 A/B 的比较键（canonical）完全相同、时间相同 → `compareBucketConfigStates == 0` → heal 永不写、永不发 RPC；而 `Equals` 因 statement 顺序不同返回 false → `mc admin replicate status` **永久**报 bucket policy mismatch，`ReplicatedBucketPolicies` 少计。

同机制第二条路径：同一轮 heal 中本地目标拿原始字节、远端目标拿规范字节，三站点集群会出现持久分叉。

为何是新问题：基线 `PeerBucketPolicyHandler` 用 `json.Marshal(policy)`，集合（Action/Resource/Principal）顺序随机但 **statement 切片顺序被 marshal/unmarshal 保持**，`Equals` 一直成立。

最小修复（三选一，推荐 a 或 b）：
- (a) `canonicalBucketPolicyJSON` 不对顶层 `Statement` 数组排序（只排集合数组）——顺序在全链路被保留，两站点独立解析同一文档仍得相同键。
- (b) `isBktPolicyReplicated` 改用 `canonicalBucketPolicy` 的键比较，而不是 `Equals`。
- (c) `healBucketConfig` 本地分支改写 `incoming.data`（与远端同一编码）——只修本地/远端分叉，**不**修 A 与新站点的分叉。

应补验证：双站点用例——A 侧直接把 legacy 顺序的策略字节写盘（绕过 canonical 编码器），join B，跑两轮 heal，断言 `SiteReplicationStatus` 不报 policy mismatch。

---

### F3 — heal 诊断以 ERROR 级、按"桶 × 字段"输出；正常瞬态也报警，且单一 key 会吞掉真实 RPC 失败

**严重度 S3 · 已证实**

位置：`cmd/site-replication-metadata.go:134-145`（两个诊断循环）、`:182-186`（peer 错误）、`cmd/logging.go:19-21`（`replLogOnceIf` 无 errKind ⇒ **ErrorKind**）

1. 第二个循环对 `!state.valid` 的目标打 `indeterminate / unusable peer`。`valid` 要求对端 `CreatedAt != 0` —— 而**对端还没有这个桶**时 `CreatedAt` 就是 0（`cmd/site-replication.go:3096-3103` 给缺桶站点填零值 `SRBucketInfo`）。于是"桶刚建、尚未传播"这种完全正常的瞬态，对每个桶产生 6 条 **ERROR**。
2. 第一个循环在对端 `RemoteTargetConnectionErr`（`cmd/site-replication.go:3020-3026` 填空 ID）时，对**每个本地桶 × 6 字段**打 `missing peer`。一个站点掉线 ≈ 每小时 6×桶数 条 ERROR。
3. 四种完全不同的情况（缺 peer / peer 不可用 / peer RPC 失败 / Created 未知）共用同一个 key `"bucket-metadata/<bucket>/<file>/indeterminate"` **且错误正文相同**，而 `logOnceIf` 只按 key+正文去重（`internal/logger/logonce.go:100-119`）。结果：**真正的 heal RPC 失败可能被同桶同字段的"缺 peer"消息顶掉而完全不打印**；基线版本这些失败走 `replLogIf`，必然打印。

最小修复：`target.CreatedAt.IsZero()`（桶不在对端）与 `found == false` 时不打诊断；把 4 种情况拆成不同 reason（`unreachable`/`peer-error`/`unknown-created`）避免 key 撞车；给 `logBucketConfigReplication` 传 `logger.WarningKind`。

---

### F4 — 三处用户可见语义变更未写入文档

**严重度 S3（文档/兼容）· 已证实（逐条比对 `docs/site-replication/README.md:65-118`）**

1. **空 Bucket Policy PUT 现在立即按删除处理**：`cmd/bucket-policy-handlers.go:102` + `cmd/bucket-metadata-replication.go:176-180` → `canonicalBucketPolicy` 对 `IsEmpty()` 返回 nil → 落盘 nil → `GetBucketPolicy` 从"200 + 空策略文档"变为 **404 NotFound**。计划 §1 明确写了"需要写入兼容说明"，文档里没有。
2. **零 Quota (`{}`) 在对端从"被删除"变为"保留 live 文档"**：`cmd/admin-bucket-handlers.go:79-96` 删除了出站 `bucketMeta.Quota = nil` 改写。这是正确的对齐（本地原本就保留 `{}`），但对端 `GetBucketQuotaConfig` 的结果会变。
3. **`GET ?policy` 与 `mc admin bucket export` 的 JSON 形态改变**：`cmd/bucket-policy-handlers.go:202`、`cmd/admin-bucket-handlers.go:439` 改用 `canonicalBucketPolicy` → 对象键按字母序、集合数组与 **Statement 数组被排序**。语义等价（S3 策略求值与 statement 顺序无关），但字节级比对输出的工具会看到变化。

---

### F5 — 最小性：Policy 规范编码器接入 PUT / GET / export / peer 落盘，并非 #77 不变量所必需

**非缺陷，最小性判断 · 已证实**

比较键在 `bucketConfigPayload`（`cmd/bucket-metadata-replication.go:172-180`）里**从已解析策略现算**，与落盘字节无关。即使 PUT/peer 仍用 `json.Marshal`（字节不确定），两站点的键依旧相同，收敛性完全不受影响。

因此这部分改动带来的是两件**额外**的事：(a) 让原本被 `ActionSet.MarshalJSON`（`silo-pkg .../policy/actionset.go:144-148`，空集合报错）挡掉的 `NotAction`/`NotResource` 策略首次可以写入 —— 这是**功能新增**；(b) 直接导致 F2。

判断：如果作者**有意**支持负集合策略，应作为独立特性声明并单独记录/测试（目前只有 `site-replication-metadata_test.go:530-541` 一个用例）；如果只为 #77，最小做法是 `canonicalBucketPolicy` 只用于比较键。我**不**主张必须删除——它确实修掉了"能存不能读"的潜在坑——但必须承认这是范围外的行为扩张，且未在文档中声明。

其它可删复杂度（都很小，不影响不变量）：
- `cmd/bucket-metadata-sys.go:151` 在零值结构体上用 `replicatedBucketConfig(&result.meta, configFile)` 做"是不是这六类"的判断，语义晦涩；一个 `isReplicatedBucketConfig(file) bool` 更清楚。
- 修掉 F3 后，`healBucketConfig` 的第二个诊断循环（`:139-145`）可并入主循环。

---

### F6 — Nit

- `isBucketMetadataEqual`（`cmd/site-replication.go:5164`）现在只被测试引用，生产死代码。
- `cmd/bucket-metadata-replication.go:36-38` 注释"Object Lock is applied before Versioning"对 bulk apply 循环成立，但 `healBuckets`（`cmd/site-replication.go:4745-4746`）先 heal Versioning 再 heal Object Lock。我推演过仍收敛（最多 2 个周期、无写入环），但注释与 heal 顺序不一致，建议补一句。

---

### F7 — 范围说明（非缺陷，但必须进入决策）

**默认 `MINIO_SITE_REPLICATION_METADATA_TOMBSTONES=off` 时，Tags / SSE / Quota 的"漏发删除"不会通过 heal 收敛。** 只有 Policy 墓碑默认导出（`cmd/site-replication.go:3953-3954` 无条件导出；另外三类在 `:3960 / :3980 / :3988` 被 gate 挡住）。

我完整推演了 off 模式，结论与文档一致：本地真实墓碑在锁内比较时**不会**被旧 PUT 复活（`applyBucketConfig` 用真实落盘状态比较）；但持有旧数据的对端会每 30 秒发一次过期 RPC 被拒绝，**状态永不收敛**，直到运维在权威站点重新提交删除，或全站点升级后统一开启开关。

也就是说：**默认配置下交付的是"顺序正确 + 不复活 + Policy 删除可 heal"，不是"四类删除都能自愈"。** 这是 v4 计划的既定取舍（计划 §提交 3 表格），实现与文档都如实写了；我在此只是确保批准时看到这一点。

---

### F8 — 证据不足项

1. **F2 对双站点实验不可见**：`twosite/main.go:141-144` 的所有策略都经 `SRPeerReplicateBucketMeta` → `canonicalBucketPolicy` 写入，两侧都是规范字节，`states()`（`:160-164`）的字节比较自然通过。**从未构造过"升级前旧编码字节"的策略。**
2. **F1 被 stub 掩盖**：`cmd/site-replication-metadata-gate_test.go:151-159` 自定义 `GetBucketInfo`，在 `opts.NoMetadata` 时直接返回物理 Created，绕过了 `erasureServerPools.GetBucketInfo` 的缓存覆盖。`"physical-created"` 子用例证明的是一个**生产中不会发生**的行为。
3. **"稳态 0 metadata RPC"的边界**：`twosite/main.go:182-190` 只统计 `event.Bucket == bucket` 的单桶、双站点、**gate=on**。结论有效但窄；gate=off 的稳态不为零（文档已声明），多桶/多站点未观测。
4. **二进制身份**：`manifest.json` 给了 `silo-final` 的 sha256，但目录里没有 `--version`/build-info 输出（驱动用 `--quiet --json` 启动），`runtime-final.log` 也没有版本行。验收自述"编译信息为 `c8f264f79 + dirty`"是**诚实的**，且日志里的 gate 行为（`gate=on/off`、`off exporter exposed new tombstone`）只能来自第三个提交的生产代码，所以**没有夸大**；但也**不能从证据目录内独立复核**。补一份 `silo-final --version` 或 `go build` 复现即可闭环。
5. **baseline.log 行号与最终测试文件不一致**（日志 93/105/117 vs 现文件 96/97/109/121）：说明"修复前复现"跑的是测试文件的早期版本。可接受，但严格讲不是同一份用例。

---

### F9 — 残留边界：桶世代冲突（计划内已声明，非本轮回归）

各站点 `Created` 不同时，A 的 baseline-live（`at == A.Created`）在 B 上会被重算为 `real`（`newBucketConfigState` 用**目标**的 `created` 判定，`cmd/bucket-metadata-replication.go:215-219`），可能压过 B 的真实墓碑。计划明确排除在收敛承诺外，且 `AddPeerClusters`（`cmd/site-replication.go:458-466`，"only one cluster may have data"）封死了最常见入口。剩余入口：分区期间两站点各自建同名桶，或 #78 接管。基线版本在同场景下是**不确定**的（seed 首个 map 项），所以不算回归。

---

### F10 — 覆盖缺口：接管时 `Created` 前移越过真实修改时间

`rebaseBucketConfigDefaults`（`cmd/bucket-metadata-replication.go:306-318`）只调整"零/等于旧 Created"的默认时间。若 `opts.CreatedAt` 晚于某个**真实**字段时间，该字段变成 `at < Created` ⇒ `valid == false`：作为 heal 源 `candidate()` 为 false；作为 heal 目标 `incoming.valid > current.valid` ⇒ **必被覆盖**，本地真实配置被丢弃。

在"远端世代胜出"的语义下可以论证这是对的，但 `TestPeerBucketAdoptionRebasesOnlyDefaults`（`cmd/site-replication-metadata_test.go:394-425`）用的 shift 是 ±1h 而真实时间在 `created+2h`，**恰好没有覆盖这一情形**。建议加一个 `shift = +3h` 的子用例，把期望行为固定下来。

---

## 2. 六类配置 × 各入口 覆盖判断

| 配置 | 本地写 | typed peer | bulk | import | initial sync | heal | 接管 | 结论 |
|---|---|---|---|---|---|---|---|---|
| Policy | ✅ `bucket-policy-handlers.go:108/152`，hook 用 `result.meta`+`result.updatedAt` | ✅ `site-replication.go:1716-1730` | ✅ `len(item.Policy)!=0` 判"已提供" | ✅ 共同 `commitAt` + 空策略另发专用 nil 事件（`admin-bucket-handlers.go:1150-1155`） | ✅ gate 控墓碑 | ✅ | ✅ rebase | 通过（F2/F4 为附带） |
| Tags | ✅ `bucket-handlers.go:1940/2021` | ✅ `:1733-1747` | ✅ `*string != nil` | ✅ | ✅ gate | ✅ 补齐 `UpdatedAt`（旧版 heal 缺此字段） | ✅ | 通过 |
| SSE | ✅ `bucket-encryption-handlers.go:106/199` | ✅ `:1787-1801` | ✅ | ✅ | ✅ gate | ✅ | ✅ | 通过 |
| Quota | ✅ `admin-bucket-handlers.go:85-99`（删掉零值改写） | ✅ `:2032-2047` | ✅ `len(item.Quota)!=0` | ✅ | ✅ gate | ✅ 含缓存清除（`parse=false` 修复） | ✅ | 通过 |
| Versioning | ✅ `bucket-versioning-handler.go:100-116`，广播用归一后的 `result.meta` | ✅ 空=no-op | ✅ 空=no-op | ✅ 归一后落盘并广播 | ⛔ 不发（由 MakeBucketHook bootstrap + heal 对齐，与基线一致） | ✅ 按**目标** Lock 状态归一后比较 | ✅ `enablePeerBucketVersioning` 用 `localBucketConfigUpdatedAt` | 通过 |
| Object Lock | ✅ `bucket-handlers.go:1841-1852` | ✅ 空=no-op，保留 `item.Tags` legacy 回退 | ✅ 空=no-op | ✅ | ✅ | ✅ | ✅ | 通过 |

逐条核对结论：

- **来源时间不会变成本地 now**：六个 typed handler 全部删除了锁外 `GetXConfig()` 判旧，统一走 `updateAndParseMetadata(..., &updatedAt)`；非零 `sourceTime` 直接 `sourceTime.UTC()`（`bucket-metadata-sys.go:191-193`）。只有 `sourceTime == nil`（本地写）或为零（legacy 兼容）才分配本地单调时间。✔
- **本地写严格单调**：`localBucketConfigUpdatedAt`（`:255-263`）保证 `> Created` 且 `> 当前字段时间`，涵盖"已有未来时间"。✔
- **重复/乱序不保存不广播**：`applyBucketConfig` 在 `compare <= 0` 返回 `changed=false`，据此跳过 `saveMetadata` 与 `LoadBucketMetadata`；bulk 用 `changed` 汇总后一次保存。✔
  我特别核对了所有使用 `result.meta`/`result.updatedAt` 的本地 handler：在这些路径上 `changed` 恒为 true（`localBucketConfigUpdatedAt` 使 `incoming.at` 严格更大，且 `incoming.candidate()` 恒真），所以**不会**出现"零时间 + 空载荷"被当成删除广播出去。这是一个隐式依赖，建议加一行注释或断言固定住。
- **整桶 `.metadata.bin` 读-比较-写全在既有分布式锁内**：`updateAndParseMetadata`、`PeerBucketMetadataUpdateHandler`、import 最终提交、`PeerBucketMakeWithVersioningHandler`、CORS 路径，我逐个确认锁的获取在读之前、释放在 `saveMetadata` 之后、fan-out 在释放之后。✔
- **无关字段 / CORS / lifecycle 不被覆盖**：所有写路径都是锁内**重新加载**后只改目标字段；import 用 `applyImportedBucketMetadata` 只按 `fields` 拷贝并 `bytes.Clone`。✔
- **缓存快照正确**：`saveMetadata` 改收 `*BucketMetadata`，`meta.Save` 内部 `parseAllConfigs` 回写归一化结果，`sys.Set(name, *meta)` 发布的是提交后快照；调用方拿到的 `result.meta` 是同一份值拷贝，不会原地修改已发布引用。✔
- **`parse` 被强制为 false 对缓存无害**：`Save()` 先 `parseAllConfigs`，发布到 `metadataMap` 的解析字段是新的；而"删除后 quota 解析残留"正是靠 `parse=false` 的新加载对象修掉的（`parseAllConfigs` 对空 `QuotaConfigJSON` **不会**把 `quotaConfig` 置 nil，`bucket-metadata.go:400-405`）。✔
- **锁/返回值/错误处理**：`unlock()` + `locked=false` 模式保留；`updateAndParse`、`Update`、`Delete` 对外签名不变。✔

---

## 3. 六字段 state 比较：传递性、交换序无关、幂等收敛

`compareBucketConfigStates`（`bucket-metadata-replication.go:227-253`）实际是按 `(valid, real, real?at:—, real?isTombstone:—, key)` 的字典序全序。

- **传递 / 反对称**：是。`real == false` 时不比较 `at`（只比 key），但空 baseline 的 key 为 nil，`bytes.Compare` 使其恒最小，效果等同"空 baseline 永不获胜"。✔
- **交换顺序无关**：`latestBucketConfig` 取全序最大值，与 map 遍历顺序无关；`applyBucketConfig` 同样只依赖全序。`TestLatestBucketConfigCandidates` 跑了全部 6 种排列。✔
- **幂等收敛**：写入后目标状态等于源状态 ⇒ 下一轮 `compare == 0` ⇒ 不写不发。我另外手工推演了 Object Lock/Versioning 的"Enabled vs Suspended 同时间"场景，归一后双侧都不再写，稳定无振荡。✔
- **baseline Created 与真修改的区分**：`real = valid && at.After(created)`，零字段时间先回填为 `created`（与 `defaultTimestamps()` 一致）。✔
- **同时间删除胜出**：`:244-250`，仅在 `a.real` 分支内生效，空 baseline 不会借此删配置。✔
- **等时不同载荷的稳定键**：
  - **Policy**：递归排序的规范 JSON + `json.RawMessage` 保留整数精度（测试 `:308-311` 验证 `9007199254740993` 不失真）。我对照了 `silo-pkg .../policy/bucket-policy-statement.go:27-36`，`BPStatement` 的 8 个字段（SID/Effect/Principal/Actions/NotActions/Resources/NotResources/Conditions）**全部覆盖，无字段静默丢失**；`ParseBucketPolicyConfig` 先做 `Validate` 保证 `Principal.MarshalJSON` 不会失败。✔ 但顶层 Statement 排序引出 F2。
  - **Quota**：`json.Marshal(parseBucketQuota(...))`。零 quota 仍是 live 文档（`candidate()` 靠 `real`，不靠 `len(data)`）。`{}` 与对端重编码字节不同但键相同，不产生 heal 环；`isBktQuotaCfgReplicated` 按解析值比较，不会误报。✔
  - **Tags / SSE / Object Lock**：有效文档字节，保留大小写与实际内容。✔
  - **Versioning**：先按**该站点**的 Object Lock 归一（`effectiveBucketVersioning`），键即归一后的文档，避免"保存阶段隐式改写"造成的空转。✔
- **空策略**：`bucketConfigPayload` 对 `cfg.IsEmpty()` 返回 `(nil, nil, nil)`，与 peer 侧既有"空策略=删除"解释统一。✔（副作用见 F4-1）
- **Object Lock 强制 Versioning 后的持久化 vs 广播**：本地只广播 Object Lock 事件，派生的 Versioning 由对端在 `Save → parseAllConfigs` 自行推导，**内容一致、时间各自保留**；heal 用归一后的有效文档比较，最多一次写入即收敛。与计划一致。✔

---

## 4. Created / 时间的异常输入

| 输入 | 行为 | 判断 |
|---|---|---|
| Created 为零 / metadata.bin 缺失 | 六类写入报错；初次同步静默跳过 | **F1，回归** |
| 字段时间为零 | 回填为 Created ⇒ baseline | ✔ 与 `defaultTimestamps()` 一致 |
| 事件时间 == Created | 可更新仍为 baseline 的字段，时间保存为 Created；同时间 nil/空只算空 baseline，不删除 | ✔ 有用例 |
| 事件时间 < Created | `before-created`，跳过 + 单条日志；heal 对该目标 `continue` | ✔ |
| 未来时间 | 接受为真实状态；后续本地写用 `max(now, at+1ns)` 压过 | ✔ 有用例 |
| 各站点 Created 不同 | 见 F9（计划外，已声明） | 残留 |
| 空 deployment ID | `latestBucketConfig` / `healBucketConfig` 均 `id == "" \|\| !known` 跳过 | ✔ 有用例 |
| peer 报错 / 缺元数据 | `candidate()` 为 false ⇒ 不参与选源；作为目标 `CreatedAt.IsZero()` ⇒ `continue`。**不会被当成删除** | ✔ |
| 单个 peer 不可达 | 记录后继续其它目标（`site-replication-metadata.go:182-186`），不因 map 顺序放弃健康站点 | ✔ 有用例（`"broken"`） |

**"已知历史不可恢复"与"新实现回归"的区分**：
- 历史不可恢复（可接受）：旧版到达时间污染、legacy-zero 事件产生的新本地时间、桶世代分歧。
- 新实现回归（应修）：F1 的零 Created 硬失败与初次同步静默漏发；F2 的永久假 mismatch；F3 的 ERROR 噪声与日志互相顶替。

---

## 5. 兼容路径与开关

- **typed 零时间兼容**：`sourceTime != nil && IsZero()` ⇒ 分配本地时间 + 一条 `legacy-zero`（`bucket-metadata-sys.go:186-190`）。不受 gate 影响，不在源时间排序保证内。✔
- **bulk 零时间拒绝**：`PeerBucketMetadataUpdateHandler:1610-1612` ⇒ `errInvalidArgument`，与现状一致；import hook 恒带非零 `commitAt`。✔
- **省略 vs 显式 null / 空串**：`madmin.SRBucketMeta` 全字段 `omitempty`（`madmin-go v3.0.110 cluster-commands.go:501-538`），因此
  - `json.RawMessage`（Policy/Quota）：省略 ⇒ nil；显式 `null` ⇒ `[]byte("null")`，走 `len(...)!=0` 判为"已提供"，再交既有解析器（Policy `null` ⇒ 语义空 ⇒ 删除；Quota `null` ⇒ 零值文档 ⇒ live）。✔ 与计划 §1 完全一致。
  - `*string`：省略/`null` ⇒ nil ⇒ 不动；空串 ⇒ 已提供且内容为空 ⇒ 四类删除、两类 no-op。✔
  - 这些由 `TestPeerBucketMetadataWireAtomicity` 通过**真实 admin 路由 + 真实 JSON 编解码**覆盖（`applySRBucketMetaViaAdmin` 走 `registerAdminRouter`），不是结构体直传。这是这批测试里质量最高的一块。
- **direct hook 与 heal 的删除传播**：普通 DELETE 事件在 gate off 下照常复制；heal 依赖导出可见性 ⇒ 见 F7。
- **初次同步**：保留原五类范围（不含 Versioning），gate 控制是否发送真实墓碑。✔
- **gate 覆盖点是否遗漏**：我检查了所有导出/初次发送点 —— `SiteReplicationMetaInfo` 的 Tags/Quota/SSE 三处（`:3960/:3980/:3988`）+ `initialBucketConfigReplicationEvent:51`。Policy 墓碑**故意**不受 gate 控制（原本已导出）；Versioning/Object Lock 无墓碑概念。**未发现遗漏的开关覆盖点。** ✔
- **滚动升级/降级**：off 时新增墓碑信息不导出，旧节点不会收到它无法正确处理的 Quota heal 墓碑（文档点名了"旧版会留下已解析 quota 残留"这一实证依据）。✔ 但 off **不等于**与旧版同构：修复端的**接收**行为已经变了（锁内排序、空 baseline 不删除、重复不写），这对旧版发来的事件是更安全的方向，混版冒烟也验证了普通 PUT/DELETE 互通。降级路径文档要求先关开关再滚降；唯一可补的一句是"墓碑时间字段本来就在 schema 内，旧版只是不导出，降级不会产生解析错误"。

---

## 6. 三个独立判断

**最小？—— 基本是，有一处可争议的扩张。**
生产 Go 代码净增约 37 行，六份重复的 heal / peer apply 被一套 helper 替代；没有新 schema、没有新 wire 字段、没有新锁、没有能力协商、没有迁移系统。唯一超出必要的是 **Policy 规范编码器接入写/读/导出路径**（F5）——它不是收敛所必需的，并且直接导致 F2。其余（`bucketMetadataUpdate` 提交快照、`ensureBucketMetadataCreated`、`rebaseBucketConfigDefaults`、gate）我都能各自对应到一个已证实的缺陷或本次修复直接触及的路径，**没有**可以无损删除的部分。

**充分？—— 对"已声明的范围"是；对"缺陷标题"不是。**
在同一桶世代、Created 已知、gate=on 的前提下，六类配置的源时间、锁范围、删除状态参与 heal、等时冲突裁决、重复/乱序抑制都成立。但两个口子要明说：(1) 默认 gate=off ⇒ Tags/SSE/Quota 的漏发删除不收敛（F7，计划内取舍）；(2) Created 未知的桶从"能写"变成"不能写"（F1，计划外回归）。

**必要？—— 是。**
每一项改动都能对应到 `baseline.log` 里的实测失败（SOURCE_TIME / NEWER_DELETE / STALE_RESURRECTION / BULK_STALE_OVERWRITE / CHECK_OUTSIDE_LOCK）或计划中论证过的路径。`rebaseBucketConfigDefaults` 这种看起来最"多余"的小分支，实际是 `Created` 一旦参与 baseline/tombstone 判定后的必然补丁（不加则接管时默认值会变成假墓碑）。

---

## 7. 新增设计记录应准确保留的内容

**关键决策**
1. baseline / live / tombstone 三态由 `(Created, 字段时间, 载荷空否)` 推导，**不新增 schema 字段**；`at == Created` 定义为 baseline。代价：必须知道 Created（⇒ F1 的根因）。
2. 全序：`valid > real > at > 同时间墓碑胜 > 稳定内容键`。deployment ID **不**参与比较，也不落盘。
3. 比较键是"已解析配置的纯函数"：Policy 走递归排序的规范 JSON，Quota 走解析后 `json.Marshal`，XML 走有效文档字节，Versioning 先按本站点 Object Lock 归一。
4. Versioning / Object Lock 为 update-only：空事件恒为 no-op，空值不是候选。
5. 读-比较-写整体在**既有** `metadata.lock` 内；`saveMetadata` 收指针以便调用方拿到提交后快照，保证"落盘状态 / 出站事件 / 源时间"三元组一致。
6. 新增删除信息的**导出**用启动开关 gate，默认 off；开关不探测远端能力，启用条件是全站点全节点已升级且旧请求排空。
7. 专用 peer 事件的零时间保留兼容例外（分配本地时间 + 限频 `legacy-zero`）；bulk 零时间仍按现状拒绝。

**被拒绝的方案**（记录以免复议）
- 新增 HLC / 向量时钟 / 新 wire 字段 / 能力协商 / 通用复制框架 / 新锁或重试系统。
- 用 `len(payload)==0 ⇒ 删除` 统一处理 bulk（会把"省略"误判为删除）。
- 把 `BucketPolicy.Equals`（忽略 Sid、对 Statement 顺序敏感）叠加为第二套判等规则。
- 把混版长期测试作为提交门槛。
- 把未修改上游 MinIO 的兼容性当作必需门禁（AGENTS.md：正式支持 PGSTY 栈，上游兼容为尽力而为）。

**限制**
- gate=off 期间 Tags/SSE/Quota 墓碑不可见，不承诺删除收敛，且存在被拒绝的周期性 RPC。
- 旧版到达时间污染、legacy-zero 产生的新本地时间、桶创建世代分歧**无法自动反推**，需运维在权威站点重新提交。
- `Created` 未知的桶不在收敛承诺内（并且按当前实现直接不可写，见 F1）。
- "比较键相同但落盘字节不同"（Quota 原始 JSON、Policy 旧编码）是允许的稳定状态，公开 mismatch 统计必须与比较键口径一致（见 F2）。

---

## 8. 建议处置顺序

1. 判定 F1 的条件（支持矩阵里有没有 `Created == 0` / 无 `.metadata.bin` 的桶）。有 ⇒ 先修再合。
2. 修 F2（推荐：不排序顶层 `Statement`，或让 `isBktPolicyReplicated` 改用规范键）。
3. 修 F3（ERROR→Warning、缺桶不报、reason 拆分）。
4. 补 F4 的三条文档。
5. 补测试：F10 的接管用例、F8-1 的 legacy 策略字节双站点用例、F8-2 去 stub 的 Created 用例、F8-4 的二进制身份记录。
6. F5 / F6 由作者判断，可留作后续。
