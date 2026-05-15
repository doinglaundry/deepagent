# Step 3 — Subagent task 工具落地

承接 `specs/20260514-feature-comparison/design.md` §3 推荐的"下一步第 3 步"。
本仓库已经把 `IsSubagentEnabled=true` 传给 `deep.New`，依赖 eino deep 内置的
**general-purpose** subagent；但 `buildNamedSubagents` 一直只是测试占位。本
文档定义如何把 **named subagent + 受控生命周期** 接到 lead agent 上，最终以
单一 `task` 工具对模型暴露。

设计原则严格跟 `AGENTS.md` 走：**结构体只装数据，行为住顶层函数，少传数据，
少压调用栈**。

---

## 1. 背景与现状

### 1.1 当前仓库

- `backend/agent/subagents.go` — `buildNamedSubagents(ctx, cfg, names)` 已存
  在，对每个 name 递归 `MakeLeadAgent(name, false, false, cfg)` 出一个深 agent。
  但 `lead_agent.go` 和 `deep_runtime.go` 都没把它的返回值传到 `deep.Config`。
- `backend/agent/lead_agent.go` — `MakeLeadAgent` 只读 `agentConfig` 的
  `Model / MaxIteration / Skills`；`ToolsConfig.Tools` 写死成
  `tools.BuildBuiltinTools(cfg.RootDir)`，无 allow / deny。
- `backend/agent/middlewares/subagent_limit.go` — 已经按 `task` 工具名拦截，
  默认 `MaxParallel=3`（被 `cfg.MaxConcurrentSubagents` 覆盖）。
- `backend/agent/prompt.go` — `effectiveMaxSubagents(cfg)` 是 system prompt
  和 middleware 共用的并发上限来源。
- `backend/runtime/eino/deep_runtime.go` — `buildLeadRunner` 调
  `MakeLeadAgent(ctx, "default", false, true, cfg)`：plan mode 关、subagent
  开。`adk.Runner` 走文件 checkpoint。

**简言之**：subagent 链路只剩 general-purpose 一档（eino 内置），没有项目级
named subagent、没有 timeout、没有 journal、没有 tool 过滤。

### 1.2 eino deep 内置 task 工具

`adk/prebuilt/deep/task_tool.go` 已实现：

- `deep.Config.SubAgents []adk.Agent` + `WithoutGeneralSubAgent bool` 两个字
  段共同决定 task 工具背后的 named 表。
- 工具 schema 是 `{subagent_type: string, description: string}`，描述里把每
  个 sub agent 的 `Name()/Description()` 拼成 markdown 列表给模型选。
- 内部把每个 sub agent 包成 `adk.AgentTool` 并以 name 索引；`InvokableRun`
  直接转 `description` 为 `request` 字段给 sub agent run。
- **没有** timeout / status / journal / cancel cooperation —— 这是我们要补的。

所以仓库不需要再实现一个 `task` 工具。**复用 eino 的 task 工具**，把
`buildNamedSubagents` 的输出灌进 `deep.Config.SubAgents` 即可。差异化能力
（timeout / journal / 状态摘要）通过给每个 subagent 挂一层 **`adk.AgentMiddleware`**
来加，不破坏 task 工具的 schema。

### 1.3 deer-flow 对照

| 能力 | deer-flow `SubagentExecutor` | 本仓库目标 |
|---|---|---|
| 工具签名 | `task(description, prompt, subagent_type, max_turns?)` | 复用 eino task：`(subagent_type, description)`；prompt 即 description |
| 隔离 history | 自己造 `HumanMessage` + `SystemMessage` 喂 `astream` | eino `AgentTool.InvokableRun` 已经自带 fresh session |
| Status 枚举 | `pending/running/completed/failed/cancelled/timed_out` | 同 6 档；落 journal，不暴露给模型 |
| Journal | 内存 `_background_tasks` dict | append-only JSONL：`.eino-cli/subagents/<run_id>.jsonl` |
| Timeout | `Future.result(timeout=…)` + cooperative `cancel_event` | `context.WithTimeout` + eino 自带的 ctx 传播 |
| Tools allow/deny | `_filter_tools(allowed, disallowed)` | yaml 配 `tools_allow / tools_deny`，在 `BuildBuiltinTools` 之后过一遍 |
| 并发上限 | 全局 `MAX_CONCURRENT_SUBAGENTS=3` | 复用 `middlewares.SubagentLimit` + `cfg.MaxConcurrentSubagents` |
| Skill 注入 | 子 agent 各自 `_load_skill_messages` | `MakeLeadAgent` 已经按 `agentConfig.Skills` 注入 system prompt，不动 |

