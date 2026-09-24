# Core 最终技术方案

以用户给出的 50 节方案为实施约束。此文件替换上一版 Definition / Kernel / pipeline 设计。

```text
ThreadHost
  → DeepAgentThread.SubmitInput / ResumeRun
    → run.execute
      → DeepAgent.Run / Stream → execute
        → Eino Graph

START → prepare → model ──tool calls──→ tools ──→ model
                    │                   │ ReturnDirect
                    └──final──→ continue ←┘
                                  ├──input──→ prepare
                                  └──empty──→ finish → END
```

```text
core/
  graph/                 agent.go graph.go model.go tools.go collector.go config.go
  runtime/agentthread/   thread.go run.go events.go context.go
  runtime/checkpointer/
  internal/conversation/ conversation.go compact.go usage.go
  types/                 run.go call.go event.go
  tools/                 registry.go policy.go filesystem.go command.go interaction.go
                         skill.go subagent.go web.go
  middleware/            middleware.go prompt.go model.go tool.go observe.go
  backend/
  memory/
  mcp/
  modelhub/
```

```go
// 生命周期与归属
type DeepAgentThread struct {
    ThreadID string
    mu sync.Mutex // SubmitInput accepted 与结束判断使用同一把锁
    current *run
    pending []types.Input
    conversation Conversation
    events chan Event
    config *RunConfig
}
type run struct {
    id string
    agent *graph.DeepAgent
    cancel context.CancelCauseFunc
    done chan struct{}
    mu sync.Mutex
    err error
    consumed []types.Input
}
func (r *run) execute(context.Context) error
func (r *run) interrupt(InterruptOptions)
func (r *run) wait(context.Context) error

type RunHandle struct { owner *DeepAgentThread; run *run }
func (h *RunHandle) RunID() string
func (h *RunHandle) Wait(context.Context) error
func (h *RunHandle) IsActive() bool
func (h *RunHandle) ConsumedInputs() []*schema.Message
func (h *RunHandle) ConsumedInputsMeta() []any
```

```go
type DeepAgent struct {
    cfg Config
    graph compose.Runnable[*types.RunState, *schema.Message]
    conversation Conversation
    registry *tools.Registry
    executor *toolExecutor
    emit func(context.Context, types.RuntimeEvent) error
    drainInput func(context.Context, string) ([]types.Input, bool, error)
    middlewares []middleware.Middleware
}
func New(context.Context, ...Option) (*DeepAgent, error)
func (a *DeepAgent) Run(context.Context, []*schema.Message, ...RunOptionFunc) (*schema.Message, error)
func (a *DeepAgent) Stream(context.Context, []*schema.Message, ...RunOptionFunc) (*schema.StreamReader[*schema.Message], error)
func (a *DeepAgent) execute(context.Context, []*schema.Message, *ResumeOptions, ...RunOptionFunc) (*schema.Message, error)
func (a *DeepAgent) buildGraph(context.Context) error
func (a *DeepAgent) prepare(context.Context, *types.RunState) (*types.RunState, error)
func (a *DeepAgent) callModel(context.Context, *types.RunState) (*types.RunState, error)
func (a *DeepAgent) callTools(context.Context, *types.RunState) (*types.RunState, error)
func (a *DeepAgent) continueRun(context.Context, *types.RunState) (*types.RunState, error)
func (a *DeepAgent) finish(context.Context, *types.RunState) (*schema.Message, error)
// Close / Interrupt / GraphState / GetGraphRunnable / Depth / Name 保留。
```

