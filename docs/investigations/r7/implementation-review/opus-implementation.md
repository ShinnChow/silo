## R7 实现与可合并性独立复审（Opus 5，只读会话）

### 1. 审阅对象

| 项 | 值 |
| --- | --- |
| 候选提交 | `4fcdf37ce656152b32ad0f615d47f5e3f9748c3a` |
| 候选树 | `d5cc904bf2a8b57aea1aac53999b5007522279fc` |
| 现场 main/base | `9ebe81c1b3611f9cc73e676b5b741c2be62c467a` |
| 审阅 diff | `/Users/vonng/tmp/silo-r7-merge-20260916-ad51/reviewed-code.diff` |
| diff SHA-256（按调用方声明采信） | `5af32dc2cb4acc34ac91a3e48c5decfcac0d25c70bfa7d6399934c66f629df2a` |
| 原始 PR | #187，head `b8f2fdde41dff3dc3b8db669c1d42d30ca5c1d3d`，未合并 |

本会话无 shell，未运行任何测试、未计算任何哈希、未做任何网络操作，未修改任何文件。

### 2. 结论

**APPROVE_WITH_NONBLOCKING_NOTES。阻塞问题 0。**

**这份代码在其自身 PR 的 CI 实际变绿后可以合入 main。** 合并前提是 CI 真实通过，而不是"预期会通过"（理由见 §5.1）。

### 3. 我实际核验的内容（非采信）

逐行对读了 diff 与工作区当前源码，确认两者一致（`cmd/handler-utils.go:247-308` 就是 diff 后的内容；三个测试文件的 grep 行号与 diff 偏移量精确对应，如 `replication-content-encoding_test.go:108` = diff:246−138，`replication-metadata_test.go:68/90` = diff:488/510−420）。

**3.1 等价性（普通路径零行为变化）**
基线 `allowReplication=false` 分支是"命中 supportedHeader → 若属复制映射则 continue"；新代码是"若属复制映射则 continue → 再查表写入"。对 `nv` 的查找、`strings.Join(value, ",")` 的拼接、user-metadata 前缀循环全部逐字保留（`handler-utils.go:262-281`）。普通提取路径字节级等价。

**3.2 六个 SSE 字段与空标记**
`supportedHeaders` 的后六项（`handler-utils.go:86-91`）与 `replicationToInternalHeaders` 的键集完全相同（`:106-114`），且六→六为单射（含 `ReplicationSsecChecksumHeader` 的恒等映射，`bucket-replication.go:81`），因此由"遍历 slice"改为"遍历 map"后迭代顺序无关，无覆盖歧义。空标记 `X-Minio-Replication-Encrypted-Multipart: ""` 走 `ok=true` 分支、`Join([""])==""`，与基线一致，且 `internal/crypto` 按键存在性消费——测试 `replication-metadata_test.go:82-86` 的 want 映射显式固定了这一点。

**3.3 信任边界（PUT/COPY/MPU/Snowball）**
diff **未触碰任何调用点与信任判定**。我复核了全部五处调用与门控：
- PUT：`object-handlers.go:2199` 先提取 → `:2263` `evaluateReplicationTrust` → `:2278-2280` `applyReplicationTrust` → `:2281-2282` 仅 `replicaTrusted` 恢复。注意提取发生在剥离之前，因此普通提取器**必须**无条件跳过复制专用头——新代码正是无条件 `continue`（`:263-265`），这道纵深防御被完整保留。
- COPY：`object-handlers.go:1144-1152`，仅 REPLACE 分支且 `allowReplication` 为真时恢复；COPY 指令分支语义不变。
- MPU：`object-multipart-handlers.go:233` + `:246`，仅初始化阶段恢复；Part/Complete 不再提取元数据，测试里的 `Content-Encoding: br` 因此是未来回归护栏。
- Snowball：`replication-trust.go:78-90` 的整包判定 + `object-handlers.go:2786-2793` 的逐条目 `ReplicateObjectAction` 复核未变；`replicationRequestHeaders`（`replication-trust.go:96-112`）仍覆盖六个 wire 头与 REPLICA 状态。
无新增投毒面。

