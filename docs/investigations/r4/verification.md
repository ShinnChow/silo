# R4 修复与本地验收

这是 2026-09-15 的本地验收快照。用户后续授权的实现级复核、提交规范调整与合并流程见 [合并前复核](merge-verification.md)；以下原始测试记录及哈希保留当时状态。

## 结果

在 `putOptsFromHeaders` 的 SSE-KMS 选项构造中补齐 `ReplicationSourceTaggingTimestamp`。目的端使用显式 SSE-KMS、桶默认 KMS 或自动加密时，可信复制 COPY 现在能消费来源标签时间戳，并在现有存储锁内完成排序。

生产修改只有一个字段和相邻注释。API、存储格式、KMS key/context、信任判断和既有排序规则保持兼容。R5 的删除/空标签及 PUT/multipart 时间戳传播独立交付。

## 方案与 Opus 共识

- 基线：`9ebe81c1b3611f9cc73e676b5b741c2be62c467a`，已重新查询 GitHub main。
- 分支：`codex/r4-kms-tag-timestamp`。
- [冻结方案 v1](plan-v1.md)：SHA-256 `ad539f2071155de6955b583991684ed33c4bfe2e29660005840cdc97d7e1a754`。
- 真实评审为本机 Claude Code 2.1.270，实际 assistant 模型 `claude-opus-5`，显式 `--effort max`。结论 **GO_WITH_NONBLOCKING_NOTES，0 个阻断项**。
- [逐条意见处置与双方共识](consensus.md)、[原始返回评审正文](opus-v1-review.md)、[模型与哈希记录](opus-v1.metadata.json) 已保存。先保存共识，再修改生产源码。

## 变更与测试

| 文件 | 内容 |
|---|---|
| `cmd/object-api-options.go` | 在 KMS 字面量中保留已解析的来源标签时间戳。 |
| `cmd/object-api-options-replication_test.go` | 无加密、SSE-S3、SSE-KMS、带 key/context 的 KMS、SSE-C；可信/非可信；缺失、有效、无效标签时间；纳秒、时区与空格；mtime/ETag/三个时间戳、metadata 与 SSE 序列化。 |
| `cmd/object-copy-replication-tagging_test.go` | 两种单池后端 × 五种目的端加密模式 × 五个有序事件，共 50 次签名 COPY 和 50 次普通 GET。每步检查最终标签、精确时间戳、对象版本、加密类型及明文内容。 |

COPY 使用 `metadata=REPLACE`、`tagging=REPLACE` 和可信复制身份。事件为较新更新、乱序旧更新、重复事件、再次更新，以及不带来源标签时间戳的请求。更新间隔仅 1–3 纳秒，防止时间精度退化被秒级测试掩盖。无加密与 AES256 是对照；KMS 覆盖显式、桶默认和自动加密入口。

## 验证状态

| 检查 | 结果 | 证据文件 |
|---|---|---|
| 最终测试 + 未修复基线 constructor overlay | 预期失败；只有可信 KMS 有效标签时间戳及 KMS COPY 更新失败，对照通过 | `baseline-final.log/json` |
| 修复后最终新增测试与既有 trust/options 测试 | 通过；未使用生产源码 overlay | `focused.log` |
| 既有 KMS Object Lock 回归 | 首次受宿主机磁盘余量阈值阻挡；仅适配测试容量报告后，与上述定向测试一起通过 | `focused-capacity-adapted.log/json`、`capacity-fixture.diff` |
| 新增测试 `-race` | 通过 | `race.log/json` |
| `go vet -p 2 ./cmd` | 通过 | `vet.log/json` |
| 仓库配置的 golangci-lint，范围 `./cmd/...`、`kqueue` build tag | 通过，0 issues | `lint.log/json` |
| gofmt、git diff --check | 通过 | `format-checks.json` |

原始日志和每条命令的运行记录位于 `/Users/vonng/tmp/silo-r4-evidence-20260915-a9cb/`。每份检查 JSON 都记录命令、退出码、时间和三个源码/测试文件的 SHA-256；最终交付已逐一确认文件哈希一致。[验证清单](verification.json) 另记录 overlay 的实际替换文件哈希，避免混淆基线与修复版执行代码。

测试使用真实签名 HTTP 路由、实际本地对象数据/元数据读写、服务器加解密代码；远程密钥服务由 `kms.NewStub` 代替。ErasureSD 与 16 盘 Erasure 均为单池。容量适配只使用已有 `tagTestCapacityDisk`，避免本机磁盘使用比例触发防写阈值，所有对象 I/O 仍由真实测试磁盘承担；未调整生产容量保护。

## 交付与剩余边界

- 本地实现和要求的定向验证均已完成，将源码、回归、研究、共识和验收记录作为一个本地提交交付。
- R4 的独立生产补丁已提供给 R5：`/Users/vonng/tmp/silo-r4-evidence-20260915-a9cb/r4-kms-tag-timestamp.patch`，SHA-256 `2d4806d986bbd94ba4bc3951f3aeee48401ee1921c28ded0988fa09ca76ca26f`。
- Opus 共识为方案级共识；本地测试结论来自实际运行，不把它记作 Opus 执行了测试。
- 多池/多站点故障恢复、外部 KMS 服务、完整 Linux CI、主干合并、远端发布和部署尚未执行。
- 没有改写存量。丢失的来源时间戳不能仅从接收端推导；后续重放/重同步须核对来源权威性及 R5 的全链路处理，不保证旧事件重放可以修复全部历史状态。
- 另登记 KMS 字面量缺少 Proxy/Speedtest 标志的范围外观察，已交父任务单独核验，本次未扩大修复。