deer-flow 把执行做成异步轮询 + 流式回调（task_started / task_running /
task_completed），是因为 LangGraph 的 `astream` 是 async 协程。我们没必要照
搬：`adk.AgentTool.InvokableRun` 是同步函数，timeout 用 ctx 截即可，进度推
送先按"final 摘要回写"做，后续再考虑流式。

---

## 2. 目标设计

### 2.1 工具签名（复用 eino，不改 schema）

模型看到的还是 eino 默认 task 工具：

```jsonc
{
  "name": "task",
  "parameters": {
    "subagent_type": "string",  // 在 description 里列出可用 name
    "description":   "string"   // 完整 task prompt
  }
}
```

不引入第三个字段（如 `timeout`）。timeout 是配置职责，不是模型决策点；让模型
选 timeout 只会绕路。需要 per-task 覆盖时通过 `subagent_type` 选不同 yaml profile。

返回值（写回 tool message）格式：

```
[task: <subagent_type> | <run_id> | <status>]
<subagent 最终 assistant 输出>
```

`status` 取自 §2.3 枚举。失败 / 超时 / 取消时，`<subagent 最终输出>` 替换为简短
错误说明（**不**回填 traceback —— 主 agent 不需要也看不懂）。

### 2.2 执行模型

```
lead agent
  └─ task tool (eino 内置)
       └─ adk.AgentTool wrapper (eino)
            └─ named subagent (deep agent, IsPlanMode=false, IsSubagentEnabled=false)
                 ├─ AgentMiddleware: subagentRunner  (timeout + journal + status)
                 ├─ ChatModel middlewares (复用 lead 的链)
                 └─ Tools = filterTools(BuildBuiltinTools, profile.ToolsAllow, profile.ToolsDeny)
```

- **History**：完全独立。task 工具调用一次 = 一个 fresh adk.Runner session；
  主 history 只看到最终 tool message。
- **Root**：subagent 共享 lead 的 `cfg.RootDir`（同一工作目录，不沙箱）。沙箱
  化是后续 spec 的事，本文档不解决。
- **Tools**：subagent 不出现 `task` 工具（eino 内置 task 工具仅在 `IsSubagentEnabled=true`
  的路径上挂上来；递归 `MakeLeadAgent(name, false, false, cfg)` 时 `WithoutGeneralSubAgent=true`
  且没有 SubAgents 传入，自动不暴露 task）。所以**嵌套调用 task 在 eino 层
  天然被禁掉**——这正是想要的。
- **回填**：单次 final 摘要写主 history 的 tool message。journal 才记中间状态。

### 2.3 Status 模型

```
pending    — 任务已登记，runner 尚未开始（基本只在登记到 invoke 之间出现）
running    — runner 已进入 inner agent，未到 terminal 状态
succeeded  — inner agent 正常返回（非 nil 输出）
failed     — inner agent 返回 error（非 ctx 错误）
timeout    — ctx.DeadlineExceeded
cancelled  — ctx.Canceled（用户 Ctrl-C / 上层取消）
```

`pending/running` 只在 journal 里出现；模型看到的 tool message 永远是
terminal 四态之一。和 deer-flow 一样的 6 档，但我们不维护"后台任务"
查询 API —— 一次工具调用 = 一次同步等。

### 2.4 Journal

每次 `task` 工具调用对应一个 `run_id`（短 uuid，8 字符就够）。落地：

```
<cfg.RootDir>/.eino-cli/subagents/<run_id>.jsonl
```

每行一条事件，schema：

```jsonc
{
  "ts":       "2026-05-14T21:51:03.123+08:00",
  "run_id":   "a1b2c3d4",
  "agent":    "researcher",
  "phase":    "start | tool_call | tool_result | final | error",
  "status":   "running | succeeded | failed | timeout | cancelled",
  "payload":  "..."   // phase-specific，截断到 N KB
}
```

