# Todo (Plan Mode) 功能 — 调研 & LLM 仓技术方案

> 调研对象: `/Users/bytedance/PycharmProjects/deer-flow`(LangChain 0.x + LangGraph 栈)
> 落地对象: 本仓 `eino-cli`(eino `adk` 栈,prebuilt deep agent + middleware)

目标:
1. **调研**deer-flow 里 todo 这一套(write_todos tool / plan mode / 提醒 / 前端)是怎么端到端串起来的。
2. **对账**eino 自带的 `write_todos` 与 `WithoutWriteTodos` 是 deer-flow/LangChain 体系里哪一段的等价物;现在差什么。
3. **落地方案**: 在 LLM 仓里把 deer-flow 里值得搬过来的几条增量补齐,同时尊重
   `AGENTS.md`(少 indirection、struct 只装数据、注释只回答 why、跟现有 trace/clarification 风格一致)。

> 写实现的人可以直接照 §4 + §5 + §7 走;§2、§3 是为了让 review 的时候不必再回去翻 deer-flow 源码。

---

## 0. 决策已锁定(待人确认即可开工)

| 项 | 决策 | 备注 |
|---|---|---|
| 工具实现 | **复用 eino prebuilt `write_todos`**,不另造 | 名字、入参 schema、session 写入都对得上 deer-flow,不要重复发明 |
| 触发条件 | **plan mode 开关 + 一律开 tool**(默认 on,不再受 `IsPlanMode` 守门) | 跟 deer-flow 早期版本一致;eino 的 `WithoutWriteTodos` 默认就是 false,无需改 |
| Plan mode 提示 | **只有 plan mode 时把 `<plan_mode>` system prompt 追加** | 当前 `middlewares/todo.go` 已是这个模型,继续沿用 |
| Plan mode 入口 | **新增 `/plan [on/off/toggle]` REPL slash 命令** + `RuntimeContext.IsPlanMode` | yaml 里那个被注释的 `is_plan_mode` 这一期不解锁,会跟 model/agent 多源决策耦合 |
| Reminder | **summarization 后 todos 还在 session 但 history 里看不到 write_todos 时,注入 reminder 系统消息** | deer-flow 用 `HumanMessage(name="todo_reminder")`,Go 这边走 `schema.SystemMessage` 即可 |
| Premature-exit guard | **本期不做** | eino ReAct 主图 `chatModel → ToolCalls? → END` 没有 deer-flow 的 `jump_to: "model"` 等价钩子,强行实现要改图。降级方案见 §4.4 |
| 多次 `write_todos` 并行 | **本期不做** | eino 现版没看到 model 同一轮会并发吐多个相同 tool call,先观察。LangChain 那边的 guard 可后续补 |
| TUI 渲染 | **`/clear` 已有的 hook 之外,加一个 todo 面板**: `Trace` 在 AfterModel 之后回读 `SessionKeyTodos`,作为新的 `DebugEvent` 兄弟事件流出来 | 一句话:不要新发明事件管道,沿用现有的 `Trace → DebugConsumer` |
| 持久化 | **仅依赖 eino session(checkpoint 自带)**;不写文件 | todos 跟 message history 是同一份 thread state;失去 thread 就该失去 todos,符合"per-task plan"的语义 |

---

## 1. 背景与现状

### 1.1 当前 LLM 仓里和 todo 相关的代码

```20:30:backend/agent/lead_agent.go
deepCfg := &deep.Config{
    Name:                   rt.AgentName,
    Description:            "Deep Agent",
    ChatModel:              chatModel,
    Instruction:            prompt,
    MaxIteration:           defaultIterationLimit(rt.AgentConfig),
    WithoutGeneralSubAgent: !rt.SubagentEnabled,
    WithoutWriteTodos:      false,
    Middlewares:            GetAgentMiddleWares(rt),
    Handlers:               handlers,
}
```

```26:30:backend/agent/middlewares/todo.go
// NewTodo returns the AgentMiddleware that adds the plan-mode preamble.
// Only attach when RuntimeContext.IsPlanMode is true.
func NewTodo() adk.AgentMiddleware {
    return adk.AgentMiddleware{
        AdditionalInstruction: TodoInstruction,
    }
}
```

```48:51:backend/agent/runtime_config.go
isPlanMode := false //todo cli 传进来
if seed != nil && seed.IsPlanMode {
    isPlanMode = true
}
```

