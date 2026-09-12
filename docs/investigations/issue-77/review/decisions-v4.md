# v3 通过后的作者实测与 v4 最小补充

Opus 第三轮已经给出 **GO_WITH_NONBLOCKING_NOTES，实施前阻断 0**，确认历史 baseline-live 初始化规则正确。见 [第三轮意见](opus5-max-v3.md)。其两条可执行非阻断说明（历史初值第二轮不再广播、deployment ID 不参与比较/持久化）已明确写入 v4。

## S4：接管改变 Created 会把默认时间变成假墓碑

这是作者补查发现，不冒称为 Opus 结论。

当前 `PeerBucketMakeWithVersioningHandler`（`cmd/site-replication.go:946-961`）先加载并补齐默认字段时间，再调用 SetCreatedAt 改写 Created。若桶被接管到较早的共同 Created，原本 `PolicyUpdatedAt == oldCreated` 且 policy=nil 的缺省状态，变成 `PolicyUpdatedAt > newCreated`，新排序/导出规则便会把它当成真实删除。

已在固定 main 上的独立 worktree 执行 `GOWORK=off go test ./cmd -run '^TestIssue77PlanAdoptionBaseline$' -count=1 -v`：ErasureSD 与 Erasure（默认多盘）都复现了该条件。Created 前移一小时，nil Policy 的字段时间留在原 Created。测试 PASS 的含义是**旧代码确实出现该现象**，不是修复通过；详见 [测试源码](adoption-baseline-probe.go.txt)、[实际输出](adoption-baseline-probe.log)、[元数据](adoption-baseline-probe.metadata.json)。测试只新增在临时 worktree，没有改产品代码。

## 最小修订

在现有接管锁内记住 oldCreated，执行现有 SetCreatedAt 得到 newCreated；仅当 Created 改变时，将本轮六类字段中原本为 0 或 oldCreated 的默认时间调整为 newCreated，再执行既有 bootstrap。所有真实字段时间（原本 > oldCreated）及载荷不动。绝不能仅以 nil 载荷识别默认状态，否则会丢失真正删除时间。

这不是另做 #78 的配置接管修复，而是让 #77 新比较器需要的 baseline 不变量跨越已有 SetCreatedAt 写入路径仍成立。只在提交 1 加小分支，T8 增加默认时间维持 baseline、真实 PUT/DELETE 时间保留的断言；没有增加实现提交、存储字段、协议、锁或依赖。

v4 其余行为与已通过的 v3 相同；第四轮只核查这个接管增量。