最小可用先只写三条：`start` / `final | error` 终止行。`tool_call / tool_result`
两档留作 `/debug` 后续扩展（中间件 hook 已经有 `Trace`，将来 fan-out 到
journal 是单点修改）。

**append-only + 文件 per run_id**，不需要锁、不需要轮转：一次 task 最多几
百 KB，10 次 task = 10 个独立文件。`ls .eino-cli/subagents | head` 就是天然
的"最近 subagent 列表"。

### 2.5 Parallel limit

不动：`middlewares.SubagentLimit` 已经在 lead 链路里、用
`effectiveMaxSubagents(cfg)` 卡数。任何超出上限的 task 调用在 model 出
ToolCalls 之后立即被丢弃，subagent 根本不会启动。和本 spec 是正交关系。

---

## 3. 代码骨架

### 3.1 文件布局

```
backend/agent/subagents/
  task_tool.go   — buildNamedSubagents (替换 backend/agent/subagents.go)
                   + filterTools helper
  runner.go      — subagentRunner middleware (timeout + journal hooks)
  journal.go     — openJournal / writeJournalEntry / journalEntry struct
```

把现有 `backend/agent/subagents.go` 物理搬到 `backend/agent/subagents/` 包，并
把测试一起搬。`MakeLeadAgent` 多写一行 import。

### 3.2 `journal.go`

结构体只装数据，写入由顶层函数完成。

```go
package subagents

// journalEntry 是 JSONL 单行 schema；保持扁平方便 jq 处理。
type journalEntry struct {
    TS      string `json:"ts"`
    RunID   string `json:"run_id"`
    Agent   string `json:"agent"`
    Phase   string `json:"phase"`
    Status  string `json:"status"`
    Payload string `json:"payload,omitempty"`
}

// openJournal 返回追加模式的 *os.File；首次写入触发目录创建。
// 调用者负责 defer Close —— 一次 task 一次 open / close，省掉文件句柄缓存
// 这种 indirection。
func openJournal(rootDir, runID string) (*os.File, error) { ... }

// writeJournalEntry 把一行 JSON 写进 f；payload 超过 maxPayloadBytes 会截。
// 不上锁：一个 run_id 一个文件，一个 run 一个写者。
func writeJournalEntry(f *os.File, entry journalEntry) error { ... }

const maxPayloadBytes = 16 * 1024
```

不导出 entry 字段以外的方法 —— 没需求就不开 API surface。

### 3.3 `runner.go`

```go
package subagents

import (
    "context"
    "errors"
    "os"
    "time"

    "github.com/cloudwego/eino/adk"
)

// subagentRunner 是单个 named subagent 上挂的 AgentMiddleware。
// 字段全部是配置 / 句柄，行为住在 wrapRun / handleTerminal 这些顶层函数里，
// 不挂 method 链。
type subagentRunner struct {
    AgentName string
    Timeout   time.Duration
    RootDir   string
}

// newSubagentMiddleware 把 subagentRunner 适配成 adk.AgentMiddleware。
// 用闭包而不是给 runner 加 method：runner 真正只是数据袋子。
func newSubagentMiddleware(r subagentRunner) adk.AgentMiddleware {
    return adk.AgentMiddleware{
        WrapRun: func(next adk.AgentRunFunc) adk.AgentRunFunc {
            return func(ctx context.Context, input []*schema.Message,
                opts ...adk.RunOption) *adk.AsyncIterator[*adk.AgentEvent] {
                return runSubagentTask(ctx, r, next, input, opts...)
            }
        },
    }
}

// runSubagentTask 是 task 一次执行的全部生命周期。
// 顺序：分配 run_id → 开 journal → ctx 套 timeout → 调 next → 落终态。
// 整段函数从上到下读完即可懂。
func runSubagentTask(
    ctx context.Context,
    r subagentRunner,
    next adk.AgentRunFunc,
    input []*schema.Message,
    opts ...adk.RunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {

    runID := newRunID()
    j, jerr := openJournal(r.RootDir, runID)
    if jerr == nil {
        defer j.Close()
        _ = writeJournalEntry(j, journalEntry{
            TS: nowISO(), RunID: runID, Agent: r.AgentName,
            Phase: "start", Status: "running",
        })
    }

    runCtx, cancel := context.WithTimeout(ctx, r.Timeout)
    defer cancel()

    iter := next(runCtx, input, opts...)
    return wrapIterWithTerminal(iter, j, r.AgentName, runID)
}

// wrapIterWithTerminal 透传所有事件；iterator 关闭后追加一条 final/error
// journal 行。把"落终态"和"主流程"切开是因为 next() 返回的是异步迭代器,
// 必须等 iter 关闭才能确定 status —— 这块逻辑独立、好测、不需要中间件 host。
func wrapIterWithTerminal(
    iter *adk.AsyncIterator[*adk.AgentEvent],
    j *os.File, agentName, runID string,
) *adk.AsyncIterator[*adk.AgentEvent] { ... }

// classifyTerminal 把 (err, lastOutput) 映射成 status；timeout / cancel
// 用 errors.Is 区分。
func classifyTerminal(err error) string {
    switch {
    case err == nil:
        return "succeeded"
    case errors.Is(err, context.DeadlineExceeded):
        return "timeout"
    case errors.Is(err, context.Canceled):
        return "cancelled"
    default:
        return "failed"
    }
}

func newRunID() string { ... } // 8-char base32; 不依赖外部包
func nowISO() string    { ... }
```