也就是说,本仓现在是 **"半通"** 状态:
- ✅ `write_todos` tool 由 eino prebuilt 自动注册(`WithoutWriteTodos: false`),tool 调用结果写到 `adk.SessionKeyTodos = "deep_agent_session_key_todos"`。
- ✅ Plan mode preamble 也写好了(`middlewares/todo.go` 的 `TodoInstruction`)。
- ❌ **CLI 没有任何渠道把 `IsPlanMode` 翻成 true**——`NewDeepAgentRuntime` 调 `agent.NewRuntimeContext(cfg, nil)`,seed 永远是 nil。
- ❌ **TUI 不读 todos**:`SessionKeyTodos` / `GetSessionValue` 在整个仓里零引用。
- ❌ **没有 reminder**:summarization 一旦把 `write_todos` 的 ToolMessage 截出 history,model 就忘了它写过什么。

### 1.2 eino prebuilt deep agent 自带的部分(以下来自 `eino@v0.9.0-alpha.17/adk/prebuilt/deep`)

```go
// deep/deep.go:198-231 (节选) — 概念再现,非引用
type TODO struct {
    Content    string `json:"content"`
    ActiveForm string `json:"activeForm"`
    Status     string `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=completed"`
}

func newWriteTodos() (adk.ChatModelAgentMiddleware, error) {
    t, err := utils.InferTool("write_todos", toolDesc, func(ctx context.Context, input writeTodosArguments) (string, error) {
        adk.AddSessionValue(ctx, SessionKeyTodos, input.Todos)
        ...
    })
    ...
    return buildAppendPromptTool("", t), nil
}
```

要点:
- tool 的入参/出参 schema 跟 LangChain `write_todos` 几乎完全一致(`content/status`,加了一个 `activeForm` 在 LangChain 里没有)。
- todos 持久化路径 = **eino session value**,key 是 `SessionKeyTodos`。session value 跟着 `RunOption.CheckPointID` 一起进 checkpoint,刚好对应 deer-flow 那边 `ThreadState.todos` 的语义——**per-thread、随 thread 复活**。
- 这块 tool description / `WRITE_TODOS_SYSTEM_PROMPT` 是 eino 内置(英文 + 中文双语)。我们目前不覆盖,跟 deer-flow 现版一致("plan mode preamble"是另一段额外文本,而非替换默认描述)。

---

## 2. deer-flow 实现剖析

### 2.1 整体拓扑

```
make_lead_agent(config)
  │
  └── _build_middlewares(config)
        ├─ ThreadDataMiddleware
        ├─ SandboxMiddleware
        ├─ DanglingToolCallMiddleware
        ├─ SummarizationMiddleware           (会截 history → 触发 §2.5 reminder)
        ├─ TodoMiddleware (only if is_plan_mode=True)   ← 关键
        ├─ TitleMiddleware / MemoryMiddleware
        └─ ClarificationMiddleware
