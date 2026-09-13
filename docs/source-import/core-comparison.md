# 两套 Core 对比与迁移约束

外层 `core/` 有 21 个 Go 文件。与 `deepagent/core/` 比较，没有内容完全相同的 Go 文件。职责重叠不等于接口或实现兼容。

| 外层代码 | 内层对应职责 | 迁移约束 |
| --- | --- | --- |
| agent.go、subagent.go | agent.go、constructor.go、graph_builder.go、subagent_factory.go | 外层 Runnable 使用 *State；内层使用消息输入/输出及 MiddlewareChain，不能拼接同名 Config/DeepAgent |
| agentthread/thread.go | agentthread/thread.go、run.go、context_mng.go | 外层接收 protocol.Input，内层使用 Message/SubmitInputResult；Worker 需要适配 |
| backends/filesystem.go | backends/ 与后续 middleware/filesystem | 外层 Filesystem 同时提供工具，内层 Backend 分离存储与工具，不能直接覆盖 |
| checkpointer/ | checkpointer/ | 文件原子写和 Redis 逻辑可复用；外层 Get 返回两值，Eino CheckPointStore.Get 返回三值，且旧执行快照和新图快照不可直接混用 |
| compact/ | compact/ 与 agentthread.CompactionStrategy | Summarize 可作为基础能力；仍需实现 Compact/Resume 契约和记录映射 |
| middleware/repairjson.go | middleware/ 的参数修复能力 | 可复用纯函数；不是新 MiddlewareChain 的实现 |
| memory/ | memory/ | 依赖旧 Agent、文件工具和顶层 Manager API，需适配后接入 |

当前 worker/thread 和 worker/distributed（含测试）仍导入 eino-cli/core 路径。新 Core 尚缺 MiddlewareChain、工具包装等依赖，尚不能替换旧 Worker 执行入口。

用户已同意迁移期间允许编译失败。外层 core 已备份并迁入 deepagent/core，原目录已移除；不兼容的旧执行链保留在 engine/ 与 engine/agentthread/，没有覆盖新 Core。Worker 的导入已更新为迁入后的路径，尚未切换到新 DeepAgentThread API。

## 实际迁入位置

- `core/agent.go` → `deepagent/core/engine/agent.go`
- `core/subagent.go` → `deepagent/core/engine/subagent.go`
- `core/subagent_test.go` → `deepagent/core/engine/subagent_test.go`
- `core/README.md` → `deepagent/core/engine/README.md`
- `core/middleware/repairjson.go` → `deepagent/core/middleware/repairjson/repairjson.go`
- `core/middleware/repairjson_test.go` → `deepagent/core/middleware/repairjson/repairjson_test.go`
- `core/compact/compact_test.go` → `deepagent/core/compact/compact_test.go`
- `core/compact/compact.go` → `deepagent/core/compact/compact.go`
- `core/memory/memory.go` → `deepagent/core/memory/memory.go`
- `core/memory/consolidator_test.go` → `deepagent/core/memory/consolidator_test.go`
- `core/memory/durable.go` → `deepagent/core/memory/durable.go`
- `core/memory/consolidator.go` → `deepagent/core/memory/consolidator.go`
- `core/memory/memory_test.go` → `deepagent/core/memory/memory_test.go`
- `core/backends/filesystem.go` → `deepagent/core/tools/filesystem/filesystem.go`
- `core/backends/filesystem_test.go` → `deepagent/core/tools/filesystem/filesystem_test.go`
- `core/agentthread/thread.go` → `deepagent/core/engine/agentthread/thread.go`
- `core/agentthread/compact_test.go` → `deepagent/core/engine/agentthread/compact_test.go`
- `core/agentthread/thread_test.go` → `deepagent/core/engine/agentthread/thread_test.go`
- `core/checkpointer/file_test.go` → `deepagent/core/checkpointer/file_test.go`
- `core/checkpointer/redis_test.go` → `deepagent/core/checkpointer/redis_test.go`
- `core/checkpointer/redis.go` → `deepagent/core/checkpointer/redis.go`
- `core/checkpointer/file.go` → `deepagent/core/checkpointer/file.go`

备份：`/Users/yingbaosun/src/LLM-core-backup-20260912-204425`。这次完成目录归并与引用调整，未声称两套执行模型已经语义统一。
