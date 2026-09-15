Write 工具在本会话被禁用，因此评审直接以回复形式交付（本次任务本就是只读评审，未改动任何文件、未运行任何测试）。

---

# R7 修复方案独立评审

## 1. 审阅版本与哈希

- **审阅对象**：`/Users/vonng/.codex/worktrees/ad51/silo/docs/investigations/r7/plan-v1.md`，72 行，标题 `R7 plan v1: preserve normalized replica object metadata`（`docs/investigations/r7/` 在 git status 中为 untracked）。
- **任务给定 SHA-256**：`7af5705ebbfb0a375956d38dba059095dc16e24558290b1bd35a6ce85b9e1f96`。本会话只读且无 shell，**我未重新计算该哈希**；逐字审阅的是该路径当前内容。
- 基线 `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`、引入提交 `56fa63bf…`、PR #187 的 OPEN/head 状态均按任务声明采纳，**未用 git/gh 复核**。
- `pr187.diff` 被当作“提议的代码”阅读，不视为已运行的测试；`baseline.log` 由本会话之外产生，我只读未跑。
- **性质**：源码与方案验证，非我执行的测试。

## 2. 结论

**APPROVE_WITH_NONBLOCKING_NOTES**

## 3. 阻塞问题

**无阻塞问题。** 以下为逐项核验依据。

**最小补丁充分性（充分）**：恢复辅助函数是 replica 路径上唯一能把未归一化的 ordinary 头写进对象元数据的入口。`putOptsFromHeaders`/`getDefaultOpts`（`cmd/object-api-options.go:388-477`）只读 SSE 与 source-* 时间戳；`completeMultipartOpts:542-548` 只取 actual-object-size 与 ssec-crc。五处调用点（`object-handlers.go:1149/2282/2836/2869`、`object-multipart-handlers.go:246`）全部被覆盖。

**信任边界（未改变）**：`evaluateReplicationTrust`（`cmd/replication-trust.go:78-90`）要求已认证主体 + 精确单值 `true` 标记 + `s3:ReplicateObject`，`REPLICA` 声明无权限直接 403；Snowball 走等价的 per-entry 内联判定（`object-handlers.go:2784-2793`）。补丁不前移恢复点、不让头部本身产生信任。

**调用链（方案描述与代码一致）**：PUT 在签名校验后评估信任、元数据在 `:2199` 已归一化；COPY REPLACE 用 `extractMetadataFromReq`、COPY 保留源语义（`:1143-1156`、`:1411`、`:1801`）；多段初始化在 sanitization 之后提取（`object-multipart-handlers.go:179/233`）；**分片与完成确实不需要恢复**——分片从 `mi.UserDefined` 取加密状态（`:885-886`、`:957-987`），完成从 `completeMultipartOpts` 取两个字段。

**六个映射与空标记**：补丁遍历 `replicationToInternalHeaders`（`handler-utils.go:106-114`），与基线遍历 `supportedHeaders` 的键集完全相同，且六→六为单射，故 map 迭代顺序无关；空值 multipart 标记按 key 存在性消费（`internal/crypto/metadata.go:27`），`strings.Join([]string{""}, ",")==""` 行为与基线一致。

**归一化与冗余用户元数据**：基线恢复分支会把刚被 `extractMetadata`（`:218-241`）删除的 `X-Amz-Meta-X-Amz-Unencrypted-Content-Length/-Md5`（`internal/http/headers.go:138-139`，GHSA-76wf-9vgp-pj7w）按原始大小写写回，补丁一并消除。

**额外独立验证（支持方案的关键事实）**：本仓库固定的 minio-go（`go.mod:71` → `pkg/signer/utils.go:70-87 setAwsChunkedContentEncoding`）**保留调用方已设编码**并生成 `aws-chunked` 或 `aws-chunked,gzip`（无空格）。因此方案声明的 `aws-chunked→无`、`aws-chunked,gzip→gzip` 与真实复制线路一致，修复后目标端存储值将等于源端 `objInfo.ContentEncoding`（`bucket-replication.go:838`），读路径 `erasure-metadata.go:138` → `api-headers.go:129-131` 也成立。

## 4. 非阻塞意见