```

`TodoMiddleware` 继承自 LangChain 的 `langchain.agents.middleware.TodoListMiddleware`。LangChain 包里的部分负责"功能本体",deer-flow 自己继承的部分负责"补 reminder + 阻止过早退出"。下面分两层讲。

### 2.2 LangChain 上游:`TodoListMiddleware`(功能本体)

`.venv/lib/python3.12/site-packages/langchain/agents/middleware/todo.py`:

| 部件 | 职责 |
|---|---|
| `Todo` TypedDict | `{content: str, status: "pending"|"in_progress"|"completed"}`(LangChain 这版**没有 activeForm**) |
| `PlanningState.todos` | LangGraph state 里加一个 `todos: list[Todo]` 字段(`Annotated[..., OmitFromInput]`,即不暴露给 model 当输入的"内部状态") |
| `write_todos` tool | 同名 StructuredTool,行为是 `Command(update={"todos": todos, "messages": [ToolMessage(f"Updated todo list to {todos}", tool_call_id=...)]})`——一次性**整体覆盖** todos |
| `wrap_model_call` | 给 system message 末尾追加一段 `WRITE_TODOS_SYSTEM_PROMPT`(讲什么时候用、什么时候不用) |
| `after_model` | **检查并阻止 parallel `write_todos` 调用**:同一个 AI message 里出现 ≥2 个 `write_todos` tool_call → 给每个返回 error ToolMessage(整个 todos 是一次性覆盖式更新,并行的话谁覆盖谁就语义不清) |

注意几点:
- LangChain 把 `tool_description` 和 `system_prompt` 都开放成构造参数,允许 deer-flow 这种下游覆盖。
- `todos` 这个 state 字段是从 langgraph reducer 写进 thread state 的——**前端 / API 拉 thread state 就能拿到**,这是渲染面板的核心机制。
- 没有持久化文件;todos 是 thread state 的一部分,跟着 langgraph checkpoint 走。

### 2.3 deer-flow 的覆盖:`TodoMiddleware`(补两个增强)

`backend/packages/harness/deerflow/agents/middlewares/todo_middleware.py`:

```python
class TodoMiddleware(TodoListMiddleware):
    def before_model(self, state, runtime):
        # 1. todos 还在 state 里
        # 2. 但 history 里已经看不到 write_todos 的 AIMessage(被 SummarizationMiddleware 裁了)
        # 3. 之前没插过 todo_reminder
        # → 注入一条 HumanMessage(name="todo_reminder"),把当前 todos 用 markdown bullet 渲染回去

    @hook_config(can_jump_to=["model"])
    def after_model(self, state, runtime):
        base = super().after_model(state, runtime)  # parallel write_todos guard
        if base: return base

        # model 想退出(无 tool_calls)+ 还有 incomplete todos + completion_reminder 累计 < 2
        # → 插一条 HumanMessage(name="todo_completion_reminder") + jump_to="model"
        # 强制再走一轮 model node
```

关键设计点:
- **两个不同的 reminder 用 `HumanMessage.name` 区分**(`todo_reminder` vs `todo_completion_reminder`),用于幂等检测和上限计数。
- **`jump_to="model"` 是 LangGraph 的 hook 返回值约定**,会跳过常规的 `next_node` 分支直接回到 `model` node。eino 里**没有这个能力**(见 §4.4)。
- **completion reminder 上限 2 次**(`_MAX_COMPLETION_REMINDERS`)以防死循环;到顶就放 model 退出。

### 2.4 自定义 prompt(plan mode 风格)

deer-flow 的 `_create_todo_list_middleware(is_plan_mode)`:
- **只在 `is_plan_mode=True` 时返回 middleware,否则返回 None**——所以一旦关 plan mode,**`write_todos` tool 也不会被注册**,model 完全不会知道它存在。
- **传入自定义 `system_prompt` + `tool_description`**,覆盖 LangChain 默认值。这个 system_prompt 用 `<todo_list_system>` XML tag 包裹,跟 deer-flow 主 prompt 风格对齐;**核心规则**(立即标 completed、in_progress 只一项、< 3 步不用)写在 `**CRITICAL RULES:**` 段。

### 2.5 plan mode 开关

`_get_runtime_config(config)["is_plan_mode"]`,从 `RunnableConfig.configurable["is_plan_mode"]` 直接读,**per-request 决定**。前端/客户端把布尔扔进 RunnableConfig 即可,后端不持有任何全局 plan-mode 状态。

### 2.6 前端怎么吃 todos

```typescript
// frontend/src/app/workspace/chats/[thread_id]/page.tsx
<TodoList
  className="bg-background/5"
  todos={thread.values.todos ?? []}
  hidden={!thread.values.todos || thread.values.todos.length === 0}