`subagentRunner` 三个字段 —— 严格满足 `AGENTS.md` "字段必须共享生命周期"：
agent name、timeout、root dir 都属于这一次 wrap 决策。不要把 `*config.Config`
塞进来 —— 那是构造时的事。

### 3.4 `task_tool.go`

替换现 `backend/agent/subagents.go`。

```go
package subagents

import (
    "context"
    "log/slog"
    "strings"
    "time"

    "github.com/cloudwego/eino/adk"
    "github.com/cloudwego/eino/components/tool"

    "eino-cli/backend/agent"
    "eino-cli/backend/agent/tools"
    "eino-cli/backend/config"
)

// BuildNamedSubagents 出每个 name 对应一个独立 deep agent,挂上 subagentRunner
// 中间件,把 timeout / journal 装好。返回值直接喂 deep.Config.SubAgents。
//
// 失败按 name 跳过 + warning,不让一份坏 profile 把 lead 拉垮。
func BuildNamedSubagents(
    ctx context.Context,
    cfg *config.Config,
) ([]adk.Agent, error) {

    names := orderedSubagentNames(cfg)
    if len(names) == 0 {
        return nil, nil
    }
    out := make([]adk.Agent, 0, len(names))
    for _, name := range names {
        sub, err := buildOneNamedSubagent(ctx, cfg, name)
        if err != nil {
            slog.Warn("subagent build skipped", "name", name, "err", err)
            continue
        }
        out = append(out, sub)
    }
    return out, nil
}

// buildOneNamedSubagent 是单个 named subagent 的装配函数:递归 MakeLeadAgent
// 拿一个标准 deep agent,再用 subagentRunner middleware 包一层。
func buildOneNamedSubagent(
    ctx context.Context, cfg *config.Config, name string,
) (adk.Agent, error) {

    profile := cfg.Subagents[name]   // 见 §5;不在 yaml 列表 = profile == nil
    inner, _, err := agent.MakeLeadAgent(ctx, name, false, false, cfg)
    if err != nil {
        return nil, err
    }
    // inner 自带 ToolsConfig = BuildBuiltinTools(cfg.RootDir);allow/deny 在
    // agent.MakeLeadAgent 内部更对路（紧靠 ToolsConfig 装配处）。本函数不
    // 再绕回去过滤,见 §4.1 改造点。

    timeout := effectiveTimeout(profile)
    mw := newSubagentMiddleware(subagentRunner{
        AgentName: name,
        Timeout:   timeout,
        RootDir:   cfg.RootDir,
    })
    return adk.WithAgentMiddlewares(inner, mw), nil
}

// orderedSubagentNames 出 yaml 里登记过的 subagent name 列表;按 map 遍历
// 顺序不稳,这里强制按字典序输出,task 工具描述生成结果可复现。
func orderedSubagentNames(cfg *config.Config) []string { ... }

func effectiveTimeout(p *config.SubagentProfile) time.Duration {
    const fallback = 15 * time.Minute
    if p == nil || p.DefaultTimeout <= 0 {
        return fallback
    }
    return time.Duration(p.DefaultTimeout) * time.Second
}

// filterTools 把 BuildBuiltinTools 的结果按 allow / deny 过滤,供
// agent.MakeLeadAgent 在 subagent 路径上调用。Allow 非空时取交集;Deny 永远
// 应用。task 名字总是被禁掉 —— 即使 yaml 写错配进去,也禁住递归。
func filterTools(all []tool.BaseTool, allow, deny []string) []tool.BaseTool {
    deny = append(append([]string{}, deny...), "task")
    ...
}
```

