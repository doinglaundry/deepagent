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
| Plan mode 入口 | **新增 `/plan [on/off/toggle]` REPL slash 命令** → `(*RuntimeContext).SetPlanMode(...)` 改字段 → `DeepAgentRuntime` 用新值重建 lead agent | `seed *RuntimeContext` 这条路本期不走;切 plan mode 不应该牵动整条 `NewRuntimeContext`(重新解析 agent / model)。yaml 里那个被注释的 `is_plan_mode` 这一期不解锁,跟 `/plan` 多源耦合 |
| Reminder | **summarization 后 todos 还在 session 但 history 里看不到 write_todos 时,注入 reminder 系统消息** | deer-flow 用 `HumanMessage(name="todo_reminder")`,Go 这边走 `schema.SystemMessage` 即可 |
| Premature-exit guard | **本期不做** | eino ReAct 主图 `chatModel → ToolCalls? → END` 没有 deer-flow 的 `jump_to: "model"` 等价钩子,强行实现要改图。降级方案见 §4.4 |
| 多次 `write_todos` 并行 | **本期不做** | eino 现版没看到 model 同一轮会并发吐多个相同 tool call,先观察。LangChain 那边的 guard 可后续补 |
| TUI 渲染 | **顶部加一个 todo 面板**: `Trace` 在 AfterModel 之后回读 `SessionKeyTodos`,作为新的 `TraceEvent` phase 流出来。**默认折叠态单行**(`▶ Todos x/y · ...`),`/todos` 切换展开;completed 项走 lipgloss `Strikethrough` 划掉。`/clear` 时清面板 | 一句话:不要新发明事件管道,沿用现有的 `Trace → DebugConsumer`(连带把名字改对成 `TraceConsumer`,见 §4.5.0)|
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

## 4. LLM 实现方案

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

TUI 渲染                         → middlewares.Trace.AfterModelRewriteState 现存逻辑里多 emit 一个 TraceEvent{Phase: TracePhaseTodos, ...}
                                 → tui/update.go 收事件 → 顶部画一个折叠面板("To-dos: 1/4 completed")
```

### 4.2 Plan mode 入口

设计取向(三连贯,缺一不可):

1. **plan mode 切换是单字段开关,不应该回流到 `NewRuntimeContext` 重跑整条解析**(那条路要重新做 agent / model lookup、补默认值,白白做工)。所以让 `*RuntimeContext` 自己暴露一组 setter,`DeepAgentRuntime` 持有指针并在 setter 后**只重建 lead agent / runner / trace**。

2. **顺手把 `NewRuntimeContext` 的 `seed *RuntimeContext` 参数也去掉**——目前 seed 干两件事:(1) 让 caller 注入 `IsPlanMode` / `SubagentEnabled` / `MaxConcurrentSubagents` 这类开关;(2) 让 subagent fork 用 `subSeed.AgentName = name` 触发对新 agent 的重新解析。两件事都走 setter 更显式:**字段改写**用 `SetXxx`,**重新解析**用 `SetAgentName`(唯一会内部跑 `GetAgentConfig` + `GetModelConfig` 的 setter)。删 seed 之后 `NewRuntimeContext(cfg)` 干脆只产一份"用 `cfg.DefaultAgent` 解析出来的基线",caller 拿到后按需 setter。

3. **`RuntimeContext` 全程用 `*RuntimeContext`,不再 by-value 传递**。`NewRuntimeContext` 返回 `*RuntimeContext`;`MakeLeadAgent` / `GetSystemPrompt` / `GetChatModelMiddlewares` / `GetAgentMiddleWares` / `buildNamedSubagents` 五个函数的 `rt` 入参全改 `*RuntimeContext`。理由:既然引入了一组 setter,by-value semantics(每个 caller 一份独立 copy)只会让"我调了 setter 但 lead agent 看到的还是旧值"这种困惑变多;指针下单一所有者(`DeepAgentRuntime` 持有,其他人借用),语义干净。代价是 fork 子 agent 必须显式 `Clone()`(§4.2.5),不能再靠 value copy 隐式继承——这正好对得上"显式优于隐式"。

#### 4.2.1 `RuntimeContext`: 删 seed 参数,改用一组 setter

##### (a) 字段哪些需要 setter

| 字段 | 需要 setter? | 备注 |
|---|---|---|
| `AgentConfig` | ❌ | 内部产物,由 `SetAgentName` 间接更新;直接暴露 setter 会让两份相关字段失同步 |
| `ModelCfg` | ❌ | 同上 |
| `AgentName` | ✅ `SetAgentName(cfg, name) error` | **特殊**:同时刷新 `AgentConfig` / `ModelCfg`;失败时三个字段都不动(原子) |
| `IsPlanMode` | ✅ `SetPlanMode(plan bool)` | 单字段写 |
| `SubagentEnabled` | ✅ `SetSubagentEnabled(enabled bool)` | 单字段写 |
| `MaxConcurrentSubagents` | ✅ `SetMaxConcurrentSubagents(n int)` | 单字段写;`n <= 0` 视作"用默认 3",setter 内部 normalise(对应原 `NewRuntimeContext` 里 `> 0` 才覆盖的语义) |
| `HITLTools` | ✅ `SetHITLTools(tools []string)` | 整片替换;详见(d)slice alias 警告 |

##### (b) 新的 `NewRuntimeContext`

签名变两处(去 seed + 返回指针):

```go
// 旧
func NewRuntimeContext(cfg *config.Config, seed *RuntimeContext) (RuntimeContext, error)

