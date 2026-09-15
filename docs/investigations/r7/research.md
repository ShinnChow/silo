# R7 调查与复现

## 结论

基线 `9ebe81c1b3611f9cc73e676b5b741c2be62c467a` 中，可信复制恢复过程把已规范化的普通元数据从原始请求中重新提取。`aws-chunked` 是传输编码，重新落盘后会被 GET/HEAD 返回。SILO 在 2026-04-15 的 `56fa63bfd155154157cd7e1fb6dc295a3b3104ed` 中引入此回归；该提交修复的复制头信任边界仍需保留。

实时核对 [PR #187](https://github.com/pgsty/silo/pull/187)：OPEN、未合并，head `b8f2fdde41dff3dc3b8db669c1d42d30ca5c1d3d`；只恢复复制专用字段的方向与根因吻合。没有将 PR 自报测试当成本轮验证。

## 当前调用链

五处调用：PUT、COPY REPLACE、NewMultipartUpload、Snowball 外层请求、Snowball PAX 条目。

- PUT/COPY/multipart 使用规范化普通元数据；可信分支不应再覆盖它。
- Snowball 外层只有 storage class/条目转换信息，没有通用提取；归档外层 Content-Type/Content-Encoding 不是条目元数据。
- PAX 元数据经过普通提取；无 Content-Encoding 的 PAX 映射也不能留下先前泄漏的外层编码。
- multipart 的 Part/CopyPart/Complete 不调用该恢复函数；完成后必须检验初始化元数据确实被保留。
- 所有可信恢复均需通过认证、精确复制标记、ReplicateObject 权限和 REPLICA 状态的组合判断；Snowball 对每个条目分别鉴权。

`erasure-metadata.go` 将落盘 `content-encoding` 读入 ObjectInfo；`api-headers.go` 在 GET/HEAD 返回该值。对象字节并非因此一定受损。

AWS [SigV4 streaming 规范](https://docs.aws.amazon.com/AmazonS3/latest/developerguide/sigv4-streaming.html) 要求保存对象时去掉 aws-chunked，只保留实际的内容编码；只有 aws-chunked 时读取响应不应有 Content-Encoding。

## 实测记录

原始证据根目录：`/Users/vonng/tmp/silo-r7-20260915-ad51/`。

| 记录 | 结果与边界 |
| --- | --- |
| `baseline_test.go` / `baseline-overlay.json` / `baseline.log` | 原始产品代码，临时 Go 测试覆盖：ordinary bare/mixed 正常，trusted bare/mixed 重新污染，纯 gzip 正常 |
| `http-baseline-unadapted.log` | 未调整夹具的本机单盘 HTTP 上传被 507 / XMinioStorageFull 拒绝；不是 R7 结果 |
| `http-baseline.log` | 首个容量适配 HTTP 运行；PUT/COPY/Snowball 可复现；multipart 夹具错误用 Header.Get 读取了仓库直接写入的 ETag 键，完成时 InvalidPart，不能用于 multipart 结论 |
| `http_test.go` / `http-capacity-overlay.json` / `http-baseline-v2.log` | 修正 ETag 读取后：PUT、COPY REPLACE、multipart 的普通和 untrusted-marker 对照通过；trusted bare/mixed 在落盘和 GET/HEAD 中失败；trusted gzip 通过；无权 replica 返回 403 且不建对象 |
| 同一最终 HTTP 基线日志的 Snowball 部分 | no-PAX、PAX 无编码、PAX 裸编码、PAX 混合编码均复现 GET/HEAD 污染 |

最终 HTTP 基线总计 64 个叶子用例：44 个控制通过，20 个预期缺陷失败。每个用例均使用认证 HTTP handler + 实际对象磁盘。PUT 传输案例和 Snowball 使用真实流式分块签名，gzip 案例使用实际 gzip 字节；读取比较原始字节并检查 Content-Encoding。

## 容量适配的准确含义

宿主 APFS 报告约 100% 使用率，触发 SILO 的相对空闲阈值。临时 `capacity-test-utils_test.go` 使用仓库既有 `tagTestCapacityDisk` 包装 API 测试夹具，将总量/已用量报告为当前空闲量/0；实际对象写入、读取和元数据持久化仍落在测试磁盘。该 overlay 不进入产品 diff，也不调整生产容量策略。结果属于明确容量适配后的本地验证，不是未改夹具的通过。

## 方案与存量

冻结方案见 [plan-v1.md](plan-v1.md)。存量对象处理见 [stored-metadata-remediation.md](stored-metadata-remediation.md)，只提供设计，本任务不执行现网对象改写。