`adk.WithAgentMiddlewares` 不存在的话用 `adk.NewChatModelAgent` 的
`Middlewares` 字段（见 eino 源 `prebuilt/deep/task_tool.go:99`）—— 这部分调用
路径要在落地阶段对一下当前 eino 版本的 API；语义上等价。

---

## 4. 改造影响

### 4.1 `MakeLeadAgent`

签名加一个 **subagent profile** 参数,**仅**当 `IsSubagentEnabled=false` 也允许
透传 profile 时生效（即 named subagent 路径）。但这会让函数变成 6 个布尔位
摆动 —— 不行。

更干净的做法:`MakeLeadAgent` 接受 cfg 本身,自己读 `cfg.Subagents[name]` 决
定 tools 过滤。`name` 已经是参数；这等于直接消费 cfg。**对照 AGENTS.md 项目特
例:配置只用一个 `config.Config`,子模块用什么自己挑。**

变更点:

- `lead_agent.go` 在 `BuildBuiltinTools(cfg.RootDir)` 后追加一次
  `subagents.FilterToolsForProfile(allTools, cfg.Subagents[agentName])` 调用。
  当 `cfg.Subagents[agentName] == nil`(default lead 路径)是 no-op。
- 把 `IsSubagentEnabled` 的语义保持不变（依然由 `MakeLeadAgent` 决定要不要
  挂 eino 内置 general subagent）。

### 4.2 `buildLeadRunner` (deep_runtime.go)

```go
leadAgent, trace, err := agent.MakeLeadAgent(ctx, "default", false, true, cfg)
```

之前 `MakeLeadAgent` 内部并没有用 `cfg.Subagents` 给 `deep.Config.SubAgents`
赋值,我们在 `MakeLeadAgent` 内部加一行:

```go
subs, _ := subagents.BuildNamedSubagents(ctx, cfg)
deepCfg.SubAgents = subs
```

注意 import 顺序:`backend/agent/subagents` 反向依赖 `backend/agent` 的
`MakeLeadAgent` —— 必须把 `MakeLeadAgent` 留在 `backend/agent` 包,新建子包
`backend/agent/subagents` 调它,不能形成循环。`agent` 不导入 `subagents` 时
就没问题；但我们在 `MakeLeadAgent` 里要调 `subagents.BuildNamedSubagents`,
**会**形成循环。

解法两个,挑后者:

1. `agent → subagents → agent` 循环 —— 砍掉,把 named subagent 装配从
   `MakeLeadAgent` 提到 `deep_runtime.go` 的 `buildLeadRunner` 里。`MakeLeadAgent`
   只负责 single agent;subagent 编排是 runtime 层的事。
2. `MakeLeadAgent` 暴露一个 `MakeAgentForSubagent(ctx, cfg, name)` 不带递归,
   `subagents` 包基于它装配。

**选 1**:`buildLeadRunner` 在拿到 lead 之后,再让 `subagents.BuildNamedSubagents`
跑一遍,然后通过 `adk.WithSubAgents(lead, subs...)` 之类的 API 安装。如果
eino 现版不支持运行时挂 SubAgents,就把 `MakeLeadAgent` 拆成两段:`prepDeepCfg`
+ `finalize(deepCfg)`,中间允许调用方注入 SubAgents 后再 `deep.New`。

落地时按 eino API 选具体形态;不影响本 spec 的边界设计。

### 4.3 `BuildBuiltinTools`

不动签名。增加 `subagents.FilterTools(all, allow, deny)` 顶层函数,在
`MakeLeadAgent` 调用 `BuildBuiltinTools` 之后即可,不绕回 `tools` 包。

### 4.4 与 eino 通用 subagent 的去留

**保留**。理由:

- yaml 没配 named subagent 时,模型仍然有 task 工具可用(指向通用 subagent)。
- 通用 subagent 用同一 lead 的 system prompt + 全工具集,行为可预测;named
  subagent 是它的特化版,不是替代。