// 新
func NewRuntimeContext(cfg *config.Config) (*RuntimeContext, error)
```

实现简化为:

```go
// NewRuntimeContext returns a baseline *RuntimeContext resolved from
// cfg.DefaultAgent. Callers that want to override AgentName / plan mode /
// subagent settings call SetXxx on the returned pointer.
//
// Returns *RuntimeContext (not value) because callers are expected to mutate
// it via setters; by-value would silently make those mutations local-only.
// See §4.2.1 (d) for the ownership rules.
func NewRuntimeContext(cfg *config.Config) (*RuntimeContext, error) {
    agentName := cfg.DefaultAgent

    agentConfig, err := GetAgentConfig(cfg, agentName)
    if err != nil || agentConfig == nil {
        return nil, errors.New("load agent fail")
    }
    modelCfg, err := GetModelConfig(agentConfig.Model, cfg)
    if err != nil {
        return nil, err
    }

    return &RuntimeContext{
        AgentConfig:            agentConfig,
        ModelCfg:               modelCfg,
        AgentName:              agentName,
        MaxConcurrentSubagents: 3, // baseline default; SetMaxConcurrentSubagents can override
    }, nil
}
```

(其它字段——`IsPlanMode`、`SubagentEnabled`、`HITLTools`——都用类型零值,显式默认 off / nil。)

**消费端签名同步**:

| 函数 | 旧 | 新 |
|---|---|---|
| `MakeLeadAgent` | `(ctx, rt RuntimeContext, cfg)` | `(ctx, rt *RuntimeContext, cfg)` |
| `GetSystemPrompt` | `(rt RuntimeContext, cfg)` | `(rt *RuntimeContext, cfg)` |
| `GetChatModelMiddlewares` | `(ctx, cfg, rt RuntimeContext, chatModel)` | `(ctx, cfg, rt *RuntimeContext, chatModel)` |
| `GetAgentMiddleWares` | `(rt RuntimeContext)` | `(rt *RuntimeContext)` |
| `buildNamedSubagents` | `(ctx, rt RuntimeContext, cfg, names)` | `(ctx, rt *RuntimeContext, cfg, names)` |

测试 callsite(`middleware_chain_test.go` / `middleware_chain_phase3_test.go` / `memory_e2e_test.go` / `prompt_test.go` / `subagents_test.go`)里 `RuntimeContext{...}` 字面量统一加 `&` 变 `&RuntimeContext{...}`——一次性 grep + replace 即可。

##### (c) Setter 代码骨架

全部加在 `backend/agent/runtime_config.go` 文件尾,顺序按字段重要性排:

```go
// SetAgentName switches the agent and re-resolves AgentConfig / ModelCfg so
// the three related fields stay in sync. On failure none of the three is
// modified. cfg is passed in (rather than stored on RuntimeContext) so the
// struct stays "data-only" — callers always have cfg in scope at this point
// (DeepAgentRuntime keeps a reference; subagent fork is handed cfg as an arg).
func (rt *RuntimeContext) SetAgentName(cfg *config.Config, name string) error {
    agentConfig, err := GetAgentConfig(cfg, name)
    if err != nil || agentConfig == nil {
        return errors.New("load agent fail")
    }
    modelCfg, err := GetModelConfig(agentConfig.Model, cfg)
    if err != nil {
        return err
    }
    // Atomic swap: only after both lookups succeed do we mutate fields.
    rt.AgentName = name
    rt.AgentConfig = agentConfig
    rt.ModelCfg = modelCfg
    return nil
}

// SetPlanMode flips IsPlanMode in place. Callers must guarantee no other
// goroutine is reading rt while this runs — DeepAgentRuntime owns that
// guarantee via its own mu, and lead agent code never holds onto rt after
// MakeLeadAgent returns (see §4.2.1 (e) invariants).
func (rt *RuntimeContext) SetPlanMode(plan bool)              { rt.IsPlanMode = plan }
func (rt *RuntimeContext) SetSubagentEnabled(enabled bool)    { rt.SubagentEnabled = enabled }

// n <= 0 falls back to the baseline 3, matching the old NewRuntimeContext
// "only > 0 overrides" semantics so callers can SetMaxConcurrentSubagents(0)
// to mean "reset to default".
func (rt *RuntimeContext) SetMaxConcurrentSubagents(n int) {
    if n <= 0 {
        n = 3
    }
    rt.MaxConcurrentSubagents = n
}

// SetHITLTools replaces the slice. The setter takes ownership of the passed
// slice; callers must not mutate it after handing it over (avoids aliasing
// bugs across forks — see §4.2.5).
func (rt *RuntimeContext) SetHITLTools(tools []string) { rt.HITLTools = tools }
```

##### (d) 所有权与 fork 模型(指针时代的注意点)

全程指针之后,RuntimeContext 不再有"自动 frozen snapshot"——它就是一份共享状态。规则:

- **单一所有者**:`DeepAgentRuntime` 是 RuntimeContext 的所有者,持有 `*RuntimeContext` 字段并对其字段写入(通过 `SetPlanMode` 等 setter)。所有写都在 `r.mu` 内完成。
- **借用者只读不写**:`MakeLeadAgent` / `GetSystemPrompt` / `GetChatModelMiddlewares` / `GetAgentMiddleWares` 这些消费方拿到 `*RuntimeContext` 后**只读字段**,从不调 setter。这条规则靠 code review + §6 测试守(没有编译期保护;Go 没有 const ref)。
- **lead agent 不长期持有指针**:`MakeLeadAgent` 在构造时把 rt 字段需要的值**全部抽出来烧死**——`IsPlanMode` 烧到 system prompt 文本和 middleware 决策里、`AgentName` 烧到 `Trace.agentName` 和 `deep.Config.Name`、`AgentConfig` 烧到 tool group 解析、`ModelCfg` 烧到 `buildChatModel` 的入参。**lead agent 内部任何 callback 都不再访问 `*RuntimeContext`**。这是为什么 `SetPlanMode` 必须重建 lead agent——光改 rt 字段对已构造的 lead agent 没有任何作用,反过来,正因为 lead agent 不持有 rt 引用,setter 改字段对正在跑的 lead agent 也是零干扰。
- **fork 子 agent 必须显式 `Clone`**:由于 RuntimeContext 是共享指针,`subRT := rt` 只是 copy 指针——两个 fork 共享同一份字段,任意一边 setter 都污染对方。subagent fork(§4.2.5)用 `rt.Clone()` 拿一份独立副本,clone 内部对 `HITLTools` slice 做拷贝避免 alias。

`Clone` 方法定义(放在 `runtime_config.go` setter 旁边):

```go
// Clone returns an independent copy of rt suitable for forking subagents.
// HITLTools slice is deep-copied because subsequent SetHITLTools on either
// side would otherwise alias through the shared backing array (§4.2.5
// explains the alias risk in detail). AgentConfig / ModelCfg pointers are
// shared on purpose — they're effectively immutable lookup results owned
// by *config.Config; SetAgentName replaces the pointer, never mutates the
// pointee.
func (rt *RuntimeContext) Clone() *RuntimeContext {
    clone := *rt
    if rt.HITLTools != nil {
        clone.HITLTools = append([]string(nil), rt.HITLTools...)
    }
    return &clone
}
```

##### (e) 不变量(写进文档,不改代码强制)

- **唯一写者**:只有 `DeepAgentRuntime.SetXxx` 系列(目前只 `SetPlanMode`,以后可能更多)在 `r.mu` 内调 `*RuntimeContext` 上的 setter。其它代码一律视 `*RuntimeContext` 为只读。
- **`AgentName` / `AgentConfig` / `ModelCfg` 三字段的一致性靠 `SetAgentName` 唯一入口维护**;别在外部直接给这三个字段赋值。
- **`SetAgentName` 是唯一会失败的 setter**;其它单字段 setter 都没有错误返回——单字段写没什么可错的。
- **lead agent 构造完即与 rt 解耦**:这是允许 setter 在 lead agent 还活着的时候改 rt 而不引起 race 的根本原因。这条若被破坏(比如以后某个 middleware 决定持有 `*RuntimeContext` 在 callback 里读),整套并发模型崩塌——必须同步加锁或退回 by-value snapshot。

#### 4.2.2 `DeepAgentRuntime` 持有 `*RuntimeContext` 并暴露 `SetPlanMode`

```go
type DeepAgentRuntime struct {
    cfg                 *config.Config        // 新增:SetPlanMode 重建 lead agent 时要它
    rt                  *agent.RuntimeContext // 新增:DeepAgentRuntime 是 rt 的唯一所有者,字段写仅通过 SetPlanMode (持 mu)
    modelName           string
    runner              *adk.Runner
    mu                  sync.Mutex
    pendingCheckpointID string
    history             []*schema.Message
    maxHistoryTurns     int
    trace               *middlewares.Trace
}