/>
```

```typescript
// frontend/src/components/workspace/todo-list.tsx
{todos.map((todo, i) => (
  <QueueItem ...>
    <QueueItemIndicator
      className={todo.status === "in_progress" ? "bg-primary/70" : ""}
      completed={todo.status === "completed"}
    />
    <QueueItemContent ...>{todo.content}</QueueItemContent>
  </QueueItem>
))}
```

底层数据来源:LangGraph 的 thread state(`thread.values.todos`)。前端订阅了 thread,任意一次 `write_todos` tool call 完成后 graph state 更新,前端组件 props 同步更新。

外加在 chain-of-thought 里,`tool_call.name === "write_todos"` 时显示一个紧凑的"Update to-do list / 更新 To-do 列表"图标行(`ListTodoIcon`),让用户在事件流里也能看到 model "更新过 todos"这件事。

### 2.7 deer-flow 实现一句话总结

> **LangChain 提供 tool + state 字段 + system prompt + parallel guard;deer-flow 在它上面套了 reminder + premature-exit guard,plan mode 开关在 RunnableConfig.configurable;前端从 thread state 拉 todos 数组直接渲染。**

---

## 3. eino 这边能对到哪一层(差距清单)

| deer-flow / LangChain 部件 | eino 现成对应 | 差距 |
|---|---|---|
| `Todo` TypedDict | `deep.TODO` struct | 一致(eino 多了个 `activeForm`,无害) |
| `PlanningState.todos` 字段 | `adk.SessionKeyTodos` session value | 一致(都是 per-thread state) |
| `write_todos` tool 注册 | `deep.newWriteTodos()` + `WithoutWriteTodos` 开关 | 一致(默认就开) |
| `WRITE_TODOS_SYSTEM_PROMPT` | eino 内置默认 description(已附在 tool 上) | 一致;**plan-mode 风格的额外 preamble 走 `AdditionalInstruction`**,不覆盖 tool description |
| `wrap_model_call` 注入 prompt | `AdditionalInstruction`(`AgentMiddleware.AdditionalInstruction`) | 一致(`middlewares/todo.go` 已实现) |
| `after_model` parallel guard | ❌ 无 | 暂不补,见 §0 表 |
| `before_model` reminder | ❌ 无 | **§4.3 补** |
| `after_model` premature-exit + `jump_to: "model"` | ❌ 无对应 hook | **§4.4 降级,本期不做** |
| RunnableConfig.configurable.is_plan_mode | `RuntimeContext.IsPlanMode` 字段已有,**但 wiring 没接通** | **§4.2 补**(slash 命令 `/plan` + 重建 lead agent) |
| 前端 thread state.todos | TUI 里完全没读 | **§4.5 补** |

---

## 4. LLM 仓实现方案

整体思路:**最小增量,顺着现有 trace / clarification / debug 那套写法**。新增的代码体量预计 ~150 行。

### 4.1 数据流

```
用户输入 "/plan on"             → tui.handleBuiltin
                                 → 设置 m.runtime.SetPlanMode(true) 并重建 lead agent
                                 → RuntimeContext.IsPlanMode = true
                                 → middlewares.NewTodo() 挂上 → Instruction 末尾追加 <plan_mode> 段

用户输入 "build feature X"      → adk.Runner.Run
                                 → eino 自带 write_todos tool 注册(IsPlanMode 与否都注册,但只有 plan mode 时模型被 prompt 鼓励调用)
                                 → model 调 write_todos
                                 → adk.AddSessionValue(ctx, SessionKeyTodos, ...)

后续每一轮 AfterModelRewriteState → middlewares.NewTodoReminder()
                                 (1) 拿 SessionKeyTodos
                                 (2) 看 state.Messages 里最后一次 write_todos AssistantMessage 还在不在
                                 (3) 不在 → state.Messages 头部插一条 SystemMessage 提醒(类比 deer-flow 的 todo_reminder)

TUI 渲染                         → middlewares.Trace.AfterModelRewriteState 现存逻辑里多 emit 一个 DebugEvent{Phase: DebugTodos, ...}
                                 → tui/update.go 收事件 → 顶部画一个折叠面板("To-dos: 1/4 completed")
```

### 4.2 Plan mode 入口

#### 4.2.1 RuntimeContext 接通

`runtime_config.go` 已经有字段,只是 seed 没传。改两行:

```48:51:backend/agent/runtime_config.go
isPlanMode := false //todo cli 传进来
if seed != nil && seed.IsPlanMode {
    isPlanMode = true
}
```

→

```go
isPlanMode := false
if seed != nil {
    isPlanMode = seed.IsPlanMode
}
```

(把 `//todo` 注释删了,seed 一定是有意覆盖,无需"truthy override"语义。)

#### 4.2.2 `DeepAgentRuntime` 接受 seed

`backend/runtime/eino/deep_runtime.go:33` 现在硬编码 `agent.NewRuntimeContext(cfg, nil)`,改成可选 seed:

```go
type Options struct {
    PlanMode bool
}

func NewDeepAgentRuntime(ctx context.Context, cfg *config.Config, opts Options) (Runtime, error) {
    seed := &agent.RuntimeContext{IsPlanMode: opts.PlanMode}
    runtimeCtx, err := agent.NewRuntimeContext(cfg, seed)
    ...
}
```

