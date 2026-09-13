# 原仓库源码接收清单

用户提供的 40 个 Go 文件已迁入本仓库 deepagent/core/。顶层 core/ 已备份并迁入；host/、manager/、worker/ 等仍保留原位置，入口尚未切换到用户提供的新 Core。文件名按职责推断；附件来源与 SHA256 见 manifest.json。仅清理粘贴格式和重复内容，不主动修改实现语义。

原始迁入批次为 40 个 Go 文件；目前连同按口述补写的常量、图状态和测试，共 45 个 Go 文件。gofmt 解析与格式化通过；已尝试 GOPROXY=off GOSUMDB=off go test -mod=readonly ./deepagent/core/...，在依赖加载阶段失败：缺少下列本地包及尚未声明的外部依赖，未进入测试执行。迁入文件与暂存源码逐字节一致。

## 已接收文件

- `deepagent/core/agent.go`
- `deepagent/core/agentthread/callback_helpers.go`
- `deepagent/core/agentthread/context_contracts.go`
- `deepagent/core/agentthread/context_middleware.go`
- `deepagent/core/agentthread/context_mng.go`
- `deepagent/core/agentthread/copy_helpers.go`
- `deepagent/core/agentthread/model_callback_helpers.go`
- `deepagent/core/agentthread/model_event_barrier.go`
- `deepagent/core/agentthread/options.go`
- `deepagent/core/agentthread/redis_seq_generator.go`
- `deepagent/core/agentthread/rollout_gorm_store.go`
- `deepagent/core/agentthread/run.go`
- `deepagent/core/agentthread/run_config.go`
- `deepagent/core/agentthread/run_event_recorder.go`
- `deepagent/core/agentthread/run_handle.go`
- `deepagent/core/agentthread/run_start.go`
- `deepagent/core/agentthread/thread.go`
- `deepagent/core/agentthread/token_usage_tracker.go`
- `deepagent/core/agentthread/tool_event_state.go`
- `deepagent/core/agentthread/types.go`
- `deepagent/core/backends/backend.go`
- `deepagent/core/backends/file_text.go`
- `deepagent/core/backends/filesystem.go`
- `deepagent/core/backends/safe_execute.go`
- `deepagent/core/backends/shell_cancel_unix.go`
- `deepagent/core/backends/store.go`
- `deepagent/core/backends/store_backend.go`
- `deepagent/core/config.go`
- `deepagent/core/constructor.go`
- `deepagent/core/constructor_test.go`
- `deepagent/core/context.go`
- `deepagent/core/graph/branch.go`
- `deepagent/core/graph/message.go`
- `deepagent/core/graph/stream_tool_executor.go`
- `deepagent/core/graph/tool_call_collector.go`
- `deepagent/core/graph_builder.go`
- `deepagent/core/max_model_calls.go`
- `deepagent/core/run_options.go`
- `deepagent/core/subagent_factory.go`
- `deepagent/core/subagent_validation.go`

## 引用但尚未收到源码的本地包

- `eino-cli/deepagent/core/internal/toolerrors`
- `eino-cli/deepagent/core/middleware`
- `eino-cli/deepagent/core/middleware/baseprompt`
- `eino-cli/deepagent/core/middleware/contextmanager`
- `eino-cli/deepagent/core/middleware/filesystem`
- `eino-cli/deepagent/core/middleware/patchtoolcalls`
- `eino-cli/deepagent/core/middleware/plan`
- `eino-cli/deepagent/core/middleware/skill`
- `eino-cli/deepagent/core/middleware/subagent`
- `eino-cli/deepagent/core/middleware/web`
- `eino-cli/deepagent/core/tools`
- `eino-cli/deepagent/core/tracing`
- `eino-cli/deepagent/core/utils`
- `eino-cli/deepagent/serialiser`

## 尚待集成核对

- 仍缺源仓库 go.mod/go.sum、完整业务入口及 Host/Manager/Worker 等模块。
- 已接收包也可能不完整；仍需 ContinueAfterModelFunc、测试辅助 fakeToolCounter 等定义。
- graph 已收到路由、消息流合并、工具调用收集器和流式工具执行器；仍依赖 tools、internal/toolerrors 等未接收包。
- backends 的 StoreBackend 返回指针切片，而已接收 Backend 接口返回值切片，需结合源仓库其余代码核对用途；未擅自调整接口。
- shell_cancel_unix.go 仅适用于非 Windows；Windows 实现尚未收到。
- code.byted.org 等外部依赖版本和可访问性尚未确认。

## 目录适配与后续补写

已核对现有源码包名和导入路径，40 个原始文件均归属 deepagent/core，不移动到同级 backend、manager 或 output。

- core 根目录：10 个文件，Agent 构造、配置、图构建与执行。
- core/agentthread：19 个文件，会话、Run、上下文和事件。
- core/backends：7 个文件，工具使用的文件与命令后端。
- core/graph：4 个文件，图路由与流式执行辅助组件。
- core/constant：按口述新增 3 个文件。
- core/types：按口述新增 graph_state.go，另有 graph_state_test.go。

同级 backend、cmd、host、manager、output、protocol、serialiser、tools、worker 当前为空目录。serialiser 按已有源码导入路径统一拼写，原先空的 serializer 目录已重命名。顶层旧实现不自动迁入这些目录，以免混入另一套实现。

补写的图状态使用标准库 log/slog；constant 和 types 包已通过各自的编译或测试，整个 Core 仍缺其余依赖。

## Core 子目录补齐

已按后续口述实际创建 checkpointer、compact、engine、internal、memory、middleware、tools、utils 空目录；已有 agentthread、backends、constant、graph、types 保留。文件后端沿用已接收源码的 backends 拼写。目录存在不代表模块已实现。

## 外层 Core 归并

外层 21 个 Go 文件和 README 已全部迁入，原 core/ 目录已移除。检查点、压缩、记忆和 JSON 修复保留对应职责；旧文件工具在 core/tools/filesystem，旧执行链及其 Thread 暂在 core/engine 下隔离。Worker 导入路径已更新。新 Core 原有文件未覆盖。详见 core-comparison.md 和 core-migration.json。