// 现状:NewDeepAgentRuntime(ctx, cfg) 一气构造完。改完后保持同签名,
// NewRuntimeContext 已经返回 *RuntimeContext(§4.2.1.b),直接持有即可。
func NewDeepAgentRuntime(ctx context.Context, cfg *config.Config) (Runtime, error) {
    rt, err := agent.NewRuntimeContext(cfg) // §4.2.1: 不再接 seed,返回 *RuntimeContext
    if err != nil { return nil, err }

    leadAgent, trace, err := agent.MakeLeadAgent(ctx, rt, cfg)
    if err != nil { return nil, fmt.Errorf("build lead agent: %w", err) }

    store := checkpoint.NewStore(filepath.Join(cfg.RootDir, ".eino-cli", "checkpoints"))
    runner := adk.NewRunner(ctx, adk.RunnerConfig{
        Agent:           leadAgent,
        EnableStreaming: true,
        CheckPointStore: store,
    })

    return &DeepAgentRuntime{
        cfg:             cfg,
        rt:              rt, // 单一所有者,后续 SetPlanMode 在 mu 内改它
        modelName:       cfg.DefaultModel,
        runner:          runner,
        maxHistoryTurns: 20,
        trace:           trace,
    }, nil
}

// SetPlanMode 只在锁内做三件事:改字段、重建 lead agent、换 runner/trace。
// history 不清:plan mode 切换不该把对话上下文也烧掉,语义上应当跟
// /debug toggle 一致(只换 agent 行为,不洗 history)。
func (r *DeepAgentRuntime) SetPlanMode(ctx context.Context, plan bool) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    if r.rt.IsPlanMode == plan {
        return nil // no-op,不浪费一次 lead agent 重建
    }
    r.rt.SetPlanMode(plan)

    leadAgent, trace, err := agent.MakeLeadAgent(ctx, r.rt, r.cfg)
    if err != nil {
        // 失败时回滚字段,免得 rt 与 lead agent 对不上
        r.rt.SetPlanMode(!plan)
        return fmt.Errorf("rebuild lead agent for plan mode %v: %w", plan, err)
    }
    store := checkpoint.NewStore(filepath.Join(r.cfg.RootDir, ".eino-cli", "checkpoints"))
    r.runner = adk.NewRunner(ctx, adk.RunnerConfig{
        Agent:           leadAgent,
        EnableStreaming: true,
        CheckPointStore: store,
    })
    r.trace = trace
    return nil
}
```

> 跟原方案(A 走 `Options{PlanMode}` + seed 重跑 NewRuntimeContext)的实质差别:
> - **不重新解析 agent / model**:RuntimeContext 已经持有 `AgentConfig` / `ModelCfg`,不必再过一次 `GetAgentConfig` / `GetModelConfig`。
> - **`NewDeepAgentRuntime` 签名不变**:CLI 启动入口零改动,默认 plan mode 关。
> - **history 不洗**:之前 §4.2.3 写过"plan mode 切换 → ClearHistory",现在去掉,理由见上面注释。如果实测发现 model 拿带旧 plan mode preamble 的 history 跑新 plan mode 时表现错乱,再加。

并发约束:

| 场景 | 安全性 |
|---|---|
| 用户输入中,无 stream 在跑 | TUI 调 `SetPlanMode` 加 `r.mu`,完成后释放;下一次 `ExecuteStream` 进来时拿到的是新 runner |
| Stream 跑到一半用户输 `/plan on` | TUI 调 `SetPlanMode` 等 `r.mu`(`ExecuteStream` 已经在 `r.mu.Unlock()` 之后跑 stream,不持锁),立刻拿到锁、改字段、换 runner。**当前进行中的 turn 用旧 runner 跑完,下一次 `ExecuteStream` 拿新 runner**。语义跟 deer-flow per-request RunnableConfig 等价(per-turn 切换) |
| 两个 goroutine 同时 `SetPlanMode` | 串行化(mu),最后赢家定 |

`r.runner` 字段的读写都在 `r.mu` 内(`ExecuteStream` 当前的 read 也得包进锁,见下条);MakeLeadAgent 的 rebuild 失败时回滚字段保持 invariant。

> 顺手要改的小坑:`ExecuteStream` 当前在 `r.mu.Unlock()` 之后才读 `r.runner`,SetPlanMode 加进来后这是 race。改法:`ExecuteStream` 进来后在锁内 `runner := r.runner`(value copy 一份),再释放锁去跑 stream。这一步本来在原方案 A 里也要做,只是写文档时漏了。

#### 4.2.3 `/plan` slash 命令

deer-flow 是把 plan mode 塞进每次 RunnableConfig,前端控制。CLI 这边没那么多客户端,**最贴近 user 直觉的就是一个 toggle slash 命令**,跟 `/debug` 同形态:

```go
// backend/cli/tui/update.go, handleBuiltin 里多一 case
case "plan":
    return m.handlePlanCmd(text), true