`Options` 故意只装 plan mode 一个字段——目前不需要别的。如果 caller 不关心,可以传 `Options{}`。

#### 4.2.3 `/plan` slash 命令

deer-flow 是把 plan mode 塞进每次 RunnableConfig,前端控制。CLI 这边没那么多客户端,**最贴近 user 直觉的就是一个 toggle slash 命令**,跟 `/debug` 同形态:

```go
// backend/cli/tui/update.go, handleBuiltin 里多一 case
case "plan":
    return m.handlePlanCmd(text), true

// handlePlanCmd: 解析 on/off/toggle,调 m.rt.SetPlanMode(...) → 内部
//  1. 重新跑 NewRuntimeContext(seed= IsPlanMode=...)
//  2. agent.MakeLeadAgent 重建 lead agent + trace
//  3. 重置 adk.Runner(沿用 NewDeepAgentRuntime 里的 runner 构造)
//  4. ClearHistory()(避免拿旧 plan mode 下的 history 喂新 agent)
// 在面板顶部 push 一行系统消息: "plan = on / off"
```

实现侧改动:
- `backend/runtime/eino/deep_runtime.go` 暴露 `SetPlanMode(plan bool) error` 方法,内部加锁重建 `runner` / `trace`。
- `backend/cli/tui/model.go` 在 `builtinHelp` 里加 `/plan [on|off|toggle]` 一行。

> 这个能力的代价是 **重启一个 agent + 失去当前 history**——这是 plan mode 的语义(进入 plan mode = 开始一个新规划的 task),不是 bug。文档里要明确写。

#### 4.2.4 yaml 字段(本期不做)

`yaml/config.yaml` 里有个被注释掉的 `is_plan_mode: false`,deer-flow 那边走 RunnableConfig 而非 yaml,**留它注释着即可**;真正激活的话会跟 `/plan` 命令的优先级、跟 `agent.yaml` 的 plan mode 字段(如果以后加)产生多源决策矛盾。这一期只走 `/plan`。

### 4.3 Reminder middleware(`middlewares/todo_reminder.go`)

和 `Trace` 同形态,放在 `backend/agent/middlewares/`:

```go
package middlewares

import (
    "context"

    "github.com/cloudwego/eino/adk"
    "github.com/cloudwego/eino/schema"

    "github.com/cloudwego/eino/adk/prebuilt/deep"
)

const todoReminderTag = "<system_reminder type=\"todo\">"

// TodoReminder injects a system reminder when the in-flight todo list is in
// SessionKeyTodos but the original write_todos AIMessage was scrubbed from
// state.Messages (e.g. by SummarizationMiddleware). Mirrors deer-flow's
// todo_reminder HumanMessage but uses SystemMessage because eino has no
// HumanMessage.name discriminator.
type TodoReminder struct {
    *adk.BaseChatModelAgentMiddleware
}

func NewTodoReminder() *TodoReminder {
    return &TodoReminder{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}}
}

func (m *TodoReminder) BeforeModelRewriteState(
    ctx context.Context,
    state *adk.ChatModelAgentState,
    _ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
    if state == nil {
        return ctx, state, nil
    }

    raw, ok := adk.GetSessionValue(ctx, deep.SessionKeyTodos)
    if !ok {
        return ctx, state, nil
    }
    todos, _ := raw.([]deep.TODO)
    if len(todos) == 0 {
        return ctx, state, nil
    }
    if hasWriteTodosCall(state.Messages) || hasReminderTag(state.Messages) {
        return ctx, state, nil
    }

    state.Messages = append([]*schema.Message{
        schema.SystemMessage(renderTodoReminder(todos)),
    }, state.Messages...)
    return ctx, state, nil
}
```

辅助函数:
- `hasWriteTodosCall(msgs)` 倒序扫,检查 `msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 && tc.Function.Name == "write_todos"`。
- `hasReminderTag(msgs)` 倒序扫,检查 `msg.Role == schema.System && strings.Contains(msg.Content, todoReminderTag)`。**幂等检测靠 tag 字符串**,不引入新字段(对应 deer-flow 用 `name="todo_reminder"`)。
- `renderTodoReminder(todos)` 输出形如:

```
<system_reminder type="todo">
Your todo list from earlier is no longer in the visible context, but it is
still active. Current state:

- [in_progress] Refactor lead_agent
- [pending]     Add reminder middleware
- [pending]     Wire /plan command

Continue tracking and updating this list as you work. Call `write_todos`
whenever a status changes.
</system_reminder>
```

> 用 `SystemMessage` 而非 `UserMessage` 的原因:eino schema 没有 `name` 字段做 discriminator,而 user-role 系统提醒和真实用户输入混在一块儿会让 model 困惑;system role + 明确的 XML tag 是更安全的等价物。

#### 4.3.1 Wiring

`GetChatModelMiddlewares` 里挂上,**plan mode + 非 plan mode 都挂**——deer-flow 的 reminder 也是 plan mode 必须开,但因为我们 tool 一直注册(eino 默认),非 plan mode 下 model 也可能调 write_todos,reminder 一并保护没坏处:

```114:114:backend/agent/middleware_chain.go
middlewareList = append(middlewareList, middlewares.NewTrace(rt.AgentName))
middlewareList = append(middlewareList, middlewares.NewClarification())
```

→

```go
middlewareList = append(middlewareList, middlewares.NewTodoReminder())
middlewareList = append(middlewareList, middlewares.NewTrace(rt.AgentName))
middlewareList = append(middlewareList, middlewares.NewClarification())
```

放在 Trace **之前**,这样 Trace 看到的"插入了 reminder"在 debug 流里会被记录成 BeforeModel 阶段的一条消息,不会被静默吞掉。

### 4.4 Premature-exit guard 暂不做

deer-flow 的 `after_model` 走 `Command(jump_to="model", update={"messages": [reminder]})`,LangGraph 有现成 `can_jump_to` 协议。

eino ReAct 主图(`adk/react.go`):

```452:454:vendor/eino/adk/react.go
if len(chunk.ToolCalls) > 0 {
    return cancelCheckNode_, nil
}
return compose.END, nil
```

——`chatModel` 节点输出无 tool_calls 时 branch 直接 → `compose.END`。要在 `AfterModelRewriteState` 里硬转回 `chatModel` node,只能改 prebuilt 图,代价大且会跟 eino 上游版本演化死锁。

降级策略(本期):
1. 在 `<plan_mode>` preamble 里强化"在 todos 全部 completed 之前不要给最终回复"这条规则,靠 prompt 约束。
2. **观察上限**: 在 trace 里统计"final reply 时 incomplete todos > 0"事件,超过经验值再决定要不要硬撑。

### 4.5 TUI 渲染

#### 4.5.1 选哪条管道传 todos

两个候选:
- **A. 复用 Trace + DebugConsumer**: 现有 `Trace.AfterModelRewriteState` 里多读一次 session,Send 一个新 phase 的 `DebugEvent`,TUI 在 `update.go` 里多一个 case。
- B. 独立的 `TodoConsumer` + 自己一套 ctx-based 注入。

选 **A**:
- 复用现有 ctx 注入路径(`WithDebugConsumer`),不用再发明一个;
- TUI 那边的事件 dispatch 是同一个 channel,顺序天然对齐 BeforeModel/AfterModel;
- 改动量最小。

具体改动:

```go
// backend/agent/middlewares/debug.go 新增 phase 常量
const (
    DebugBefore = iota + 1
    DebugAfter
    DebugTodos
)

type DebugEvent struct {
    AgentName string
    Phase     int
    Turn      int
    Messages  []*schema.Message
    Todos     []deep.TODO  // only set when Phase == DebugTodos
}
```

`Trace.AfterModelRewriteState` 末尾追加:

```go
if raw, ok := adk.GetSessionValue(ctx, deep.SessionKeyTodos); ok {
    todos, _ := raw.([]deep.TODO)
    if len(todos) > 0 {
        consumer.Send(DebugEvent{
            AgentName: t.agentName,
            Phase:     DebugTodos,
            Turn:      int(t.turn.Load()),
            Todos:     todos,
        })
    }
}
```

> 注意:`Todos` 字段只在 `DebugTodos` 时填,其它两个 phase 留空——这跟 `Messages` 字段在不同 phase 语义不同(Before 是 full slice、After 是 single delta)是一样的,struct 同一份按 phase 解释,符合
> AGENTS.md 的"struct 装数据,不为单一字段炸新结构"。

#### 4.5.2 渲染

`backend/cli/tui/update.go` 的 `case middlewares.DebugAfter:` 旁边再加一个:

```go
case middlewares.DebugTodos:
    m.todos = ev.Todos          // 缓存最新一份
    return m, nil
```

`backend/cli/tui/view.go` 在主消息列表上方画一个紧凑列表(高度固定 1 行 header + 最多 5 行 todos):

```
─── Todos (2/5) ─────────────────────────
  ✓ Read deer-flow source
  ✓ Compare with eino prebuilt
  → Write reminder middleware       (in_progress)
  ○ Wire /plan slash command
  ○ Update tests
─────────────────────────────────────────
```

折叠/展开靠回车或 `/todos` slash 命令(同 `/debug` 模式)——视图细节不锁,UX 调到舒服为止。

### 4.6 前端 / API(N/A)

仓里目前只有 TUI,没有 web 前端;deer-flow §2.6 那段 React 组件不在我们的范围。如果以后接 web,直接从 trace stream 同一管道把 `DebugTodos` 事件转 SSE 即可,本期不留接口。

---

## 5. 落地任务清单(分 commit)

> 参考 AGENTS.md "Commit 粒度":纯重命名 / 摘中间层 / 加新功能 各拆开。

### Commit 1 — `runtime: thread plan-mode seed end-to-end`

文件:
- `backend/agent/runtime_config.go`(去掉 `//todo` 注释,seed 直接覆盖)
- `backend/runtime/eino/deep_runtime.go`(`NewDeepAgentRuntime` 增加 `Options{PlanMode bool}` 参数;新增 `SetPlanMode(bool)` 方法;内部 mutex + 重建 `runner` / `trace`)
- `backend/cli/main.go` / TUI 入口(传 `Options{}` 占位,行为不变)
- 更新对应 `_test.go`

验证:`go build ./... && go test ./agent/... ./runtime/...` 全绿,plan mode 行为没变(seed 仍可空)。

### Commit 2 — `cli: add /plan slash command`

文件:
- `backend/cli/tui/update.go`(`handleBuiltin` 加 `case "plan"`,新增 `handlePlanCmd`)
- `backend/cli/tui/model.go`(`builtinHelp` 文案;`Model` 字段加 `planMode bool`)
- `backend/cli/tui/debug_format_test.go`(扩展 `/help` 必含 `/plan` 检查)
- 新增 `backend/cli/tui/plan_test.go`(toggle / on / off 三态)

验证:跑 TUI,`/plan on` 后 `/help` 文案有 `plan = on`;再问一个多步任务,model 调 `write_todos`。

### Commit 3 — `middlewares: todo reminder on context loss`

文件:
- `backend/agent/middlewares/todo_reminder.go`(本文 §4.3 全部代码)
- `backend/agent/middlewares/todo_reminder_test.go`(下面 §6.1 用例)
- `backend/agent/middleware_chain.go`(在 `NewTrace` 之前 append `NewTodoReminder()`)
- `backend/agent/middleware_chain_test.go`(顺序断言加一项)

验证:用 §6.1 的 unit test。

### Commit 4 — `tui: render todo panel from trace stream`

文件:
- `backend/agent/middlewares/debug.go`(新增 `DebugTodos` 常量 + `DebugEvent.Todos` 字段)
- `backend/agent/middlewares/debug_test.go`(覆盖 Trace 在 sessionvalue 存在时 emit todos 事件)
- `backend/cli/tui/update.go` / `view.go`(新增 case + 渲染)
- `backend/cli/tui/model.go`(`Model.todos []deep.TODO`)

验证:开 TUI 跑一个多步任务,看顶部 todo 面板实时更新。

> **不在本期**: parallel `write_todos` guard / premature-exit guard / yaml `is_plan_mode` 解锁 / web 前端管道。理由见 §0 表 + §4.4。

---

## 6. 测试计划

### 6.1 `todo_reminder_test.go`

| 用例 | 期望 |
|---|---|
| session 没 todos | 不注入 |
| session 有 todos,history 包含 `write_todos` AssistantMessage | 不注入 |
| session 有 todos,history 不含 `write_todos`(模拟 summarization 截断后) | 在 messages[0] 插入 SystemMessage,内容含 `<system_reminder type="todo">` 和每条 todo 的 status |
| 同上但 history 已含同 tag SystemMessage | 不重复注入(幂等) |
| state == nil | 不 panic,直接返回 |