- `WithoutGeneralSubAgent` 让 yaml 一个 bool 控制(见 §5)。默认 false。

### 4.5 与 `SubagentLimit` middleware 的协作

`SubagentLimit` 工作在 lead 的 ChatModelMiddleware 上,在 model 输出 ToolCalls
之后、tool 真正调用之前截。它不关心 task 背后是 general 还是 named —— 看的
是工具名 `task` 出现次数。**所以本 spec 不改 SubagentLimit**;并发上限统一
靠 `cfg.MaxConcurrentSubagents`。

---

## 5. 配置变更

### 5.1 yaml 段

新增顶级 `subagents:` map,key 是 name,value 是 profile:

```yaml
# ============================================================================
# Named Subagents (task tool)
# ============================================================================
# Each entry becomes one option in the lead agent's `task` tool.
# Missing field → inherits from default lead (model / tools / skills).
# tools_allow + tools_deny operate on tool names returned by
# tools.BuildBuiltinTools; "task" is always force-denied to block nesting.
# default_timeout is seconds; zero/unset → 15 min fallback.
subagents:
  researcher:
    description: "Read-only exploration agent for codebase questions."
    model: gpt-5-fast        # absent → inherit cfg.DefaultModel
    tools_allow:             # absent → all tools
      - read_file
      - glob
      - grep
      - rg
      - semantic_search
      - ls
    tools_deny: []
    skills_allow: []         # absent → cfg.Skills.Enabled; [] → no skills
    default_timeout: 600
  shell:
    description: "Execute bash for git ops, builds, terminal tasks."
    tools_allow:
      - execute
      - shell
      - await_shell
      - read_file
      - ls
    default_timeout: 900

# Disable eino's built-in general-purpose subagent. Leave false when you
# want named subagents AND general to co-exist. true = only named subagents
# show up in the task tool description.
without_general_subagent: false
```

### 5.2 `cfg` 端读取位置

`backend/config/types.go` 新增:

```go
type SubagentProfile struct {
    Description     string   `yaml:"description"`
    Model           string   `yaml:"model,omitempty"`
    ToolsAllow      []string `yaml:"tools_allow,omitempty"`
    ToolsDeny       []string `yaml:"tools_deny,omitempty"`
    SkillsAllow     []string `yaml:"skills_allow,omitempty"`
    DefaultTimeout  int      `yaml:"default_timeout,omitempty"`
}

type Config struct {
    // ...existing...
    Subagents              map[string]*SubagentProfile `yaml:"subagents"`
    WithoutGeneralSubagent bool                        `yaml:"without_general_subagent"`
}
```

读取入口:`backend/agent/subagents/task_tool.go:BuildNamedSubagents` 直接读
`cfg.Subagents`;`backend/agent/lead_agent.go` 的 `MakeLeadAgent` 用
`cfg.WithoutGeneralSubagent` 直接覆盖 `deepCfg.WithoutGeneralSubAgent`(注意
是 OR:`!IsSubagentEnabled || cfg.WithoutGeneralSubagent`)。

### 5.3 `yaml/CHANGELOG.md` 要补的条目

按 `AGENTS.md` 强制要求,新增一条:

````markdown
## 2026-05-14: subagents + without_general_subagent

新增顶级段,在 `hitl_tools` 下面、`models` 上面:

```yaml
subagents:
  researcher:
    description: "Read-only exploration agent for codebase questions."
    model: gpt-5-fast
    tools_allow:
      - read_file
      - glob
      - grep
      - rg
      - semantic_search
      - ls
    default_timeout: 600
  shell:
    description: "Execute bash for git ops, builds, terminal tasks."
    tools_allow:
      - execute
      - shell
      - await_shell
      - read_file
      - ls
    default_timeout: 900

without_general_subagent: false
```

驱动:
- `backend/config/types.go` 新增 `Config.Subagents map[string]*SubagentProfile`、`Config.WithoutGeneralSubagent bool`、`SubagentProfile` 结构体。
- `backend/agent/subagents/task_tool.go` 读 `cfg.Subagents`,把每个 profile 转成 named subagent。
- `backend/agent/lead_agent.go` 用 `cfg.WithoutGeneralSubagent` OR 进 `deepCfg.WithoutGeneralSubAgent`。
- `backend/agent/subagents/runner.go` 用 `profile.DefaultTimeout` 套 ctx。