// handlePlanCmd: 解析 on/off/toggle,调 m.rt.SetPlanMode(ctx, ...)。
// 成功后 push 一行系统消息: "plan = on / off"。
// 出错就 push "plan toggle failed: <err>",老 plan mode 已被 SetPlanMode
// 内部回滚,UI 状态不动。
```

实现侧改动:
- `backend/cli/tui/model.go` 在 `builtinHelp` 里加 `/plan [on|off|toggle]` 一行;`Model` 字段加 `planMode bool`(纯展示状态,真值在 runtime)。
- `backend/runtime/eino/deep_runtime.go` 暴露的 `SetPlanMode(ctx, plan bool) error` 见 §4.2.2。

#### 4.2.4 yaml 字段(本期不做)

`yaml/config.yaml` 里有个被注释掉的 `is_plan_mode: false`,deer-flow 那边走 RunnableConfig 而非 yaml,**留它注释着即可**;真正激活的话会跟 `/plan` 命令的优先级、跟 `agent.yaml` 的 plan mode 字段(如果以后加)产生多源决策矛盾。这一期只走 `/plan`。

#### 4.2.5 Subagent fork callsite 改动

`backend/agent/subagents.go:30-32` 现在的写法依赖 seed:

```30:32:backend/agent/subagents.go
subSeed := rt
subSeed.AgentName = name
subRT, err := NewRuntimeContext(cfg, &subSeed)
```

意图是"copy 父 rt → 改 AgentName → 让 NewRuntimeContext 重新解析 AgentConfig / ModelCfg"。删 seed + 全程指针之后改成两步:**`Clone()` 拿独立副本,再调 `SetAgentName` 触发重新解析**:

```go
subRT := rt.Clone()                                       // 独立副本(HITLTools 已 deep copy,见 §4.2.1.d)
if err := subRT.SetAgentName(cfg, name); err != nil {     // 重新解析 AgentConfig / ModelCfg
    slog.Warn("failed to finalize subagent runtime; skipping",
        "agent", name,
        "err", err,
    )
    continue
}

sub, _, err := MakeLeadAgent(ctx, subRT, cfg)
```

要点:

- **必须 `Clone()` 不能直接 `subRT := rt`**:全程指针后,`subRT := rt` 只是 copy 指针,subagent 的 setter 会污染父 rt——典型的 alias bug。Clone 给出独立副本,父子互不影响。
- **`Clone` 已处理 HITLTools alias**:slice 在 Clone 内部已经 `append([]string(nil), ...)` 拷贝,subagent 后续如果调 `SetHITLTools` 不会回改父。`AgentConfig` / `ModelCfg` 是共享指针目标,但它们是 `*config.Config` 拥有的不可变 lookup 结果,没人会去 mutate 指针目标本身,共享是安全的。
- **`SetAgentName` 失败时 `subRT` 三字段都不动**(§4.2.1.c 的原子性),立刻 `continue` 跳过这个 subagent,subRT 走出作用域被 GC。
- **取消旧写法的隐藏行为**:原来 seed 里 `HITLTools` 字段没被 `NewRuntimeContext` 读、`IsPlanMode` 走 `if seed.IsPlanMode` 的 truthy override(false 不能覆盖 true),都是隐藏规则。新写法 Clone 直接复制所有字段,显式可控。

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

#### 4.5.0 前置:解决 `DebugConsumer` 跟 `m.debug` 的语义错位

照搬当前管道(`Trace` + `DebugConsumer`)有个隐含 bug:**当前 consumer 只在 `/debug on` 时才挂**,debug 没开 → ctx 上没 consumer → `Trace` 里所有 `consumer.Send` 调用直接 short-circuit,新加的 todo 事件根本不会到 TUI。

```124:127:backend/cli/tui/update.go
var consumer middlewares.DebugConsumer
if m.debug && m.prog != nil {
    consumer = teaProgramConsumer{p: m.prog}
}
```

```32:38:backend/agent/middlewares/debug.go
func WithDebugConsumer(ctx context.Context, consumer DebugConsumer) context.Context {
    if consumer == nil {
        return ctx
    }
    return context.WithValue(ctx, debugConsumerKey{}, consumer)
}
```

两件事其实是正交的:`Trace` 是个**事件采集器**,always-on(每 turn Before/After 都跑);`DebugConsumer` 这个名字 + 当前的挂载条件**把"采集事件"和"渲染 debug UI"耦合到了同一个布尔 `m.debug` 上**。多一类 todo 事件之后,这个耦合就不成立了——todos 应该在 plan-mode 下永远显示,不受 `/debug` 守门。

| 维度 | 现状 | 多 todo 之后的需求 |
|---|---|---|
| Trace 跑不跑 | 永远跑(BeforeModel/AfterModel 钩子注册了) | 不变 |
| Consumer 挂不挂 | 只在 `m.debug` 时挂 | **永远挂**(否则 todos 收不到) |
| 渲染 Before/After trace | `m.debug` 决定 | 不变 |
| 渲染 Todo 面板 | — | **不受 `m.debug` 守门** |

修法分两步,各自独立的 commit:

- **Step 1(纯重命名,行为零变更)**:`DebugConsumer` / `DebugEvent` / `WithDebugConsumer` / `DebugBefore` / `DebugAfter` / `getDebugConsumerFromContext` / `debugConsumerKey` 全改成 `Trace*` 同形态;`backend/agent/middlewares/debug.go`(已经放的就是 `Trace` middleware)文件名也改 `trace.go`,`debug_test.go` → `trace_test.go`。`m.debug bool`、`/debug` 命令、`formatDebugInput` / `formatDebugOutput`、`pushMessage("debug-input", ...)` 这些**保留不动**——它们确实是 debug-only,不是 trace 事件管道的一部分。
- **Step 2(行为变更)**:加 `TracePhaseTodos`、改 `startStream` 永远挂 consumer、`handleTraceEvent` 里 Before/After 受 `m.debug` 守门、Todos 永远渲染。这一步即下面 4.5.1 / 4.5.2。

> 这两步必须分两个 commit,不能合一起——AGENTS.md "纯重命名 ≠ 行为变更"是死规矩;合并后 review 没法用一句话讲清 diff。

#### 4.5.1 加 `TracePhaseTodos` 事件 + 让 consumer 永远挂

(基于 Step 1 已经把名字改干净的前提)

事件结构扩字段:

```go
// backend/agent/middlewares/trace.go 新增 phase 常量
const (
    TracePhaseBefore = iota + 1
    TracePhaseAfter
    TracePhaseTodos
)