**3.4 Snowball 普通/replica parity（最实质的行为变化，判定为修复而非回归）**
`object-handlers.go:2802-2804` 的 `metadata` 只含 storage class。基线在 `:2836` 的恢复调用会把**外层 archive 请求**的 supportedHeaders + 用户元数据整体灌进每个条目（普通条目则完全没有），补丁后只剩六个映射；PAX 分支 `:2864-2872` 同理不再用原始 wire 值覆盖 `extractMetadata` 已归一化的 `m`。我另行确认仓库内**没有生产代码发送** `X-Amz-Meta-Snowball-Auto-Extract`（只有 `api-router.go:391` 接收），即不存在依赖旧继承行为的内部生产者。

**3.5 空格 token 行为**
`trimAwsChunkedContentEncoding`（`handler-utils.go:367-378`）按 `,` 切分后做**精确等值**比较、不做 TrimSpace。因此 `"aws-chunked, gzip" → " gzip"`（保留前导空格）、`"gzip, aws-chunked" → "gzip, aws-chunked"`（整串不变）。测试 `replication-metadata_test.go:44-51` 的两条期望与代码一致，属于**现状固化**，不得对外宣称为本次修复。

**3.6 原始普通元数据的删除（GHSA 相关，正面收益）**
`extractMetadata:219-223` 删除 `X-Amz-Meta-X-Amz-Unencrypted-Content-Length/-Md5`。基线的恢复函数会通过 `x-amz-meta-` 前缀循环把它们**重新注入**，这是对该 advisory 缓解的实际回退（仅限可信 replica 写）。补丁消除了该路径，测试在 canonical/lowercase 两种写法下都做了断言（`:100-105`）。

**3.7 GET/HEAD 与原始字节**
`ObjectInfo.ContentEncoding` 来自 `fi.Metadata["content-encoding"]`（`erasure-metadata.go:138`），`setObjectHeaders` 仅在非空时下发（`api-headers.go:129-131`）。所以测试同时断言"落盘 UserDefined 无该键"和"响应头不存在该键"是有意义且互相独立的。GET 分支比较原始字节，replica/gzip 用例写入的是真实 gzip 字节。

**3.8 签名与请求体**
`replicaEncodingStream` 的顺序正确：`newTestStreamingRequest` → 设置全部头 → `signStreamingRequest` → `assembleStreamingChunks`，chunk 签名逐块校验；非 chunked 分支 `newTestSignedRequestV4` 对 payload 计算 `x-amz-content-sha256` 并走 `authTypeSigned` 校验。测试确实经过签名验证链路，不是绕过。无权 replica 用例断言 XML `Code == AccessDenied` 并复核对象未被创建，能区分"签名失败"与"授权拒绝"。

**3.9 生产代码与 PR #187 的关系**
逐行比对 `pr187.diff` 与候选 diff：`cmd/handler-utils.go` 与 `cmd/handler-utils_test.go` **完全一致**，唯一差异是恢复函数的三行注释（pr187.diff:76-78 vs 候选:76-78），候选版补充了 Snowball 语义。"仅澄清注释"的说法属实。`commit-message.txt` 含 `Co-authored-by: Mikhail Khadarenka` 与 PR #187 归属声明。

**3.10 容量夹具是否削弱证据：不削弱**
`capacity-overlay.json` 只替换 `cmd/test-utils_test.go`；夹具唯一的功能性改动是 `ExecObjectLayerAPITest` 开头用仓库**既有**的 `tagTestCapacityDisk`（`cmd/erasure-server-pool-tags_test.go:258-264`，`DiskInfo` 返回 `Total=Free, Used=0`）包装 set 磁盘。对象字节与 xl.meta 仍写真实临时盘。未适配时的 `507/XMinioStorageFull` 原始日志保留（`http-baseline-unadapted.log`）。该文件在仓库外，**不在交付 diff 中**。
反事实同样成立：`exact-baseline-overlay.json` 只额外把**生产文件**换成 `baseline-handler-utils.go`（我核对该文件确含 `extractMetadataFromMimeWithReplication`/`allowReplication` 布尔开关），测试文件一字未改。失败点精确落在 `replica/bare`、`replica/mixed`、全部 8 个 Snowball 用例与全部 16 个 helper 子用例，`ordinary`/`untrusted-marker`/`gzip`/`unauthorized-replica` 全通过——44 通过 / 36 失败与声明吻合。

