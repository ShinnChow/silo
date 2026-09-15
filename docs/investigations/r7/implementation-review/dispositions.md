# R7 最终实现复核意见处置

## 审阅身份与范围

用户追加指令：使用 Opus 5 Max 核实，确认无误后合并 main。该指令授权此次推送、PR 与主干合并。

候选提交 `4fcdf37ce656152b32ad0f615d47f5e3f9748c3a`，基线 `9ebe81c1b3611f9cc73e676b5b741c2be62c467a`。实际评审 assistant 消息均为 `claude-opus-5`，命令显式指定 `--effort max`；辅助 Haiku 用量与评审模型分开记录。结果为 **APPROVE_WITH_NONBLOCKING_NOTES，阻塞 0**。

完整独立意见见 [Opus 原文](opus-implementation.md)，模型、源文件、diff 和原始日志哈希见 [metadata](opus-implementation.metadata.json)。原始 JSONL 保存在 `/Users/vonng/tmp/silo-r7-merge-20260916-ad51/opus-implementation.jsonl`。

## 七项非阻塞意见

| 意见 | 处置与证据 |
| --- | --- |
| 1. 必须等待本分支自己的真实 CI | 接受。合并前核验适用检查全部通过，尤其是不使用本机容量 overlay 的完整 `cmd` 测试。现有 main CI 通过不能替代候选 PR 的 CI。 |
| 2. 自检提交对象、署名和 diff | 已核对实际提交：树 `d5cc904bf2a8b57aea1aac53999b5007522279fc`，包含 Mikhail Khadarenka 的 Co-authored-by 与提交作者匹配的 DCO Signed-off-by。相对基线仅一个生产文件、三个测试文件及调查文档变化；四个 Go 文件哈希与通过的验证日志一致。 |
| 3. POST 表单路径仍未归一化 | 确认是原有低层调用路径，本修复不改变它。作为独立后续研究项记录；没有把本次结果宣称为所有上传方式的编码归一化。 |
| 4. 普通请求还可断言六个 wire 字段不泄漏 | 现有 `TestExtractMetadataHeaders` 已输入全部六个 wire 字段，仅期望 `content-type`，并用 `reflect.DeepEqual` 比较完整 metadata map，任何 wire 或 internal 字段泄漏都会失败。该测试已包含在通过的 `fixed-targeted` 验证中；无需增加重复断言。新增 canonical/lowercase 矩阵进一步覆盖恢复行为。 |
| 5. 可补充 Snowball untrusted-marker 对照 | 保留为可选增强。当前测试含同一归档 ordinary/replica 元数据一致性，以及既有 Snowball 逐条目权限回归；此次调用点与授权门控未改。 |
| 6. GET/HEAD 使用 getPutObjectURL 命名不够直观 | 确认 URL 构造等价，不影响方法、签名或断言。无需为命名改动已经通过的测试。 |
| 7. PR 描述必须说明存量与来源污染限制 | 接受并写入 PR 描述。旧对象不会自动修复；来源仍受污染时，后续 heal/resync/比较可能反复选择元数据复制。参见既有存量处理设计；本次没有现网扫描或改写。 |

## 合并条件

此次处置只增加审阅文档，生产与测试代码维持 Opus 审阅版本。推送前再次核对源文件哈希、DCO 和 main 基线；main 若前进，先检查集成增量，相关行为改变时重新验证和评审。通过正常 PR 合并流程保留 #187 作者署名，不修改贡献者分支。

本记录形成时尚未发布本分支的 PR，不能作为 CI 通过或已合并的证据。实际 PR、CI 与合并回执另行核验并保存于 `/Users/vonng/tmp/silo-r7-merge-20260916-ad51/`。发布、部署、线上对象改写和双站点故障验收不在此次执行范围。