type TraceEvent struct {
    AgentName string
    Phase     int
    Turn      int
    Messages  []*schema.Message
    Todos     []deep.TODO  // only set when Phase == TracePhaseTodos
}
```

> 注意:`Todos` 字段只在 `TracePhaseTodos` 时填,其它两个 phase 留空——这跟 `Messages` 字段在不同 phase 语义不同(Before 是 full slice、After 是 single delta)是一样的,struct 同一份按 phase 解释,符合 AGENTS.md "struct 装数据,不为单一字段炸新结构"。

`Trace.AfterModelRewriteState` 末尾追加(在已有 `TracePhaseAfter` Send 之后):

```go
if raw, ok := adk.GetSessionValue(ctx, deep.SessionKeyTodos); ok {
    todos, _ := raw.([]deep.TODO)
    if len(todos) > 0 {
        consumer.Send(TraceEvent{
            AgentName: t.agentName,
            Phase:     TracePhaseTodos,
            Turn:      int(t.turn.Load()),
            Todos:     todos,
        })
    }
}
```

`startStream` 调用点不再受 `m.debug` 守门:

```go
// backend/cli/tui/update.go (旧)
var consumer middlewares.DebugConsumer
if m.debug && m.prog != nil {
    consumer = teaProgramConsumer{p: m.prog}
}

// backend/cli/tui/update.go (新)
var consumer middlewares.TraceConsumer
if m.prog != nil {
    consumer = teaProgramConsumer{p: m.prog}
}
```

代价:每个 turn 多发 1–2 次 trace 事件给 TUI(在 `m.debug == false` 时被 handler 丢掉)。`m.prog == nil` 这条 nil 守留着,`prog` 只在 main 启完 bubbletea 之后才被填,启动早期消息要走老路;这条 invariant 不变。

#### 4.5.2 渲染

`backend/cli/tui/update.go` 的 `(*Model).handleTraceEvent` 按 phase 过滤,Before/After 受 `m.debug` 守、Todos 不受:

```go
func (m *Model) handleTraceEvent(ev middlewares.TraceEvent) (tea.Model, tea.Cmd) {
    switch ev.Phase {
    case middlewares.TracePhaseBefore:
        if m.debug {
            m.pushMessage("debug-input", formatDebugInput(ev))
        }
    case middlewares.TracePhaseAfter:
        if m.debug {
            m.pushMessage("debug-output", formatDebugOutput(ev))
        }
    case middlewares.TracePhaseTodos:
        m.todos = ev.Todos // 永远更新缓存,与 m.debug 无关
    }
    return m, nil
}
```

`Model` 字段加一行 `todos []deep.TODO`(零值 = 不画面板)。

`backend/cli/tui/view.go` 在主消息列表上方画一个紧凑面板。**两态视图,默认折叠**——避免长 todo list 把 chat 区挤死:

##### 展开态(候选 A) — 跟 scrollback 平铺,无边框

```text
  Todos · 2/5  ▰▰▰▱▱

  ✓  R̶e̶a̶d̶ ̶d̶e̶e̶r̶-̶f̶l̶o̶w̶ ̶s̶o̶u̶r̶c̶e̶
  ✓  C̶o̶m̶p̶a̶r̶e̶ ̶w̶i̶t̶h̶ ̶e̶i̶n̶o̶ ̶p̶r̶e̶b̶u̶i̶l̶t̶
  ◐  Write reminder middleware       in_progress
  ○  Wire /plan slash command
  ○  Update tests