### 4. 我采信而未独立验证的部分

- 四个源文件与各日志的 SHA-256、diff SHA-256、提交/树对象哈希（无 shell，无法计算）。
- 所有测试的**执行事实**：`fixed-targeted`（80 叶子）、`fixed-sse-trust`、`fixed-race`、`make verifiers`、`make build` 的通过是读日志所得（我确认了 `fixed-targeted.log:179-180` 的 `PASS/ok`、`fixed-race.log:209-210`、`fixed-sse-trust.log:125-126`，以及四个 fixed 日志中无 `--- FAIL`），但这些进程不是本会话运行的。
- 提交对象实际携带的 author/trailer（`commit-message.txt` 内容正确，但我无法确认它就是 `4fcdf37ce` 的提交信息）。
- PR #187 远端当前状态（读取的是本地缓存 `pr187.json` / `pr187.diff`）。
- 无任何两站点调度器 / 进程重启 / 网络故障 / 线上验收；本次结论只覆盖本机认证 handler 接收链路与既有 SSE 回归。

### 5. 非阻塞意见

1. **CI 是真正的门，不是形式。** 这两个 HTTP 级测试从未在"无容量 overlay"的环境里跑过（本机盘满，未适配时直接 507）。CI 机器盘空间正常时不需要 overlay，预期可过，但这一点**本地无证据**。请等该分支自己的 CI 变绿再合，不要用本地日志替代。
2. **提交对象自检**（推送前，一条命令）：`git log -1 --format='%H %T %an %ae%n%b' 4fcdf37ce` 与 `git diff --stat 9ebe81c1b..4fcdf37ce`，确认树哈希、`Co-authored-by` 与"只动 2 个产品/测试文件 + 2 个新测试 + docs"。
3. **POST 表单路径仍未归一化**：`bucket-handlers.go:1262` 直接调用低层 `extractMetadataFromMime`，不走 `extractMetadata` 的 `aws-chunked` 裁剪与默认 content-type。本补丁按 N9 刻意不动它，但这意味着 POST policy 上传仍可能落盘原始 `aws-chunked`。建议单独开 issue，不要在本 PR 顺手统一。
4. **测试可加一条断言**：`replication-metadata_test.go:68-72` 只断言普通路径不出现六个 **internal** 键；再断言六个 **wire** 键名也不在 metadata 中，可完整封死这一面（当前实现确实不会写入，属加固）。
5. **Snowball 可选对照**：现有 parity 只比 ordinary vs replica，可再加一条 untrusted-marker archive 对照（Opus v1 的可选建议，非必须）。
6. **小瑕疵**：`assertReplicaEncodingObject` 用 `getPutObjectURL` 构造 GET/HEAD 目标，功能等价但读起来别扭，`getGetObjectURL` 更清楚。
7. **存量限制需在 PR 描述里写明**：本补丁只阻止目的端再次污染，不修复既有对象；若来源端仍存错误编码，`bucket-replication.go` 的逐字符串比较会在后续 heal/resync/比较时反复选择元数据复制。措辞按 N5 收窄（"可能反复选中"，不是"不间断热循环"）。设计见 `stored-metadata-remediation.md`，本次不授权任何现网扫描或改写。

### 6. 明确许可声明

**本次审阅的这份代码（候选 `4fcdf37ce…`，基线 `9ebe81c1b…`，即上述 diff）在其 PR 的适用 CI 实际通过后，允许合入 main。** 不得修改原贡献者的 #187 分支；不授权发布、部署或线上对象改写。若在此期间 main 前进，需重新查看集成增量，并在相关行为改变时重跑相应测试并重新评审。
