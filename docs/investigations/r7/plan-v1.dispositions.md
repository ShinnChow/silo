# R7 v1 评审意见处置与验收补充

- 冻结方案仍为 `plan-v1.md`，SHA-256 `7af5705ebbfb0a375956d38dba059095dc16e24558290b1bd35a6ce85b9e1f96`。
- Opus 首轮：`APPROVE_WITH_NONBLOCKING_NOTES`，阻断 0；原始评审见 `review/opus-v1.md`。
- 本文件只澄清兼容边界与验收，不改变生产补丁范围；作为 v1 的绑定附录交给 Opus 再确认。确认前仍不修改产品代码。

| 意见 | Codex 处置与证据 |
| --- | --- |
| N1：Snowball 无 PAX 行为变化与 parity | 接受。可信 replica 的无 PAX 条目不再继承外层 archive 的 ordinary content-type/content-encoding/cache-control/user metadata；这一可见变化使它与普通 Snowball 一致，纳入报告。六个复制专用字段仍能由外层传给每个已授权条目，PAX 的专用字段可按现有次序覆盖。parity 测试用相同 tar 分别执行 ordinary 与 replica 请求并比较条目元数据（排除 replica 状态/时间/ETag）；不能按建议字面在一个 REPLICA 请求中混入无 ReplicateObject 权限的条目并期待它成功，因为 `object-handlers.go:2788-2791` 会拒绝该条目。现有 per-entry 权限回归另行保持。 |
| N2：HTTP baseline 回归护栏 | 接受，已实测。`/Users/vonng/tmp/silo-r7-20260915-ad51/http-baseline-v2.log` 包含单盘及 16 盘的真实 streaming PUT -> ObjectInfo -> GET/HEAD 失败，同期 ordinary/gzip 控制通过；还覆盖 COPY/multipart/Snowball。64 个叶子：44 控制通过，20 缺陷失败。 |
| N3：签名前注入所有头 | 接受，已落实在临时测试 `replicaEncodingStream`：先 newTestStreamingRequest、设置所有头，再 signStreamingRequest 和 assembleStreamingChunks。拒绝测试还应断言 XML 错误码为 AccessDenied，区分签名失败。 |
| N4：空格 token 现状 | 接受。helper 增加 `aws-chunked, gzip -> " gzip"`、`gzip, aws-chunked -> "gzip, aws-chunked"`，仅固定现有精确 token 规则，生产 normalizer 不改。后一例属于既有 token 语法限制，不能宣传为本次已修复。 |
| N5：历史污染来源反复不一致 | 接受风险并限定措辞。`bucket-replication.go:987-997` 的逐字符串比较可让仍有错误编码的来源与修正后目的对象持续不一致；在再次 heal/resync/比较时可再次选择 metadata 复制。源码证据不单独证明一个不间断热循环。存量提案应先确认并处理权威源版本，再协调各副本；记录重复元数据复制/不一致，而不是只修目的端。自动清理历史来源不进入本次生产补丁。 |
| N6：容量 adapter 不存在 | 不采纳此事实判断，但接受“临时调整不入交付”的要求。请直接读取基线 `cmd/erasure-server-pool-tags_test.go:258-265`：`type tagTestCapacityDisk struct{ StorageAPI }` 的 DiskInfo 返回当前 Free 作为 Total、Used=0；该文件 :131 有现有调用。此前按字符串 adapter 搜索漏掉了该类型。临时 `capacity-test-utils_test.go` 仅复用它来包装 API 夹具；原始 507 和适配日志均保留，产品容量策略不改。 |
| N7：map 迭代等价性 | 接受。现有六个 wire key 到六个 internal key 为单射，遍历顺序无关；重复不同大小写头的 canonical map 碰撞行为沿用基线，不引入新的解析规则。 |
| N8：被删除的旧加密用户字段 | 接受，根因和测试均包含 `X-Amz-Meta-X-Amz-Unencrypted-Content-Length/-Md5` 的再次注入。历史污染可能继续从来源传来；此次目标端普通提取删除后不会恢复这些字段。helper 对 canonical/lowercase 均断言不存在。 |
| N9：POST 表单路径 | 接受边界说明。POST 表单直接调用低层 extractMetadataFromMime，原本就不执行 extractMetadata 的完整归一化；本补丁保持它的现状，不顺带统一逻辑。 |

## 最终验收范围补充

正式回归保留真实分块签名、有效 gzip 字节、GET 原始字节比较；没有两站点调度器/进程重启/网络故障验收时，就只报告本地复制接收链路和既有 SSE 测试的结论。

存量修复有单独可审阅文件 `stored-metadata-remediation.md`，须按 N5 增补“权威来源优先”的顺序；没有扫描/改写现网对象的授权或动作。