背景:`specs/20260514-feature-comparison/step3-subagent-task-tool.md`。
````

---

## 6. TUI 集成（可选小尾巴）

最小可用先什么都不做:eino 内置 task tool 的输入输出会走 `Trace` 中间件,在
TUI 已经会渲染成普通 tool block(args = `{subagent_type, description}`,result
= `[task: ... | <status>] <summary>`)。够看了。

后续增项,按价值排序:

1. **`/subagents` slash 命令**:`ls .eino-cli/subagents/*.jsonl` 列最近几条
   journal,带 status 颜色。零成本。
2. **`/debug` 输出 subagent journal 路径**:trace event 里携带 `run_id`,debug
   block footer 加一行 `journal: .eino-cli/subagents/a1b2c3d4.jsonl`。
3. **streaming 进度**:`subagentRunner` 把 inner iter 的 assistant chunks
   fan-out 给一个 `progress chan`,TUI 渲染成嵌套 tool block 的子节点。
   要等 §2.5 streaming sink 抽象完才做,不在本 spec 范围。

---

## 7. 测试计划

### 7.1 单测

- `journal_test.go`:
  - `writeJournalEntry` round-trip(写两条 → 重新 open → 行数对、JSON 可解析)。
  - `payload` 超长被截到 `maxPayloadBytes`,不破坏 JSON 语法(尾部不留半字符)。
- `runner_test.go`(用 fake `adk.AgentRunFunc` 注入):
  - 正常返回 → journal 收到 `start` + `final/succeeded` 两行。
  - `ctx.WithTimeout` 触发 `DeadlineExceeded` → `final/timeout`。
  - 外层 ctx cancel → `final/cancelled`。
  - inner 返回业务 err → `final/failed`,payload 携带 err.Error() 截短。
- `task_tool_test.go`:
  - `cfg.Subagents` 为空 → `BuildNamedSubagents` 返回 nil。
  - 一个坏 profile(model 不存在) + 一个好 profile → 出 1 个 agent,日志 warn。
  - `filterTools` 同时给 allow/deny,deny 优先,`task` 强制 deny。

### 7.2 端到端(进 `backend/runtime/eino/runtime_test.go`)

- mock chat model:第一轮回 `task(subagent_type="researcher", description="explore /foo")` 的 tool call;
  named subagent 用同一 mock 但回普通 assistant 文本"done"。校验:
  - 主 history 看到一条 tool message 内容是 `[task: researcher | <id> | succeeded]\ndone`。
  - `.eino-cli/subagents/<id>.jsonl` 存在,行数 ≥ 2。

### 7.3 失败路径

- 把 `researcher.default_timeout` 设 50ms,subagent 死循环 → tool message
  status=timeout,主 agent 看到合理错误描述继续。
- subagent profile 配 `tools_allow: ["read_file"]`,subagent 试图调用
  `write_file` → eino 自然拒(工具不在 bind 列表,模型不会调出来);测试用
  `MakeLeadAgent` 的 tools list 校验 `write_file` 不在里面。
- 嵌套深度:让 researcher 的 mock 输出再 `task(...)` 调用 —— `MakeLeadAgent(name, false, false, cfg)`
  路径上没挂 task 工具,模型即便回 ToolCalls 也会被 eino 报"tool not found"。
  测试断言主 history 不出现第二条 task 调用。

### 7.4 并发

- 在 fake model 单次返回 6 个并行 task 调用,`cfg.MaxConcurrentSubagents=3`。
  断言 `SubagentLimit` 把后 3 个截掉(单测已有,本 spec 不重复)。

---

## 8. Commit 拆分建议

按 `AGENTS.md` "Commit 粒度":每个 commit 一句话说得清,行为变更和重命名分
开。建议 7 个 commit:

1. `subagents: 搬包,move backend/agent/subagents.go → backend/agent/subagents/task_tool.go`
   (纯文件移动 + import 修;不改逻辑;测试同步搬)。
2. `config: 加 SubagentProfile + WithoutGeneralSubagent`(只动 `types.go` / `yaml.go`,无消费方;
   CHANGELOG 同 commit)。