```go
type Descriptor struct {
    Tool einotool.BaseTool
    ReadOnly, RequiresApproval, ParallelSafe, ReturnDirect bool
    NormalizeArgs func(string) (string, error)
}
type Registry struct { entries map[string]Descriptor }
func NewRegistry(context.Context, []Descriptor) (*Registry, error)
func (r *Registry) Lookup(string) (Descriptor, bool)
func (r *Registry) ModelTools(context.Context) ([]*schema.ToolInfo, error)
func (r *Registry) Filter(context.Context, bool, Mask) (*Registry, error)
func (r *Registry) RewriteInfo(context.Context, ToolInfoRewriter) error

type Policy interface { Decide(context.Context, types.ToolCall, Descriptor) (Decision, error) }
type Decision struct { Action Action; Reason string } // allow / deny / ask_approval

// 私有执行账本，key = RunID + CallID。
func (e *toolExecutor) execute(context.Context, types.ToolCall, *types.ResumeAnswer, types.ToolChunkSink) (*types.ToolResult, error)
func (e *toolExecutor) executeBatch(context.Context, []types.ToolCall, types.ToolChunkSink) ([]types.ToolResult, error)
func (e *toolExecutor) cancel(context.Context) error
// running 等待；completed 复用；outcome_unknown 禁止自动重放副作用。
// ParallelSafe 分组并发，其他工具形成 barrier；持久化按 Call.Index 排序。
// eager 与 tools 节点必须使用这个账本。
```

```go
type Conversation interface {
    ReloadHistory(context.Context) error
    AddHistory(context.Context, string, ...*schema.Message) error
    History(context.Context) []*schema.Message
    BuildRequest(context.Context, []*schema.Message) ([]*schema.Message, error)
    ContextUsage() ContextUsageSnapshot
    RecordModelUsage(context.Context, *model.TokenUsage)
    RunUsage() types.Usage // 当前 Run 累计；与 ContextUsage 的上下文占用分开
    RestoreRunUsage(context.Context, types.Usage) error // 新 Run 清零 / checkpoint 恢复
    CompactNeeded(context.Context) bool
    Compact(context.Context, string) (*ContextCompactedPayload, error)
}
// store 成功 → 更新内存；MessageID 去重。
// compact: snapshot → 解锁计算 → 校验 SourceVersion → store → 提交。
// Conversation 是在线 usage 唯一真源。
```

```go
// 保留原 Middleware 公共契约，新增能力用类型断言检测，禁止新 pipeline。
type RunMiddleware interface {
    BeforeRun(context.Context, *types.RunState) error
    AfterRun(context.Context, *types.RunState, error) error
}
type EventObserver interface { Observe(context.Context, types.RuntimeEvent) error }
// Prompt: Base → Skill → Memory → others
// Model: LoopGuard → PlanReminder → Retry → CircuitBreaker → model
// Tool: ToolErrors → ToolPolicy → Approval → actual tool
// 可变 middleware 每 Run 独立，AfterRun 恰好一次。
```

```text
Checkpoint: Envelope{Version, ThreadID, RunID, GraphVersion, EinoSnapshot}
RunState: Eino local state；禁止独立 sidecar。
Resume: 校验 envelope → Eino Resume → 恢复 RunState/挂起工具 → 原节点继续。
Event: Sequence 递增 → 最后事件发送 → inactive → Wait 返回。
Host: SaveOutput → Yield → ReleaseThread。
ChildRunner: 同一 DeepAgent Graph，独立上下文、budget、middleware。
Manager: spawn_task/send_message/wait_message/close_task 留在 adapter 层。
Memory: Read/Observe/Consolidate；模型工作复用 DeepAgent。
MCP: discover → Descriptor → Registry。
ModelHub: OpenAI-compatible / Ark / Kimi / Claude → ToolCallingChatModel。
```

```text
最终工具：
list_files read_file write_file edit_file delete_file glob grep rg
semantic_search read_lints apply_patch execute shell await_shell
ask_user update_plan activate_skill task web_search read_url

参数在 Tool 边界归一化；工具依赖 Workspace/CommandService，直接实现 Eino 工具接口。
保留旧名称与参数兼容，迁移完成前不删除旧能力。
```

实施与验收见 [执行清单](../plans/2026-09-23-core-rewrite.md)。目标不是文件搬迁；新实现行为验证通过后才切换 ThreadHost，并删除旧执行路径。