```

##### 折叠态(候选 D) — 单行,只显示当前 in_progress

```text
▶ Todos 2/5 · in_progress: Write reminder middleware
```

折叠态规则:
- 显示进度 `<done>/<total>` + 当前 in_progress 项的 `Content`(若有);
- 如果没 in_progress 项(全 pending 或全 completed),退化成 `▶ Todos 2/5 · 3 pending` / `▶ Todos 5/5 · all done`;
- 单行强制截断,超过宽度用 `…` 省略。

##### 配色 + 状态符号(展开/折叠共用)

| 元素 | 符号 | lipgloss style(加到 `styles.go`) |
|---|---|---|
| `completed` | `✓` | `todoCompletedStyle = dimStyle.Strikethrough(true).Foreground(lipgloss.Color("10"))` (绿 + 删除线 + faint) |
| `in_progress` | `◐` | `todoInProgressStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))` (橙加粗) |
| `pending` | `○` | `todoPendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))` (浅灰) |
| 标题 `Todos` | — | `headerTitleStyle`(已有,magenta bold) |
| 进度条已填 | `▰` | `todoBarFilledStyle = accentStyle`(已有 blue) |
| 进度条未填 | `▱` | `todoBarEmptyStyle = dimStyle`(已有) |
| 折叠 prefix | `▶` | `headerTitleStyle`(magenta) |
| 状态标签 `in_progress` | — | `dimStyle` |

> **删除线终端兼容性**: lipgloss `Strikethrough(true)` 走 ANSI SGR 9。iTerm2 / Apple Terminal / Alacritty / kitty / WezTerm / VS Code 内置 / Windows Terminal 都支持。极少数老 tmux 转发链路会丢 SGR 9,降级表现是"completed 项只剩 dim + 绿色,没划线"——可读性不受损,不需要专门 fallback。

##### 折叠/展开切换

- **触发**:`/todos` slash 命令(`/todos` 不带参 = toggle,`/todos open` / `/todos close` 显式)。沿用现有 `/debug` 一致的 verb 模型,不引入新键绑定 keymap(键绑定逻辑改 model.go 太重,而且会跟 textinput 的 keymap 打架)。
- **状态字段**:`Model.todoExpanded bool`,默认 `false`(折叠)。
- **空 todos 时不渲染**: `len(m.todos) == 0` → 两个态都不画(连 prefix 都不留),让 chat 区没有视觉残留。

##### Model 字段汇总

```go
type Model struct {
    // ...existing fields...
    todos         []deep.TODO  // 由 TracePhaseTodos 写入
    todoExpanded  bool         // /todos toggle 切;默认 false (折叠态)
}
```

`/clear` 时除了清 history,还要 `m.todos = nil` + `m.todoExpanded = false`(否则切 thread 后旧面板还挂着——todos 跟 thread state 一起活,视图缓存得跟着死)。

### 4.6 前端 / API(N/A)

仓里目前只有 TUI,没有 web 前端;deer-flow §2.6 那段 React 组件不在我们的范围。如果以后接 web,直接从 trace stream 同一管道把 `TracePhaseTodos` 事件转 SSE 即可,本期不留接口。

---

## 5. 落地任务清单(分 commit)

> 参考 AGENTS.md "Commit 粒度":纯重命名 / 摘中间层 / 加新功能 各拆开。

### Commit 1 — `runtime: drop seed, switch RuntimeContext to *RuntimeContext + setters`

> 这次 commit 同时做三件结构性改动(删 seed + 加一组 setter + RuntimeContext 全程指针化),它们必须同步——任何一边单独改都会让另一边的 callsite 编译不过。**没有跨文件的纯重命名**,签名变更跟测试改动绑死,合一个 commit 不违 AGENTS.md "纯重命名 ≠ 行为变更"。

#### 类型与签名变更总览

| 位置 | 旧 | 新 |
|---|---|---|
| `NewRuntimeContext` | `(cfg, seed) (RuntimeContext, error)` | `(cfg) (*RuntimeContext, error)` |
| `MakeLeadAgent` | `(ctx, rt RuntimeContext, cfg)` | `(ctx, rt *RuntimeContext, cfg)` |
| `GetSystemPrompt` | `(rt RuntimeContext, cfg)` | `(rt *RuntimeContext, cfg)` |
| `GetChatModelMiddlewares` | `(ctx, cfg, rt RuntimeContext, chatModel)` | `(ctx, cfg, rt *RuntimeContext, chatModel)` |
| `GetAgentMiddleWares` | `(rt RuntimeContext)` | `(rt *RuntimeContext)` |
| `buildNamedSubagents` | `(ctx, rt RuntimeContext, cfg, names)` | `(ctx, rt *RuntimeContext, cfg, names)` |
| `(*RuntimeContext)` 方法新增 | — | `Clone()` / `SetAgentName(cfg, name) error` / `SetPlanMode` / `SetSubagentEnabled` / `SetMaxConcurrentSubagents` / `SetHITLTools` |

`NewDeepAgentRuntime` 签名零改动(`(ctx, cfg)`),CLI 启动入口零改动。

#### 文件清单

**生产代码**

- `backend/agent/runtime_config.go`
  - 删 `NewRuntimeContext` 的 `seed *RuntimeContext` 参数;返回类型 `RuntimeContext` → `*RuntimeContext`(成功路径返回 `&RuntimeContext{...}`,失败返回 `nil, err`)。
  - 函数体简化为只解析 `cfg.DefaultAgent` 的基线 + `MaxConcurrentSubagents = 3`(其它字段类型零值)。
  - 新增 6 个方法:`Clone() *RuntimeContext` / `SetAgentName(cfg, name) error` / `SetPlanMode` / `SetSubagentEnabled` / `SetMaxConcurrentSubagents` / `SetHITLTools`(代码骨架见 §4.2.1.c + §4.2.1.d 的 `Clone`)。
- `backend/agent/lead_agent.go`
  - `MakeLeadAgent(rt RuntimeContext, ...)` → `(rt *RuntimeContext, ...)`;函数体内 `rt.X` 字段访问写法不变(指针自动解引)。
- `backend/agent/prompt.go`
  - `GetSystemPrompt(rt RuntimeContext, cfg)` → `(rt *RuntimeContext, cfg)`。
- `backend/agent/middleware_chain.go`
  - `GetChatModelMiddlewares` / `GetAgentMiddleWares` 签名 `RuntimeContext` → `*RuntimeContext`。
- `backend/agent/subagents.go`
  - `buildNamedSubagents` 签名 `RuntimeContext` → `*RuntimeContext`。
  - body 改成 `subRT := rt.Clone(); subRT.SetAgentName(cfg, name)` 两步(详见 §4.2.5)。
- `backend/runtime/eino/deep_runtime.go`
  - `NewRuntimeContext(cfg, nil)` → `NewRuntimeContext(cfg)`,直接拿 `*RuntimeContext`。
  - `DeepAgentRuntime` 字段加 `cfg *config.Config` 和 `rt *agent.RuntimeContext`,**持有可变 RuntimeContext 单一所有者**。
  - `ExecuteStream` 在 `r.mu` 内 snapshot `r.runner` 一份指针,修原本 read-without-lock 的 race。
  - 新增 `SetPlanMode(ctx, plan bool) error`:no-op 短路 / 改字段 / 重建 lead agent + runner / trace / 失败回滚字段(详见 §4.2.2)。

**测试代码**

- 新增 `backend/agent/runtime_config_test.go`
  - `(*RuntimeContext).SetPlanMode` / `SetSubagentEnabled` / `SetMaxConcurrentSubagents`(含 `n <= 0` 走 default 3) / `SetHITLTools` 的字段写入断言。
  - `(*RuntimeContext).SetAgentName` 三件事:成功路径(三字段同步刷新)、agent 不存在路径(三字段全不动)、model 不存在路径(三字段全不动)。
  - `Clone` 三件事:基本字段相等、`HITLTools` 修改不互相污染、`AgentConfig` / `ModelCfg` 指针共享(故意,因为不可变)。
- `backend/runtime/eino/runtime_test.go`
  - `DeepAgentRuntime.SetPlanMode` 三态:no-op / 真切换(`r.runner` 指针变了)/ rebuild 失败回滚 `r.rt.IsPlanMode`。
  - `-race` 用例:一个 goroutine 反复 `SetPlanMode`,另一个反复 `ExecuteStream` 一个会立刻拒掉的 prompt(空串),期望 race detector 干净。
- 旧测试 callsite 一律加 `&`(机械修改,无逻辑变化):
  - `backend/agent/middleware_chain_test.go`(`return RuntimeContext{...}` → `return &RuntimeContext{...}`)
  - `backend/agent/middleware_chain_phase3_test.go`(2 处)
  - `backend/agent/memory_e2e_test.go`(2 处)
  - `backend/agent/prompt_test.go`(2 处)
  - `backend/agent/subagents_test.go`(2 处:`RuntimeContext{}` → `&RuntimeContext{}`)

#### 验证

- `go build ./...` 全绿——指针化是机械传染,任何 callsite 漏改都会编译不过,这点 Go 编译器代我们守住。
- `go test -race ./backend/agent/... ./backend/runtime/...` 全绿。
- 手动跑无 `/plan` 时行为跟之前一致。

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

### Commit 4 — `middlewares: rename DebugConsumer/DebugEvent → TraceConsumer/TraceEvent`

> 纯重命名 + 文件 rename。**零行为变更**,跑测试前后输出比特一致。AGENTS.md "纯重命名 ≠ 行为变更" 要求独立 commit;这是给 Commit 5 让路——todo 事件不是 debug 事件,DebugConsumer 这名字撑不下去。

文件 rename:
- `backend/agent/middlewares/debug.go` → `backend/agent/middlewares/trace.go`
- `backend/agent/middlewares/debug_test.go` → `backend/agent/middlewares/trace_test.go`

符号 rename(全仓 grep + replace,确保 callsite 一次扫到底):
- `DebugConsumer` → `TraceConsumer`
- `DebugEvent` → `TraceEvent`
- `WithDebugConsumer` → `WithTraceConsumer`
- `getDebugConsumerFromContext` → `getTraceConsumerFromContext`
- `debugConsumerKey{}` → `traceConsumerKey{}`
- `DebugBefore` → `TracePhaseBefore`
- `DebugAfter` → `TracePhaseAfter`
- `(*Model).handleDebug` → `(*Model).handleTraceEvent`(dispatcher 不再是 debug-only;`update.go:23` 的 type switch case 类型同步改 `middlewares.TraceEvent`)
- `teaProgramConsumer.Send(ev DebugEvent)` 入参类型同步

涉及文件(都是机械替换):
- `backend/agent/middlewares/trace.go`(原 debug.go)
- `backend/agent/middlewares/trace_test.go`(原 debug_test.go)
- `backend/cli/tui/stream.go`
- `backend/cli/tui/update.go`
- `backend/cli/tui/model.go`(`formatDebugInput(ev middlewares.DebugEvent)` 入参类型)
- `backend/cli/tui/debug_format_test.go`(测试体里 `middlewares.DebugEvent` / `middlewares.DebugBefore` / `middlewares.DebugAfter` 引用更新;**文件名保留**——它就是测 debug 渲染格式的)

**保留不动**(确实是 debug-only,不属于 trace 事件管道):
- `m.debug bool` 用户开关、`/debug` slash 命令、`handleDebugCmd`
- `formatDebugInput` / `formatDebugOutput`
- `pushMessage("debug-input", ...)` / `"debug-output"` tag 字符串
- `debug_format_test.go` 文件名、`TestBuiltinHelpMentionsDebug` 测试名
- `styles.go:16` "Debug trace styling" 注释

验证:`go build ./... && go test ./...` 全绿,且 diff `go test -v ./...` 输出与 Commit 3 末态完全一致(没有任何新测试,纯改名)。

### Commit 5 — `tui: always attach trace consumer & render todo panel`

> 把原方案的"Commit 4 — todo 面板"和"§4.5.0 挂载时机改动"合在一起。Step 2(行为变更)。

文件:
- `backend/agent/middlewares/trace.go`(新增 `TracePhaseTodos` 常量 + `TraceEvent.Todos` 字段;`Trace.AfterModelRewriteState` 末尾 emit todos 事件)
- `backend/agent/middlewares/trace_test.go`(覆盖 SessionKeyTodos 存在 / 不存在 / 空 slice 三态)
- `backend/cli/tui/stream.go`(无改动——签名已经是 `TraceConsumer`)
- `backend/cli/tui/update.go`:
  - `startStream` 调用点去掉 `m.debug` 守门(只留 `m.prog != nil` nil 守)
  - `handleTraceEvent` 三 case:Before / After 受 `m.debug` 守门,Todos 永远缓存
  - `handleBuiltin` 的 `/clear` 分支增加 `m.todos = nil` + `m.todoExpanded = false`
  - `handleBuiltin` 加 `case "todos"` → `handleTodosCmd(text)`(toggle / `open` / `close` 三态,跟 `/debug` 同模型)
  - `builtinHelp` 文案补 `/todos` 行
- `backend/cli/tui/view.go`:
  - 在 banner 与 message scrollback 之间加 `renderTodoPanel(m)` 段
  - 折叠态(`!m.todoExpanded`)→ 单行 `▶ Todos x/y · ...`(§4.5.2 候选 D)
  - 展开态(`m.todoExpanded`)→ 多行块(§4.5.2 候选 A)
  - `len(m.todos) == 0` 直接返回 `""`,不占行
- `backend/cli/tui/styles.go` 加 5 个新 style:`todoCompletedStyle` / `todoInProgressStyle` / `todoPendingStyle` / `todoBarFilledStyle` / `todoBarEmptyStyle`(配色见 §4.5.2 表)
- `backend/cli/tui/model.go`:`Model` 新增字段 `todos []deep.TODO` 和 `todoExpanded bool`
- 新增 `backend/cli/tui/todo_render_test.go`,覆盖:
  - `m.debug == false` 时 `TracePhaseTodos` 事件仍能更新 `m.todos`
  - `/clear` 后 `m.todos == nil` 且 `m.todoExpanded == false`
  - `/todos` toggle:`false → true → false`,`/todos open` 强开,`/todos close` 强关
  - `renderTodoPanel`:折叠态包含当前 in_progress Content;展开态包含 `▰` 进度填充和 `▱` 空白格;completed 项渲染输出含 SGR 9 转义(`\x1b[9m`),证明 strikethrough 真的应用了
  - `len(m.todos) == 0` 时 `renderTodoPanel` 返回空串
  - 单行折叠态遇超长 in_progress Content 截断为 `…`

验证:
- 开 TUI **不开 `/debug`**,`/plan on`,跑多步任务 → 折叠态 todo 面板出现在顶部并随 model 调用 `write_todos` 实时更新(关键回归点,这就是 §4.5.0 修的洞)。
- `/todos` 切到展开态,看到 ✓ 项划掉(strikethrough)。
- `/debug on` 后 Before/After trace 仍正常显示,跟 todo 面板互不干扰。
- `/clear` 后面板消失。

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

`(*RuntimeContext).SetPlanMode(true)` → 字段确实变 true;再 `SetPlanMode(false)` → 回到 false。仅断言字段行为,不涉锁。

### 6.3 `deep_runtime_test.go`(新增)

| 用例 | 期望 |
|---|---|
| `SetPlanMode(true)` 后 `r.rt.IsPlanMode == true`,且 `r.runner` / `r.trace` 指针变了(说明 lead agent 真的 rebuild 过) | OK |
| `SetPlanMode(true)` 两次连续调用,第二次走 no-op 分支(`r.runner` 指针不变) | OK |
| 让 `MakeLeadAgent` mock 出错 → `SetPlanMode` 返回错误,且 `r.rt.IsPlanMode` 回滚 | OK |
| `-race`: 一个 goroutine 反复 `SetPlanMode(true/false)`,另一个 goroutine 反复 `ExecuteStream` 一个会立刻拒绝的 prompt(不真正进 LLM) → race detector 干净 | OK |

### 6.4 TUI 集成测试

跟 `debug_format_test.go` 同形态:
- 断言 `/help` 包含 `/plan` 和 `/todos` 两行
- `handlePlanCmd("/plan on")` 后 `m.planMode == true`,`handlePlanCmd("/plan off")` 反之
- `handleTodosCmd("/todos")` toggle `m.todoExpanded`(false→true→false)
- `handleTodosCmd("/todos open")` 强开;`handleTodosCmd("/todos close")` 强关
- `handleTodosCmd` 在 `m.todos == nil` 时不报错(允许在没 todos 时切,后续来事件再生效)

### 6.5 `todo_render_test.go`(新增)

| 用例 | 期望 |
|---|---|
| `len(m.todos) == 0` 调 `renderTodoPanel(m)` | 返回 `""` |
| `todoExpanded == false`,有 1 in_progress | 输出单行,含 `▶ Todos`、`x/y`、in_progress Content |
| `todoExpanded == false`,全 pending | 输出 `... · N pending` |
| `todoExpanded == false`,全 completed | 输出 `... · all done` |
| `todoExpanded == false`,Content 超长 | 末尾 `…` 截断,总宽 ≤ 终端宽 |
| `todoExpanded == true`,有 completed 项 | 输出含 SGR 9 转义 `\x1b[9m`(strikethrough 真生效)|
| `todoExpanded == true`,3 completed / 5 total | 进度条含 3 个 `▰` 和 2 个 `▱` |
| `m.debug == false` 时投递 `TracePhaseTodos` 事件 | `handleTraceEvent` 后 `m.todos` 已更新(关键回归 — §4.5.0 修的洞)|
| `/clear` 后 | `m.todos == nil` 且 `m.todoExpanded == false` |

### 6.6 手动验证清单

启 TUI(默认 OFF):
- [ ] `/plan on` 后系统消息 `plan = on`,history **保留**(plan mode 切换不洗 history,见 §4.2.2)。
- [ ] 输入"refactor 三个文件" → model 应该调 `write_todos` 并把 status 标 in_progress。
- [ ] 顶部出现折叠态 todo 单行 `▶ Todos x/y · in_progress: ...`(默认折叠,§4.5.2 候选 D)。
- [ ] `/todos` 切到展开态,看到分项列表;completed 项有删除线(终端不支持时退化为 dim+绿,可读性不损)。
- [ ] 完成第一项后 model 调一次 `write_todos`,面板第一项变 ✓ 并被划掉(展开态)/ 折叠态 in_progress 跳到下一项。
- [ ] `/todos close` 强收回折叠态。
- [ ] **不开 `/debug` 时 todo 面板照样实时更新**(§4.5.0 修的洞,关键回归点)。
- [ ] 强行制造长 history(repeated `/help` + 长 prompt 凑量)直到 summarization 触发,继续追问 → reminder 应被注入(开 `/debug` 看 BeforeModel 头一条 SystemMessage)。
- [ ] `/plan off` → 系统消息 `plan = off`;新对话不再有 plan-mode preamble(可对比 system prompt diff)。
- [ ] `/plan` 流式跑到一半时输入 → 当前 turn 用旧 plan mode 跑完,下一次输入起用新 plan mode。
- [ ] `/clear` 后 todo 面板消失,折叠态也复位。

---

## 7. 跟 `AGENTS.md` 对齐说明

- **结构体只装数据 + 简单 setter**:`TraceEvent.Todos` 只多一个字段不新建子类型;`TodoReminder` 只持有 `BaseChatModelAgentMiddleware` 嵌入,无业务字段;`(*RuntimeContext).SetPlanMode` 只改字段、不持锁(锁的责任在 `DeepAgentRuntime.mu`,职责单一)。
- **少压调用栈**:Reminder middleware 一层、Trace 一层、TUI handler 一层——总深度 ≤ 4。
- **少传数据**:不为 reminder 单独做一个 config 子结构;不抽 `TodoConsumer` 单独的 ctx-key,复用 `TraceConsumer`(改名前是 `DebugConsumer`,见 §4.5.0 + Commit 4)。
- **注释只回答 why**:每个新文件最多一段顶层 doc + 一两行讲"为什么 SystemMessage 而非 HumanMessage""为什么 reminder 在 Trace 之前"。
- **Commit 粒度**:Runtime 接通 / slash 命令 / reminder middleware / TUI 渲染 各一个 commit,diff 一句话讲清。
- **变量命名以动词开头**:`hasWriteTodosCall` / `renderTodoReminder` / `getTodosFromSession`(如果抽出去的话)。

---

## 8. 参考实现位置速查

LLM 仓:
- `backend/agent/middlewares/todo.go`(已有 plan mode preamble)
- `backend/agent/middleware_chain.go`(挂载点)
- `backend/agent/runtime_config.go`(IsPlanMode 字段 + `SetPlanMode` 方法)
- `backend/runtime/eino/deep_runtime.go`(持有 `*RuntimeContext` + `SetPlanMode` 重建 lead agent)
- `backend/cli/tui/update.go` / `model.go` / `view.go`(slash 命令 + 面板)
- `backend/agent/middlewares/trace.go`(TraceEvent 扩展点;Commit 4 之前叫 `debug.go`)

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
