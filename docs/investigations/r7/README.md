# R7：可信复制 Content-Encoding 修复

## 交付摘要

生产修复只改 `cmd/handler-utils.go`：可信复制只恢复六个复制专用字段，保留调用方已规范化的普通元数据。采用 [PR #187](https://github.com/pgsty/silo/pull/187) 的生产逻辑，增加准确说明 Snowball 调用方的注释。PR 原作者：Mikhail Khadarenka；本地新增回归与调查记录由本任务提供。

- `aws-chunked`：对象元数据和 GET/HEAD 不包含 Content-Encoding。
- `aws-chunked,gzip`：只保留 `gzip`，原始 gzip 字节不变。
- `gzip`：保持原值与原字节。
- 六个复制字段保留，包括空 multipart 标记和 SSE-C checksum；认证/权限门控保持原语义。
- 普通提取删除的旧 unencrypted length/MD5 用户元数据不会被复制恢复阶段重新注入。

## 方案和真实 Opus 共识

- [调查与基线复现](research.md)
- [冻结方案 v1](plan-v1.md) 与 [逐项处置附录](plan-v1.dispositions.md)
- [最终共识](consensus.md)：真实 Claude Code 2.1.270，两轮显式 `claude-opus-5 --effort max`；所有实际评审 assistant 消息均为 `claude-opus-5`。第二轮 `APPROVE`，阻断 0。
- [首轮原文](review/opus-v1.md)、[第二轮原文](review/opus-v1-confirmation.md)；相邻 metadata 文件记录模型、命令、源 SHA、方案/原文哈希与原始 JSONL 路径。

共识在产品代码修改前记录；Opus 审阅代码和方案，测试由本任务执行，二者分别留证。

## 最终实现复核与合并准备

用户随后追加授权：使用 Opus 5 Max 核实，确认无误后合并 main。已对提交 `4fcdf37ce656152b32ad0f615d47f5e3f9748c3a`（基线 `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`）执行新的独立实现复核，实际评审模型为 `claude-opus-5`，显式 `--effort max`。结论为 **APPROVE_WITH_NONBLOCKING_NOTES，阻塞 0**；适用 PR CI 实际通过后才可合并。

[实现复核原文](implementation-review/opus-implementation.md)、[身份与哈希](implementation-review/opus-implementation.metadata.json)、[逐项处置](implementation-review/dispositions.md) 分别记录审阅与执行方验证边界。本次补充仅为文档；生产代码与测试维持已审阅、已本地验证版本。

## 兼容性与边界

Snowball 无 PAX 的可信复制条目不再继承外层归档的 content-type/cache-control/用户元数据，与普通 Snowball 一致。外层的六个复制专用字段仍可按既有规则作用于已授权条目。相同 tar 的普通和 replica 写入已纳入条目元数据一致性回归。

现有精确 token 裁剪规则保持不变：`aws-chunked, gzip` 留下带前导空格的 ` gzip`；`gzip, aws-chunked` 中带空格的 token 仍不会被去掉。这两条记录现状的断言不代表它们已被修复。POST 表单低层元数据提取行为也保持原样。

旧对象不会因升级自动修正；普通 COPY 保留来源已有元数据。若权威来源仍受污染，后续对账可能继续认为目标不一致并再次选择元数据复制。先核实来源版本、再协调副本的操作提案见 [存量处理设计](stored-metadata-remediation.md)。本任务未扫描或改写现网对象。

## 复验命令

在有充足空闲比例的普通测试机器上，正式测试不需要容量 overlay：

```sh
go test ./cmd -run '^Test(ExtractReplicationMetadata.*|APIReplicaContentEncoding|APISnowballReplicaContentEncoding)$' -count=1
go test -race ./cmd -run '^Test(ExtractReplicationMetadata.*|APIReplicaContentEncoding|APISnowballReplicaContentEncoding|APISnowballReplicationTrustIsPerEntry|APISSECReplicaSkipsDestinationTransforms|APISSECMultipartReplicaRoundTripWithCompression)$' -count=1
make verifiers
make build
```

本机实际命令见下述每次运行的 JSON；其中包含容量 overlay、并行度和使用的本地 golangci-lint 路径。

## 验证证据

完整命令、源文件/方案哈希、构建身份和检查结果汇总于 [verification.json](verification.json)。构建发生在本地提交前，二进制嵌入基线提交号；代码内容以验证清单中的文件哈希为准，不作为发布制品。

原始材料目录：`/Users/vonng/tmp/silo-r7-20260915-ad51/`。除原始发现阶段外，每次正式验证的 `.json` 记录命令、退出码、时间、日志哈希和四个代码文件的 SHA-256。

| 验证 | 状态与材料 |
| --- | --- |
| 原始 helper 基线 | `baseline.log`：裸/混合可信恢复失败，gzip 控制通过 |
| 原始 HTTP 基线 | `http-baseline-v2.log`：单盘及 16 盘，44 个控制通过，20 个已知缺陷失败 |
| 最终测试回退原始 helper | `exact-baseline-regression.{json,log}`：测试不变，只覆盖回基线产品文件；44 控制通过、36 预期失败（20 HTTP + 16 helper） |
| 修复后的定向测试 | `fixed-targeted.{json,log}`：9 个顶层测试、80 个具名子用例全部通过 |
| 既有 SSE 与信任边界 | `fixed-sse-trust.{json,log}`：SSE-C 单段/多段、SSE multipart trust、PUT/COPY 投毒、普通/复制权限、Snowball per-entry、默认桶加密、streaming trailer 等全部通过 |
| Race | `fixed-race.{json,log}`：新增 helper/HTTP/Snowball、既有 Snowball per-entry 与 SSE-C 单段/多段全部通过 |
| 仓库 verifiers | `verifiers.{json,log}`：make verifiers 通过，golangci-lint 0 issues，生成文件与兼容标识检查通过；typos 未安装，按 Makefile 跳过 |
| 构建 | `build.{json,log}`：make build 通过；本地 silo --version 已核对，二进制身份见 verification.json |

### 本机容量条件

未调整的 HTTP 夹具返回 507 / XMinioStorageFull，原始日志为 `http-baseline-unadapted.log`。宿主 APFS 接近满盘，触发相对空闲阈值。HTTP/既有 SSE/race 验证使用临时 Go overlay 复用仓库的 `tagTestCapacityDisk`，只改变 API 测试夹具看到的容量比率，实际对象和元数据仍读写测试磁盘。该临时文件在仓库外，不进入交付；生产容量策略没有变化。

证据为本机认证请求处理链路及实际存储、读取和既有 SSE 往返，不是双站点调度器、进程重启、网络故障或线上验收。

## 初始交付状态

初始提交形成时，研究、真实 Opus 方案共识、本地实现与验证均完成，结果保存在 `codex/r7-replication-content-encoding` 分支；当时尚未推送或合并。随后按用户追加授权进行上述最终实现复核与合并准备。实际 PR、CI 与主干合并状态以对应远端记录和 `/Users/vonng/tmp/silo-r7-merge-20260916-ad51/` 中的执行回执为准。