3. `subagents: journal.go(写 + 测)`。
4. `subagents: subagentRunner middleware + runSubagentTask`(纯新增,无人调用)。
5. `subagents: BuildNamedSubagents 串起 profile → middleware,filterTools 顶层函数`。
6. `agent/lead: MakeLeadAgent 消费 cfg.Subagents + WithoutGeneralSubagent;runtime/eino: buildLeadRunner 注入 named subagents`。
7. `tests: e2e + 失败路径`。

PR 拆 1+2(配置铺垫)、3-5(subagent 包内自闭)、6-7(接线 + e2e)三组也合理。

---

## 9. 副作用与回滚

### 9.1 内存

- subagent 自己的 history 不进主 history;主 history 只多一个 tool message
  (受 §2.1 摘要长度限制,典型 < 4KB)。
- journal 直接落盘,内存里**不**驻留 background task dict(对照 deer-flow:
  我们走同步路径,迭代器一关闭就 GC)。
- 风险点:subagent 跑超长任务时,inner 的 ChatModel middleware 链
  (Summarization / Memory)可能向磁盘写大文件;由各自中间件的现有上限管,不
  是本 spec 的事。

### 9.2 Checkpoint

- 现 `adk.Runner` 的 `CheckPointStore` 是 lead 级的,checkpoint id 不会被
  subagent 中间状态污染:subagent 用的是 eino `AgentTool.InvokableRun` 自己内
  部的子 session,不复用 lead 的 store(eino 源码可验证)。
- journal 文件**不**等于 checkpoint;`/clear` 不应该删 journal —— 那是历史
  审计,跨 session 仍要保留。

### 9.3 嵌套

- 禁:见 §2.2,subagent 路径(`MakeLeadAgent(name, false, false, cfg)`)上 eino
  天然不挂 task 工具。
- `filterTools` 里硬塞一道 `deny ∋ "task"` 兜底,即使别处 wiring 改动错误地
  让 task 工具混进去,这一层也截住。

### 9.4 回滚

最简回滚 = 在 yaml 删 `subagents:` + `without_general_subagent: false`:
`BuildNamedSubagents` 返回 nil,eino 仍然只挂 general subagent,行为与
本 spec 落地前等价。代码不必回滚。

完整回滚 = revert commit 6(接线层),保留 1-5(纯新增,无 callsite)。risk-free。

---

## 10. 不做的事

- **不**做后台异步 task / `task_status` 工具:同步等够用;模型一次工具调用
  返回一次摘要,符合 LLM tool use 心智。后续需要再加。
- **不**做沙箱(独立 cwd / tmpdir / 网络隔离):跨 spec,先做"控制面"再做
  "执行面"。
- **不**做 subagent 间通信:每个 task 调用独立 session;有信息要传通过 lead
  的下一轮 prompt。
- **不**给 task 工具加 `timeout` 参数:配置职责,见 §2.1。

---

## 11. 关键引用

| 文件 | 角色 |
|---|---|
| `backend/agent/subagents.go` | 旧 `buildNamedSubagents`,本 spec 搬到子包 |
| `backend/agent/lead_agent.go` | `MakeLeadAgent` 装配 `deep.Config`,要消费 `cfg.Subagents` |
| `backend/agent/middlewares/subagent_limit.go` | 并发上限 — 不动 |
| `backend/agent/prompt.go` `effectiveMaxSubagents` | 单一并发数来源 — 不动 |
| `backend/runtime/eino/deep_runtime.go` `buildLeadRunner` | 注入 named subagents 的点 |
| `backend/config/types.go` | 加 `SubagentProfile / WithoutGeneralSubagent` |
| `yaml/CHANGELOG.md` | 必补条目,见 §5.3 |
| eino `adk/prebuilt/deep/task_tool.go` | task 工具底层实现 — 已存在,复用 |
| eino `adk/prebuilt/deep/deep.go` `Config.SubAgents` | named subagent 注入字段 |
| deer-flow `subagents/executor.py` | status / cancel / journal 思路来源(抽象,不直译) |
| deer-flow `subagents/registry.py` | builtin + custom 合并思路;本 spec 只做 custom |
| deer-flow `tools/builtins/task_tool.py` | 工具签名参考(我们复用 eino 内置签名,不照抄) |