1. **Snowball 无 PAX 条目的行为变化必须显式承认并加断言**。证据：`object-handlers.go:2802-2842` 的 `metadata` 只有 storage class 与压缩键，基线恢复会把外层 tar 请求的 content-type / `x-amz-meta-*` / cache-control 复制进每个 entry；补丁后不再复制，与普通 Snowball（`:2874-2877` 分支从不做 `extractMetadata`）一致。我同意这个选择，但它超出“只去掉 aws-chunked”。最小修正：在 §Proposed patch 第 6 条写明“trusted replica 无 PAX 条目不再继承外层归档 ordinary 元数据”，并在 §Verification 4 增加断言：同一 tar 中 trusted 与 untrusted 无 PAX 条目的 UserDefined（除 replica 状态/时间戳/ETag 外）相等；同时注明“外层请求的六个字段仍套用到所有条目”是既有且有意保留的行为。
2. **回归护栏应绑定 handler 级用例**。`baseline_test.go` 只覆盖 helper、绕过信任门；真正会退化的是调用点。最小修正：§Verification 7 的“基线必须失败”至少绑定一条 HTTP 用例（trusted replica streaming PUT → `GetObjectInfo().ContentEncoding`）。
3. **流式签名测试必须在签名前注入 replication 头**。`newTestStreamingSignedCustomEncodingRequest`（`test-utils_test.go:817-834`）先 Set 编码再签名；若签名后再加 `x-amz-bucket-replication-status`，得到的是 403 SignatureDoesNotMatch，容易被误读成“未恢复元数据”。最小修正：在 §Verification 2/3 补一句，并要求区分签名失败与权限拒绝。
4. **精确 token 裁剪的空格限制未被测试固定**。`handler-utils.go:357-368` 按 `,` 分割做精确等值比较，`"gzip, aws-chunked"` 不会被裁剪。同意不改语法；最小修正：§Verification 1 增加两条“记录现状”的断言用例。
5. **已污染对象的收敛性风险应进入补救段**。`bucket-replication.go:987-997` 用源端 `ContentEncoding` 与目标端元数据比较，而 minio-go 在已含 `aws-chunked` token 时直接早退（`utils.go:78-80`）：源端仍为 `aws-chunked` 的历史对象在目标端修复后将永久 mismatch 并反复触发 metadata 复制。最小修正：补救顺序明确为“先修源端版本、再 reconcile 副本”，并把该 churn 列为已知影响与监控项。
6. **“仓库已有的容量适配器”与现状不符**：`cmd/` 下不存在此类测试旁路（`object-api-utils.go:1302 hasSpaceFor` 无测试开关；`cmd` 内 grep `adapter` 仅命中 `object-api-interface.go:325` 注释）。最小修正：改写为“显式记录所用临时本地改动并保证其不进入交付 diff”。
7. **map 迭代等价性建议写入方案**（第 3 条补一句“顺序无关性来自单射映射”），避免未来新增映射时被误改；`nv` 对重复大小写头仍是“最后写入者胜”，与基线相同，不是本次引入。
8. **GHSA 冗余键回写的修复建议在根因段显式成句**；并注意 `putReplicationOpts`（`bucket-replication.go:797-813`）不像 `getCopyObjMetadata`（`:733-735`）那样过滤这两个键，故历史对象可能确实携带它们，修复后目标端会与普通 PUT 一样丢弃。
9. **POST 表单路径的不对称**：`bucket-handlers.go:1260-1266` 直接调用 `extractMetadataFromMime`，本就不做裁剪/默认 content-type/冗余键删除；补丁未改变它，建议在方案中补半句，避免未来“顺手统一”而改变 POST 行为。

## 5. 是否同意同一 v1 方案进入实现

**同意。** 该 v1 方案的根因、信任边界、调用链与补丁范围均与当前基线源码相符，不存在阻塞缺陷；上述 9 条均为记录/测试/文档层面的处置，不需要改动补丁本体。在 Codex 逐条记录处置（尤其第 1、2、5 条）后，**同一 v1 方案可直接进入实现，无需 v2**。

最后重申两点事实边界：PR #187 diff 中的测试改动是提议代码而非已运行证据；`baseline.log` 的失败断言是既有缺陷的证据，非实现验证——两者都不是我在本会话中运行的测试。