工具:用 `deep.SessionKeyTodos` 真 key,通过 `adk.AddSessionValue` 准备数据;`state.Messages` 用 `schema.AssistantMessage(...)` 直接造。

### 6.2 `runtime_config_test.go`(扩 1 用例)

`NewRuntimeContext(cfg, &RuntimeContext{IsPlanMode: true})` → 返回的 RuntimeContext.IsPlanMode == true。

### 6.3 `deep_runtime_test.go`(新增)

`SetPlanMode` 不并发安全是 bug:跑 100 个 goroutine 各调一次 `SetPlanMode(rand)`,期望 race detector 干净 + 最终 plan mode 是最后一次写入的值。

### 6.4 TUI 集成测试

跟 `debug_format_test.go` 同形态,断言 `/help` 包含 `/plan`,`handlePlanCmd("/plan on")` 后 `m.planMode == true`,`handlePlanCmd("/plan off")` 反之。

### 6.5 手动验证清单

启 TUI(默认 OFF):
- [ ] `/plan on` 后系统消息 `plan = on`,history 清空。
- [ ] 输入"refactor 三个文件" → model 应该调 `write_todos` 并把 status 标 in_progress。
- [ ] 顶部 todo 面板出现。
- [ ] 完成第一项后 model 调一次 `write_todos`,面板第一项 ✓。
- [ ] 强行制造长 history(repeated `/help` + 长 prompt 凑量)直到 summarization 触发,继续追问 → reminder 应被注入(开 `/debug` 看 BeforeModel 头一条 SystemMessage)。
- [ ] `/plan off` → 系统消息 `plan = off`,history 清空,新对话不再有 plan-mode preamble。

---

## 7. 跟 `AGENTS.md` 对齐说明

- **结构体只装数据**:`DebugEvent.Todos` 只多一个字段不新建子类型;`TodoReminder` 只持有 `BaseChatModelAgentMiddleware` 嵌入,无业务字段;`Options{PlanMode bool}` 单字段就一个。
- **少压调用栈**:Reminder middleware 一层、Trace 一层、TUI handler 一层——总深度 ≤ 4。
- **少传数据**:不为 reminder 单独做一个 config 子结构;不抽 `TodoConsumer` 单独的 ctx-key,复用 DebugConsumer。
- **注释只回答 why**:每个新文件最多一段顶层 doc + 一两行讲"为什么 SystemMessage 而非 HumanMessage""为什么 reminder 在 Trace 之前"。
- **Commit 粒度**:Runtime 接通 / slash 命令 / reminder middleware / TUI 渲染 各一个 commit,diff 一句话讲清。
- **变量命名以动词开头**:`hasWriteTodosCall` / `renderTodoReminder` / `getTodosFromSession`(如果抽出去的话)。

---

## 8. 参考实现位置速查

LLM 仓:
- `backend/agent/middlewares/todo.go`(已有 plan mode preamble)
- `backend/agent/middleware_chain.go`(挂载点)
- `backend/agent/runtime_config.go`(IsPlanMode 字段)
- `backend/runtime/eino/deep_runtime.go`(seed 接通点)
- `backend/cli/tui/update.go` / `model.go` / `view.go`(slash 命令 + 面板)
- `backend/agent/middlewares/debug.go`(DebugEvent 扩展点)

eino prebuilt:
- `adk/prebuilt/deep/deep.go`(`newWriteTodos`、`SessionKeyTodos`)
- `adk/prebuilt/deep/types.go`(`SessionKeyTodos = "deep_agent_session_key_todos"`)
- `adk/prebuilt/deep/prompt.go`(`writeTodosToolDescription` 中英双语)
- `adk/runctx.go`(`AddSessionValue`/`GetSessionValue`)

deer-flow 对照:
- `backend/packages/harness/deerflow/agents/middlewares/todo_middleware.py`(reminder + premature-exit)
- `backend/packages/harness/deerflow/agents/lead_agent/agent.py:118-230`(`_create_todo_list_middleware` + plan-mode prompts)
- `backend/packages/harness/deerflow/agents/thread_state.py:48`(`todos` 字段挂在 ThreadState)
- `frontend/src/components/workspace/todo-list.tsx`(渲染参考)

LangChain 上游:
- `langchain/agents/middleware/todo.py`(本体:`TodoListMiddleware` + `write_todos` tool + parallel guard)
