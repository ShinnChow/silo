# 作者补充核验

S1：`internal/logger/logonce.go:95` 按错误正文而非只按 key 去重。动态时间进入正文会绕过去重。修订要求：稳定的原因错误，动态详情放 logger ReqInfo，沿用现有每小时清理机制；不新增日志/限频框架。

S2：当前 silo-pkg 的 ActionSet.MarshalJSON 与 ResourceSet.MarshalJSON 直接枚举 map。实测同一个已解析 BucketPolicy 连续 json.Marshal 100 次，得到 4 种字节序列。源代码探针为 policy-encoding-probe.go.txt；使用项目 go.mod 选择的 pgsty/silo-pkg/v3 v3.13.4-0.20260910091716-2d8fd3cbbf07，命令为 GOWORK=off go run <探针临时路径>。

因此普通 json.Marshal 不能当作稳定排序键。v2 需要一个仅用于 Server 桶策略比较的确定性表示，对已通过现有解析器校验的策略排序集合成员；不能为了这一点修改依赖或持久化 wire 格式。

以上为作者在外部 v1 评审期间做的独立补查，不冒充 Claude 的评审发现，也不代表已经实现修复。
