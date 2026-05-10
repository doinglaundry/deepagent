# Memory 富结构化重构 — 技术设计文档

把 Go 这边目前扁平的 `Memory{Key, Content, TurnIndex}` memory 模型改造成跟
deer-flow 对齐的三段式结构(`user.* / history.* / facts[]`),并补齐目前缺失
的写入闭环(LLM-based memory updater)。

## 0. 决策已锁定

| 项 | 决策 |
|---|---|
| 范围 | **B**: 完整闭环(读 + 写 + 自动 update) |
| 老数据 | **a**: 直接废弃,启动不读老格式 |
| Token 计数 | **chars/4** 估算,不引 tiktoken-go |
| Updater 模型 | **复用主对话模型**(`rt.ModelCfg`),`config.Memory.ModelName` 字段保留但本期不读 |
| Updater 触发 | **(iii)** debounce + `/clear` 强制 flush(详见 §1) |
| LLM 调用超时 | 常量 `memoryUpdateTimeout = 60 * time.Second`,`context.WithTimeout` 包一层 |
| Force flush 超时 | 常量 `memoryFlushTimeout = 5 * time.Second`,`/clear` 时短 ctx,超时即放弃 |
| Commit 策略 | **两步切换**(store + reader 一个 commit,updater 一个 commit) |
| 类型设计 | **(R)** 不引入 `MemoryAccessor` 类型;`MemoryUpdater` 保留(因为有 `mu`/`lastRunAt` 真状态)。reader 是顶层函数,wiring 闭包做。详见 §6 |
| Wiring 创建位置 | **(②)** `store` / `updater` 都在消费方内部建(`GetSystemPrompt` 内自建 store,`GetChatModelMiddlewares` 内自建 store + updater)。`MakeLeadAgent` 不持有任何 memory 引用。详见 §9 |
| Inject agentName | `rt.AgentName`,Load 不存在 → empty,不降级到 global。 |

---

## 1. 触发频率(已定: iii)

Python 是"每轮 AfterModel 后触发,LLM 调用本身在线程池里跑"。直接照搬到 CLI
不合适——CLI 单 user、单进程,每轮都发一个跟主模型同等规模的 LLM 请求,
成本和延迟都不划算。

最终行为:

- **触发点**: middleware 框架在 `AfterModelRewriteState` 后用 goroutine
  调用我们提供的 `MemoryHooks.Extract` 闭包(在 §9.2 `GetChatModelMiddlewares`
  里组装)。
- **debounce**: 上次成功 update 至今 `< cfg.Memory.DebounceSeconds` 则跳过。
  `DebounceSeconds == 0` 视作"不防抖,每轮跑";`< 0` 视作"禁用 updater"。
  Python 已经在 yaml 里定义了这个字段,正好复用。
- **会话结束 / `/clear` 强制 flush**: 复用现有 `FlushBeforeSummarization`
  钩子,在那里调 `Run` 时绕过 debounce(传 `force=true`),并用 5s 短 ctx
  防止阻塞 UI。

---

## 2. 总体架构(数据流)

```
启动 (lead_agent.go):
  prompt   := agent.GetSystemPrompt(rt, cfg)                     // 不持 store
  handlers := agent.GetChatModelMiddlewares(ctx, cfg, rt, chatModel)  // 不持 store/updater

读路径 (每次构建 prompt):
  GetSystemPrompt(rt, cfg)
    → store := memorystore.NewStoreFromConfig(cfg)              // 派生 (Store 无状态)
    → getMemoryPrompt(rt.AgentName, store, cfg.Memory)           // prompt.go 内部 helper
    → agent.GetMemoryPromptBlock(store, agentName, cfg.Memory.MaxInjectionTokens)
       → store.Load(agentName)                                   // 单文件 JSON
       → formatMemoryForInjection(data, maxTokens)
       → "<memory>...</memory>"

写路径 (每轮 AfterModel 异步):
  GetChatModelMiddlewares 内部 (cfg.Memory.Enabled 时):
    store   := memorystore.NewStoreFromConfig(cfg)              // 派生
    updater := agent.NewMemoryUpdater(store)                     // 持 mu+lastRunAt 状态
    hooks   := middlewares.MemoryHooks{Inject, Extract}          // 闭包捕获 store/updater/chatModel/cfg.Memory/rt.AgentName

  middleware.Memory.AfterModelRewriteState
    → go hooks.Extract(ctx, msgs)
       → updater.Run(ctx, chatModel, cfg.Memory, rt.AgentName, msgs, force=false)
          → mu.Lock(); defer Unlock()
          → debounce check (跳过若 time.Since(lastRunAt) < DebounceSeconds)
          → ctx, cancel := WithTimeout(ctx, 60s); defer cancel()
          → store.Load(agentName)
          → buildUpdatePrompt(current, formatConversationForUpdate(msgs))
          → chatModel.Generate(ctx, prompt)                     // 主对话模型
          → parseUpdatePayload(resp.Content)                    // 剥 ``` + json.Unmarshal
          → applyUpdate(current, payload, cfg.Memory)            // shouldUpdate / newFacts / factsToRemove
          → store.Save(rt.AgentName, updated)
          → lastRunAt = time.Now()                              // 仅成功才更新

强制 flush 路径 (/clear / summarization, 同步):
  flushHook(ctx, before, after)                                  // 同上闭包, 跟 Extract 共用 updater 实例
    → flushCtx, cancel := WithTimeout(ctx, 5s); defer cancel()
    → updater.Run(flushCtx, chatModel, cfg.Memory, rt.AgentName, before.Messages, force=true)
```

---

## 3. Schema 定义

新增 `backend/memory/store/data.go`。JSON tag 跟 deer-flow 文件格式 1:1 对齐,
方便后续如有人手写 / 跨工具读写都能 work。

```go
type MemoryData struct {
    Version     string      `json:"version"`             // 固定 "1.0"
    LastUpdated string      `json:"lastUpdated"`         // ISO-8601 + Z
    User        UserContext `json:"user"`
    History     History     `json:"history"`
    Facts       []Fact      `json:"facts"`
}

type Section struct {
    Summary   string `json:"summary"`
    UpdatedAt string `json:"updatedAt,omitempty"`
}

type UserContext struct {
    WorkContext     Section `json:"workContext"`
    PersonalContext Section `json:"personalContext"`
    TopOfMind       Section `json:"topOfMind"`
}

type History struct {
    RecentMonths       Section `json:"recentMonths"`
    EarlierContext     Section `json:"earlierContext"`
    LongTermBackground Section `json:"longTermBackground"`
}

type Fact struct {
    ID          string  `json:"id"`                     // "fact_" + 8 hex
    Content     string  `json:"content"`
    Category    string  `json:"category"`               // free string, 见下
    Confidence  float64 `json:"confidence"`             // 0~1, load 时 clamp
    SourceError string  `json:"sourceError,omitempty"`
    CreatedAt   string  `json:"createdAt,omitempty"`
    Source      string  `json:"source,omitempty"`       // "llm" | "manual"
}
```

`Category` 合法值(只在文档约束,代码不强校验):
`preference | knowledge | context | behavior | goal | correction`

辅助函数(同包,公开 / 不公开命名):

```go
// GetEmptyMemoryData 跟 deer-flow `create_empty_memory` 对齐。
// Version 固定 "1.0"; LastUpdated 设为 utcNowISO()。
func GetEmptyMemoryData() MemoryData

// coerceConfidence 把任意 float64 钳到 [0, 1]; NaN / +Inf / -Inf → 0。
func coerceConfidence(v float64) float64

// utcNowISO 返回 UTC ISO-8601 + Z, 例如 "2026-05-10T08:30:00Z"。
// 实现: time.Now().UTC().Format("2006-01-02T15:04:05Z")
func utcNowISO() string

// newFactID 生成 "fact_" + 8 位随机 hex。
// 实现: crypto/rand 8 字节 → hex.EncodeToString → 取前 16 字符 / 8 字符。
// 选择 crypto/rand 而不是 math/rand: 避免多 goroutine 抢同一 source 的 race。
func newFactID() string
```

时间字段统一用 `string` 而不是 `time.Time`,理由:
- deer-flow 文件兼容(避免 `Z` vs `+00:00` 的 marshal 坑)
- AGENTS.md "结构体只装数据"——这些字段就是给 JSON 的,不参与 Go 侧的时间运算

### 3.1 文件骨架: `backend/memory/store/data.go`

```go
package store

const memoryFormatVersion = "1.0"

type MemoryData struct  { /* 见上 */ }
type Section     struct { /* 见上 */ }
type UserContext struct { /* 见上 */ }
type History     struct { /* 见上 */ }
type Fact        struct { /* 见上 */ }

// 公开: applyUpdate / 测试也要用
func GetEmptyMemoryData() MemoryData
func CoerceConfidence(v float64) float64
func NewFactID() string

// 包内
func utcNowISO() string
```

---

## 4. Store 层

`backend/memory/store/store.go` 整体重写。三个公开入口:

```go
const memorySubdir = ".eino-cli/memory"

type Store struct{ dir string }

// 直接给 dir 字符串构造; 测试用 t.TempDir() 时的入口。
func NewStore(dir string) *Store

// 从 cfg 直接派生 store; 生产代码主入口 (lead_agent / GetSystemPrompt /
// GetChatModelMiddlewares 都通过它得到 Store)。Store 无状态, 多次调用
// 等价。
func NewStoreFromConfig(cfg *config.Config) *Store

func (s *Store) Load(agentName string) (MemoryData, error)
func (s *Store) Save(agentName string, data MemoryData) error
```

`NewStoreFromConfig` 实现就一行:

```go
func NewStoreFromConfig(cfg *config.Config) *Store {
    return NewStore(filepath.Join(cfg.RootDir, memorySubdir))
}
```

文件布局:

```
{cfg.RootDir}/.eino-cli/memory/global.json                  # agentName == ""
{cfg.RootDir}/.eino-cli/memory/agents/<safeName>.json       # agentName != ""
```

`safeName` 校验:正则 `^[A-Za-z0-9][A-Za-z0-9_\-]{0,63}$`,跟 deer-flow
`AGENT_NAME_PATTERN` 同等严格。校验失败 `Save` 直接返回 error,`Load`
返回 empty + 不写盘。

实现要点:

- `Load`: 文件不存在 → 返回 `GetEmptyMemoryData()`, no error;存在但
  unmarshal 失败 → warn log + 返回 empty, no error(deer-flow 兼容);存在
  且解析成功 → 对每个 fact 做 `coerceConfidence` + 默认值填充。
- `Save`: 原子写,流程见下。
- **不缓存**: 每次 Load 直接读盘。CLI 单进程短生命,不需要 mtime cache;
  少一层状态。
- **不显式 lock**: 单进程内的串行化由上层 `MemoryUpdater.mu` 保证;跨进程
  不防(目前没多进程场景)。

### 4.1 原子写入流程

目标: 任何崩溃 / 断电 / kill 都不留下半截 JSON。POSIX `rename(2)` 是原子的,
所以"先写临时文件,再 rename 到目标"这一招在所有 Unix 上都成立。

伪代码:

```go
func (s *Store) Save(agentName string, data MemoryData) error {
    path, err := s.getPath(agentName)        // 同时校验 agentName
    if err != nil { return err }

    err = os.MkdirAll(filepath.Dir(path), 0o755)
    if err != nil { return fmt.Errorf("mkdir memory dir: %w", err) }

    payload, err := json.MarshalIndent(data, "", "  ")
    if err != nil { return fmt.Errorf("marshal memory: %w", err) }

    // 临时文件名: <path>.<8hex>.tmp
    // - 后缀 ".tmp" 是约定, 让 ls 看一眼就知道写没写完;
    // - 中间 8 位随机 hex 防同进程并发 Save 撞车。
    var nonce [4]byte
    _, err = rand.Read(nonce[:])              // crypto/rand
    if err != nil { return fmt.Errorf("rand nonce: %w", err) }
    tmp := fmt.Sprintf("%s.%x.tmp", path, nonce)

    err = os.WriteFile(tmp, payload, 0o644)
    if err != nil { return fmt.Errorf("write tmp memory: %w", err) }

    err = os.Rename(tmp, path)                // 原子提交
    if err != nil {
        _ = os.Remove(tmp)                    // 失败时清残留
        return fmt.Errorf("rename tmp memory: %w", err)
    }
    return nil
}
```

如果中间任何一步失败:
- `WriteFile` 失败 → tmp 文件可能存在 / 半截,但目标文件没动。下次 Save
  会用新 nonce 写新 tmp,旧的 tmp 会被磁盘"遗忘"(可接受;清不清都行)。
- `Rename` 失败 → 显式 `os.Remove(tmp)` 清残留。
- 进程突然死(kill -9 / 断电)→ 目标文件保持上次完整版本,tmp 文件可能
  残留(可接受;启动期我们不读 .tmp 文件)。

### 4.2 废弃接口

`LoadAll` / `Save(Memory)` / `Find` / `Memory{Key,Content,...}` /
`TaskMemoryPrefix` 全删。

### 4.3 文件骨架: `backend/memory/store/agent_name.go`

```go
package store

import "regexp"

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_\-]{0,63}$`)

// validateAgentName 返回错误时不写盘 / 不读盘。空 agentName 合法 (== global)。
func validateAgentName(name string) error
```

### 4.4 文件骨架: `backend/memory/store/store.go`

```go
package store

const memorySubdir = ".eino-cli/memory"

type Store struct{ dir string }

func NewStore(dir string) *Store
func NewStoreFromConfig(cfg *config.Config) *Store

func (s *Store) Load(agentName string) (MemoryData, error)
func (s *Store) Save(agentName string, data MemoryData) error
func (s *Store) getPath(agentName string) (string, error)       // 包内, 派生路径 + 校验
```

老数据处理: 启动**完全不读** `{dir}/*.json` 老格式。后果:
- 用户感知"上次的记忆没了"。可接受——`store.Save(Memory)` 在生产代码里没
  调用方,这块本来就没真正在用。
- 老 `task<...>.json` 等文件会被新 store 忽略(新 store 只读 `global.json`
  和 `agents/<name>.json`),不会误吃。
- 极小概率冲突: 老格式如果恰好有 `global.json`,新 store 会按新 schema 解析
  失败 → 返回 empty。可接受。
- commit message 里写明"如有老数据想清掉,自行 `rm -rf ~/.eino-cli/memory`"。

---

## 5. Renderer 层

新增 `backend/agent/memory_render.go`。唯一公开函数:

```go
func formatMemoryForInjection(data MemoryData, maxTokens int) string
```

完整翻译 deer-flow `format_memory_for_injection`,输出格式 1:1 对齐。

**关键差异 vs 现状**: 现在 Go 输出是 `- (turn N) content` 一行式,新输出是
分段 markdown-ish:

```
User Context:
- Work: <summary>
- Personal: <summary>
- Current Focus: <summary>

History:
- Recent: <summary>
- Earlier: <summary>
- Background: <summary>

Facts:
- [preference | 0.95] user prefers tabs
- [correction | 0.95] avoid X (avoid: tried Y last time)
```

伪代码逻辑(直译 deer-flow):

```
sections = []

if user.* 任一段 summary 非空:
    user_lines = []
    if WorkContext.Summary != "":     user_lines += "Work: ..."
    if PersonalContext.Summary != "": user_lines += "Personal: ..."
    if TopOfMind.Summary != "":       user_lines += "Current Focus: ..."
    sections += "User Context:\n" + bullets(user_lines)

if history.* 任一段 summary 非空:
    同上 (Recent / Earlier / Background)

if len(facts) > 0:
    sorted_facts = sort by Confidence desc
    base_tokens = countTokens(join(sections, "\n\n"))
    sep_tokens  = countTokens("\n\nFacts:\n") if sections else countTokens("Facts:\n")
    running     = base_tokens + sep_tokens
    fact_lines  = []
    for f in sorted_facts:
        if f.Content trimmed empty: continue
        category   = f.Category or "context"
        confidence = coerceConfidence(f.Confidence)
        if category == "correction" and SourceError != "":
            line = "- [%s | %.2f] %s (avoid: %s)"
        else:
            line = "- [%s | %.2f] %s"
        line_text   = ("\n" + line) if fact_lines else line
        line_tokens = countTokens(line_text)
        if running + line_tokens > maxTokens: break
        fact_lines += line; running += line_tokens
    if fact_lines: sections += "Facts:\n" + join(fact_lines, "\n")

if sections empty: return ""

result = join(sections, "\n\n")
total  = countTokens(result)
if total > maxTokens:
    char_per_token = len(result) / total          // 这里 == 4,因为 chars/4
    target         = int(maxTokens * char_per_token * 0.95)
    result         = result[:target] + "\n..."

return result
```

`countTokens(s) = len(s) / 4`(字符级估算)。

注:由于 chars/4 是 deterministic,二次截断里的
`char_per_token = len(result)/total` 恒等于 4,等价于
`result[:int(maxTokens*4*0.95)] + "\n..."`。但保留这个写法,跟 deer-flow
形态一致,后续替换 tokenizer 时不用改逻辑。

`maxTokens <= 0` 的处理:deer-flow 里没有这分支(默认 2000)。Go 这里语义沿用
现有 `getMemoryPrompt`——`<= 0` 表示"不限制",此时 token 检查全部跳过,facts
全量输出。

### 5.1 文件骨架: `backend/agent/memory_render.go`

```go
package agent

const charsPerToken = 4

// 公开: GetMemoryPromptBlock 在同包但本身是 reader 层公开 API,
// formatMemoryForInjection 只在 reader / updater 内部用,小写即可。
func formatMemoryForInjection(data memorystore.MemoryData, maxTokens int) string

// 包内 helpers
func countTokens(s string) int                          // = len(s) / charsPerToken
func renderUserSection(u memorystore.UserContext) string
func renderHistorySection(h memorystore.History) string
func renderFactsSection(facts []memorystore.Fact, runningTokens, maxTokens int) (string, int)
```

---

## 6. Reader 层(方案 R: 顶层函数 + wiring 闭包)

### 6.1 设计原则

按 AGENTS.md "结构体只装数据,函数承载行为":本期**不引入** `MemoryAccessor`
类型。理由:

- 它的字段如果只剩 `store + updater`,等于一个把这俩攒在一起的胶水容器,
  没有真正"必须一起出现"的状态——`store` 在 `MakeLeadAgent` 里就能拿到,
  `updater` 也是。
- 它的方法 `GetPromptBlock` / `FlushBeforeSummarization` / `Hooks` 都不依赖
  receiver 的状态,完全可以是顶层函数。
- middleware 框架的 callback 接缝(`MemoryHooks` / `SummarizationMemoryFlushHook`)
  签名固定,需要闭包捕获依赖——闭包在 `GetChatModelMiddlewares` 里直接
  组装,比"挂 receiver method"更显式。

`MemoryUpdater` 类型保留——它有 `mu` + `lastRunAt` 真状态,跨多个 `Run`
调用必须存活。

### 6.2 公开函数签名

`backend/agent/memory.go` 整体替换为:

```go
package agent

// GetMemoryPromptBlock 加载 agent 的 memory 并渲染成
// "<memory>...</memory>" 块; nil store / 空 memory / 加载失败均返回 ""。
func GetMemoryPromptBlock(
    store *memorystore.Store,
    agentName string,
    maxTokens int,
) string

// InjectMemory 把 memory 块作为 system message 前置到 messages 里。
// memory disabled / injection disabled / 块为空时直接返回原 messages。
// 给 middleware Inject 闭包用。
func InjectMemory(
    store *memorystore.Store,
    cfg config.Memory,
    agentName string,
    msgs []*schema.Message,
) []*schema.Message
```

`prompt.go` 里的 `getMemoryPrompt` 改造成调 `GetMemoryPromptBlock`,
`GetSystemPrompt` 内部自建 store(从 cfg 派生):

```go
// prompt.go (内部 helper, 调用 reader 层公开 API)
func getMemoryPrompt(agentName string, store *memorystore.Store, cfg config.Memory) string {
    if !cfg.Enabled || !cfg.InjectionEnabled { return "" }
    block := GetMemoryPromptBlock(store, agentName, cfg.MaxInjectionTokens)
    if block == "" { return "" }
    return block + "\n"
}

// GetSystemPrompt 不再接 *MemoryAccessor 也不接 *memorystore.Store; store
// 在内部从 cfg 派生 (Store 无状态, 派生开销可忽略)。
func GetSystemPrompt(rt RuntimeContext, cfg *config.Config) string {
    store := memorystore.NewStoreFromConfig(cfg)
    memoryContext := getMemoryPrompt(rt.AgentName, store, cfg.Memory)
    // ...
}
```

### 6.3 行为细节

```go
func GetMemoryPromptBlock(
    store *memorystore.Store,
    agentName string,
    maxTokens int,
) string {
    if store == nil { return "" }
    data, err := store.Load(agentName)
    if err != nil { return "" }                       // 防御; store 内部已 warn log
    body := formatMemoryForInjection(data, maxTokens)
    if body == "" { return "" }
    return "<memory>\n" + body + "\n</memory>"
}

func InjectMemory(
    store *memorystore.Store,
    cfg config.Memory,
    agentName string,
    msgs []*schema.Message,
) []*schema.Message {
    if !cfg.Enabled || !cfg.InjectionEnabled { return msgs }
    block := GetMemoryPromptBlock(store, agentName, cfg.MaxInjectionTokens)
    if block == "" { return msgs }
    out := make([]*schema.Message, 0, len(msgs)+1)
    out = append(out, schema.SystemMessage(block))
    out = append(out, msgs...)
    return out
}
```

### 6.4 废弃符号

`backend/agent/memory.go` 现有内容全部移除:

- 类型: `MemoryAccessor` / `memoryDataKey`
- 构造器: `NewMemoryAccessor`
- 方法: `GetPromptBlock` / `FormatMemoryForInjection` / `loadFiltered` /
  `Hooks` / `inject` / `extract` / `FlushBeforeSummarization`
- 顶层函数: `renderMemoryBullets` / `filterMemories`
- 常量: `memoryMaxItems` / `memoryMinContentLen`

替换成 §6.2 的两个顶层函数。文件大概会从现在 ~160 行缩到 ~30 行。

### 6.5 文件骨架: `backend/agent/memory.go`(重写后)

```go
package agent

import (
    "github.com/cloudwego/eino/schema"

    "eino-cli/backend/config"
    memorystore "eino-cli/backend/memory/store"
)

// 仅两个公开顶层函数; reader 行为不挂 receiver。
func GetMemoryPromptBlock(store *memorystore.Store, agentName string, maxTokens int) string
func InjectMemory(store *memorystore.Store, cfg config.Memory, agentName string, msgs []*schema.Message) []*schema.Message
```

---

## 7. Updater 层

新增 `backend/agent/memory_updater.go` + `backend/agent/memory_update_prompt.go`。

### 7.1 类型签名

按"struct 只装真状态"原则,`MemoryUpdater` 只持 `store + mu + lastRunAt`。
`chatModel` / `cfg` 都通过 `Run` 参数传:

```go
const (
    memoryUpdateTimeout = 60 * time.Second  // LLM 调用单次上限
    memoryFlushTimeout  = 5 * time.Second   // /clear 强制 flush 上限
)

type MemoryUpdater struct {
    store *memorystore.Store

    mu        sync.Mutex
    lastRunAt time.Time
}

func NewMemoryUpdater(store *memorystore.Store) *MemoryUpdater {
    return &MemoryUpdater{store: store}
}

// Run 异步触发或 force flush; 串行化由 mu 保证。
func (u *MemoryUpdater) Run(
    ctx context.Context,
    chatModel model.BaseChatModel,
    cfg config.Memory,
    agentName string,
    messages []*schema.Message,
    force bool,
) error
```

### 7.2 触发判定 + Run 主体框架

```go
func (u *MemoryUpdater) Run(
    ctx context.Context,
    chatModel model.BaseChatModel,
    cfg config.Memory,
    agentName string,
    messages []*schema.Message,
    force bool,
) error {
    if !cfg.Enabled { return nil }
    if chatModel == nil { return nil }                      // 防御
    if len(messages) == 0 { return nil }

    u.mu.Lock()
    defer u.mu.Unlock()

    // debounce: 上次成功 update 离现在不够久就跳过
    if !force && cfg.DebounceSeconds > 0 {
        if time.Since(u.lastRunAt) < time.Duration(cfg.DebounceSeconds)*time.Second {
            return nil
        }
    }

    // 单次 LLM 调用包一层超时
    runCtx, cancel := context.WithTimeout(ctx, memoryUpdateTimeout)
    defer cancel()

    // 1. 加载当前 memory
    current, err := u.store.Load(agentName)
    if err != nil { return fmt.Errorf("load memory: %w", err) }

    // 2. 组装对话文本; 空对话直接退出
    convo := formatConversationForUpdate(messages)
    if strings.TrimSpace(convo) == "" { return nil }

    // 3. 构造 prompt
    prompt, err := buildUpdatePrompt(current, convo)
    if err != nil { return fmt.Errorf("build prompt: %w", err) }

    // 4. 调 LLM
    resp, err := chatModel.Generate(runCtx, []*schema.Message{schema.UserMessage(prompt)})
    if err != nil { return fmt.Errorf("memory llm: %w", err) }

    // 5. 解析 + 应用更新
    payload, err := parseUpdatePayload(resp.Content)
    if err != nil { return fmt.Errorf("parse update: %w", err) }

    updated := applyUpdate(current, payload, cfg)

    // 6. 落盘成功才更新 lastRunAt
    err = u.store.Save(agentName, updated)
    if err != nil { return fmt.Errorf("save memory: %w", err) }

    u.lastRunAt = time.Now()
    return nil
}
```

注意点:
- **`mu.Lock` 在 ctx 超时检查之前**: 这样并发触发的第二个 Run 会等第一个,
  排队序列化。如果担心 lock 等待时间太长,可以改成 `TryLock`(Go 1.18+),
  抢不到锁直接返回 nil。本期采用阻塞,简单优先。
- **`lastRunAt` 在落盘成功后才更新**: 任何中间步骤失败,都不算"用掉"
  debounce 窗口,下一次 extract 会再尝试。
- **ctx cancel 行为**: 主进程退出 / `/clear` flush 超时 → ctx canceled →
  `chatModel.Generate` 返回 ctx.Err() → `Run` 返回 wrapped error → 调用方
  log warn。无 goroutine 泄漏。

### 7.3 Prompt 组装

新文件 `memory_update_prompt.go`。

模板存储:Go 不能用 deer-flow 那种 Python `.format()`,因为模板里有大量
`{{` `}}` JSON 示例,会跟 Python 占位符规则冲突——deer-flow 实际是用 `{}`
单括号占位 + JSON 里的 `{{` `}}` 转义。我们这边换成不会跟 JSON 撞的标记:

```go
// memory_update_prompt.go
const memoryUpdatePromptTemplate = `You are a memory management system. ...
Current Memory State:
<current_memory>
__CURRENT_MEMORY__
</current_memory>

New Conversation to Process:
<conversation>
__CONVERSATION__
</conversation>

Instructions:
... (照抄 deer-flow MEMORY_UPDATE_PROMPT 全文, 把 {current_memory} /
     {conversation} / {correction_hint} 三处占位符换成
     __CURRENT_MEMORY__ / __CONVERSATION__ / __CORRECTION_HINT__,
     原模板里的 {{ }} 转义还原成单括号 { } )
...

__CORRECTION_HINT__

Memory Section Guidelines:
...

Return ONLY valid JSON, no explanation or markdown.`

func buildUpdatePrompt(current memorystore.MemoryData, conversation string) (string, error) {
    payload, err := json.MarshalIndent(current, "", "  ")
    if err != nil { return "", err }

    correction, reinforcement := detectSignals(/* messages */)  // 本期返回 false, false
    hint := buildCorrectionHint(correction, reinforcement)

    out := memoryUpdatePromptTemplate
    out = strings.Replace(out, "__CURRENT_MEMORY__", string(payload), 1)
    out = strings.Replace(out, "__CONVERSATION__", conversation, 1)
    out = strings.Replace(out, "__CORRECTION_HINT__", hint, 1)
    return out, nil
}

// detectSignals 占位实现; 后续可基于 messages 里关键词 grep 实现。
func detectSignals(messages []*schema.Message) (correction, reinforcement bool) {
    return false, false
}

// buildCorrectionHint 本期都返回空, 留接口给后续扩展。
func buildCorrectionHint(correction, reinforcement bool) string {
    if !correction && !reinforcement { return "" }
    // ... (照抄 deer-flow _build_correction_hint 文本)
    return ""
}

// formatConversationForUpdate 把 message 列表渲染成
// "Human: ...\nAssistant: ..." 的形式; 跳过 system / tool messages。
func formatConversationForUpdate(messages []*schema.Message) string
```

`CorrectionHint`: 本期 `detectSignals` 返回 `false,false`,所以 hint 永远
是空字符串。deer-flow 那边的 grep 实现复杂(关键词 + 否定词联检),
AGENTS.md "不写 user 没要求的功能",先不实现。但保留函数骨架,后续要做
就直接改 `detectSignals` 函数体。

`Conversation`: `formatConversationForUpdate` 简化版,不做
`<uploaded_files>` strip(项目里现在没 uploads 概念)。

### 7.4 LLM 调用 + 超时

```go
runCtx, cancel := context.WithTimeout(ctx, memoryUpdateTimeout)  // 60s
defer cancel()

resp, err := chatModel.Generate(runCtx, []*schema.Message{schema.UserMessage(prompt)})
```

为什么显式包 timeout 而不只靠 `cfg.Models.<name>.TimeoutSeconds`(HTTP 层
timeout)?
- HTTP timeout 控制单个 HTTP 请求,但有些 model provider 用 streaming /
  长连接,HTTP timeout 不一定及时触发。
- ctx-level timeout 是最外层兜底,无论 underlying transport 怎么搞,
  60s 后 ctx done,LLM SDK 必然 abort。
- `/clear` 的 5s flush ctx 也走这条路:在外层包了 5s,内层即便有 60s
  Update timeout 也会被 5s 抢先触发。

错误处理: 任何 err → log warn + return err(**不更新** `lastRunAt`,下次
debounce 不算这次)。Log 在 `GetChatModelMiddlewares` 里组装的 Extract
闭包里统一做(见 §9.2),不让 `Run` 自己 log,避免重复。

### 7.5 JSON 解析

LLM 经常会用 ` ```json ... ``` ` 把 JSON 包起来,解析前要剥壳:

```go
func parseUpdatePayload(raw string) (updatePayload, error) {
    text := strings.TrimSpace(raw)

    // 剥 ```...``` 包装 (deer-flow 一致)
    if strings.HasPrefix(text, "```") {
        lines := strings.Split(text, "\n")
        if len(lines) >= 2 {
            if lines[len(lines)-1] == "```" {
                lines = lines[1 : len(lines)-1]
            } else {
                lines = lines[1:]
            }
            text = strings.Join(lines, "\n")
        }
    }

    var p updatePayload
    err := json.Unmarshal([]byte(text), &p)
    if err != nil {
        return updatePayload{}, fmt.Errorf("unmarshal update payload: %w", err)
    }
    return p, nil
}
```

`updatePayload` 类型(只在 updater 包内部用):

```go
type updatePayload struct {
    User          map[string]sectionUpdate `json:"user"`
    History       map[string]sectionUpdate `json:"history"`
    NewFacts      []factUpdate             `json:"newFacts"`
    FactsToRemove []string                 `json:"factsToRemove"`
}

type sectionUpdate struct {
    Summary      string `json:"summary"`
    ShouldUpdate bool   `json:"shouldUpdate"`
}

type factUpdate struct {
    Content     string  `json:"content"`
    Category    string  `json:"category"`
    Confidence  float64 `json:"confidence"`
    SourceError string  `json:"sourceError,omitempty"`
}
```

解析失败: 返回 err,`Run` 那一层 wrap 后 log warn,**不**更新 `lastRunAt`
(下次再试)。

### 7.6 Merge 策略

```go
func applyUpdate(
    current memorystore.MemoryData,
    upd updatePayload,
    cfg config.Memory,
) memorystore.MemoryData {
    now := utcNowISO()
    out := current  // value copy; Section / Fact 是值类型, slice 后面 reassign

    // 6.1 user.* sections
    if s, ok := upd.User["workContext"]; ok && s.ShouldUpdate {
        out.User.WorkContext = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
    }
    if s, ok := upd.User["personalContext"]; ok && s.ShouldUpdate {
        out.User.PersonalContext = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
    }
    if s, ok := upd.User["topOfMind"]; ok && s.ShouldUpdate {
        out.User.TopOfMind = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
    }

    // 6.2 history.* sections (同形)
    if s, ok := upd.History["recentMonths"]; ok && s.ShouldUpdate {
        out.History.RecentMonths = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
    }
    if s, ok := upd.History["earlierContext"]; ok && s.ShouldUpdate {
        out.History.EarlierContext = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
    }
    if s, ok := upd.History["longTermBackground"]; ok && s.ShouldUpdate {
        out.History.LongTermBackground = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
    }

    // 6.3 facts: remove first, then append
    if len(upd.FactsToRemove) > 0 {
        toRemove := make(map[string]struct{}, len(upd.FactsToRemove))
        for _, id := range upd.FactsToRemove {
            toRemove[id] = struct{}{}
        }
        kept := out.Facts[:0]                           // reuse backing array
        for _, f := range out.Facts {
            if _, drop := toRemove[f.ID]; !drop {
                kept = append(kept, f)
            }
        }
        out.Facts = kept
    }

    for _, nf := range upd.NewFacts {
        content := strings.TrimSpace(nf.Content)
        if content == "" { continue }
        conf := memorystore.CoerceConfidence(nf.Confidence)
        if conf < cfg.FactConfidenceThreshold { continue }
        category := nf.Category
        if strings.TrimSpace(category) == "" { category = "context" }
        out.Facts = append(out.Facts, memorystore.Fact{
            ID:          memorystore.NewFactID(),
            Content:     content,
            Category:    category,
            Confidence:  conf,
            SourceError: nf.SourceError,                // 只有 category=="correction" 时 deer-flow 才会带; 我们不强校验
            CreatedAt:   now,
            Source:      "llm",
        })
    }

    // 6.4 facts cap: 按 confidence 降序保留前 N
    if cfg.MaxFacts > 0 && len(out.Facts) > cfg.MaxFacts {
        sort.SliceStable(out.Facts, func(i, j int) bool {
            return out.Facts[i].Confidence > out.Facts[j].Confidence
        })
        out.Facts = out.Facts[:cfg.MaxFacts]
    }

    out.LastUpdated = now
    return out
}
```

注意点:
- `applyUpdate` 是**纯函数**(给定输入永远输出同样结果,没有副作用),好测。
  唯一的隐式输入是 `utcNowISO()` 和 `NewFactID()`,测试时可以注入 fake 时钟
  / 计数器(本期不做,直接用真实时间戳,断言时只检查"是否非空字符串")。
- `out := current` 是 value copy,但 `out.Facts` slice 共享底层数组——所以
  6.3 那段用 `out.Facts[:0]` reuse 数组没问题,因为我们立即用 `kept` 替换。
  外部 `current.Facts` 在调用方眼里没变(我们已经拿到 `current` 的 value
  copy)。
- `cfg.FactConfidenceThreshold == 0` 时所有 newFacts 都会通过(0 是合理的
  默认值)。

写盘: `store.Save(agentName, out)`。失败:`Run` 返回 wrapped error,**不**
更新 `lastRunAt`。

### 7.7 并发模型

- `MemoryUpdater.mu` 保护 `Run` 整体串行。两轮对话连续触发 → 第二轮等第一
  轮完成。
- middleware 那边 `go hooks.Extract(...)` 起 goroutine,所以 `Run` 阻塞不
  影响主对话流。
- ctx 控制: 接收来自 middleware 的 ctx(`context.Background()` 衍生,
  生命周期跟主流程一致),Run 内部用 `WithTimeout` 包 60s 上限。两层叠加
  关系:
  - `chatModel.Generate(runCtx, ...)`: 受 60s ctx 控制,LLM 卡了会抛
    `context.DeadlineExceeded`。
  - 主进程 ctrl-C → 父 ctx cancel → runCtx 联级 cancel → LLM 调用 abort。
  - `/clear` / summarization 走 flushHook 闭包,父 ctx 已经被 5s 包过,内层
    `WithTimeout(60s)` 会被 5s 抢先触发(取较短的 deadline)。
- **不存在 goroutine 泄漏**: ctx cancel 后 `chatModel.Generate` 必须返回,
  随后 `Run` 返回,`mu.Unlock` 通过 `defer` 触发,extract goroutine 结束。

### 7.8 文件骨架: `backend/agent/memory_updater.go`

```go
package agent

const (
    memoryUpdateTimeout = 60 * time.Second
    memoryFlushTimeout  = 5 * time.Second
)

type MemoryUpdater struct {
    store     *memorystore.Store
    mu        sync.Mutex
    lastRunAt time.Time
}

type updatePayload struct  { /* 见 §7.5 */ }
type sectionUpdate struct  { /* 见 §7.5 */ }
type factUpdate    struct  { /* 见 §7.5 */ }

func NewMemoryUpdater(store *memorystore.Store) *MemoryUpdater
func (u *MemoryUpdater) Run(
    ctx context.Context,
    chatModel model.BaseChatModel,
    cfg config.Memory,
    agentName string,
    messages []*schema.Message,
    force bool,
) error

// 包内 helpers
func parseUpdatePayload(raw string) (updatePayload, error)
func applyUpdate(current memorystore.MemoryData, upd updatePayload, cfg config.Memory) memorystore.MemoryData
```

### 7.9 文件骨架: `backend/agent/memory_update_prompt.go`

```go
package agent

const memoryUpdatePromptTemplate = `...` // 见 §7.3, 含 __CURRENT_MEMORY__ / __CONVERSATION__ / __CORRECTION_HINT__ 三占位符

// 包内 helpers (供 Run 调)
func buildUpdatePrompt(current memorystore.MemoryData, conversation string) (string, error)
func formatConversationForUpdate(messages []*schema.Message) string
func detectSignals(messages []*schema.Message) (correction, reinforcement bool)   // 本期返回 false, false
func buildCorrectionHint(correction, reinforcement bool) string                   // 本期返回 ""
```

`formatConversationForUpdate` 实现细节(对齐 deer-flow,简化掉 multimodal
和 uploads strip):

```go
const messageContentMaxLen = 1000  // 单条消息渲染上限, 超过截断 + "..."

func formatConversationForUpdate(messages []*schema.Message) string {
    lines := make([]string, 0, len(messages))
    for _, msg := range messages {
        if msg == nil { continue }
        content := strings.TrimSpace(msg.Content)
        if content == "" { continue }
        if len(content) > messageContentMaxLen {
            content = content[:messageContentMaxLen] + "..."
        }
        switch msg.Role {
        case schema.User:
            lines = append(lines, "User: "+content)
        case schema.Assistant:
            lines = append(lines, "Assistant: "+content)
        default:
            // 跳过 system / tool messages
        }
    }
    return strings.Join(lines, "\n\n")
}
```

注意:
- 跟 deer-flow 一致用 `\n\n` 双换行分隔(不是单换行),让 LLM 更容易看清
  turn 边界。
- 单条消息超过 1000 字符截断 —— 防止把整篇代码 / 文件内容扔进 update
  prompt,既贵又稀释 signal。
- system / tool messages 跳过——deer-flow 也是只看 user / assistant。

---

## 8. Config 复用

`backend/config/yaml.go` 的 `Memory` 不动。现有字段对应关系:

| 字段 | 用途 | 本期处理 |
|---|---|---|
| `Enabled` | 总开关 | 用 |
| `InjectionEnabled` | 读侧开关 | 用(`getMemoryPrompt` 已经在用) |
| `MaxInjectionTokens` | 渲染预算 | 用 |
| `DebounceSeconds` | updater 防抖 | 用 |
| `MaxFacts` | facts 上限 | 用 |
| `FactConfidenceThreshold` | 写入时 confidence 过滤 | 用 |
| `ModelName` | updater 专用模型 | **不读**,留字段 + 注释"reserved" |
| `StoragePath` | 自定义存储路径 | **不读**,Go 这边硬走 `{root}/.eino-cli/memory/`,留字段 + 注释"reserved" |

不需要改 `config.Memory` 类型。

---

## 9. 调用面 ripple(完整改动清单)

按方案 ②: lead_agent.go 不持有 store/updater,memory wiring 完全收敛在
`GetSystemPrompt`(读侧)和 `GetChatModelMiddlewares`(写侧)内部。

| 文件 | 操作 | 说明 |
|---|---|---|
| `backend/memory/store/store.go` | 重写 | 新 `Load` / `Save` + 新增 `NewStoreFromConfig`(详见 §4.4) |
| `backend/memory/store/data.go` | 新建 | `MemoryData` / `UserContext` / `History` / `Fact` + 辅助函数(§3.1) |
| `backend/memory/store/agent_name.go` | 新建 | `validateAgentName` 校验正则(§4.3) |
| `backend/memory/store/store_test.go` | 重写 | Load/Save/原子写/不存在文件/坏 JSON/agentName 校验 |
| `backend/agent/memory.go` | **整体替换** | 类型 `MemoryAccessor` 全删;改成两个顶层函数 `GetMemoryPromptBlock` / `InjectMemory`(§6.5) |
| `backend/agent/memory_render.go` | 新建 | `formatMemoryForInjection` 等渲染 helpers(§5.1) |
| `backend/agent/memory_update_prompt.go` | 新建 | prompt 模板 const + 4 个 helper(§7.9) |
| `backend/agent/memory_updater.go` | 新建 | `MemoryUpdater` 类型 + `Run` + `parseUpdatePayload` + `applyUpdate`(§7.8) |
| `backend/agent/memory_test.go` | 重写 | 测试目标改为顶层函数 + `MemoryUpdater` |
| `backend/agent/lead_agent.go` | 改 ~2 行 | 拆掉 `mem := NewMemoryAccessor(...)`,改用新 wiring(§9.1)|
| `backend/agent/prompt.go` | 改签名 | `GetSystemPrompt(rt, cfg)`(去掉第三参数);内部自建 store |
| `backend/agent/middleware_chain.go` | 改签名 + wiring | `GetChatModelMiddlewares` 只多接 `chatModel`,store/updater 内部建(§9.2) |
| `backend/agent/middleware_chain*_test.go` | 改调用点 | 改新签名,见 §9.2 末尾 |
| `backend/agent/prompt_test.go` | 改调用点 | `GetSystemPrompt(rt, cfg)` 去掉第三参数;`cfg.RootDir = t.TempDir()` 隔离副作用 |

### 9.1 lead_agent.go 新 wiring(极简)

```go
// MakeLeadAgent body:
chatModel, err := buildChatModel(ctx, rt.ModelCfg)
if err != nil { return nil, nil, err }

backend := newLocalBackend("")
shell   := newLocalShell("")

prompt   := GetSystemPrompt(rt, cfg)
handlers := GetChatModelMiddlewares(ctx, cfg, rt, chatModel)

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
applyToolGroups(deepCfg, rt.AgentConfig, backend, shell)
// ...
```

`MakeLeadAgent` 完全不持有 `store` / `updater` / `mem` 任何 memory 引用。
跟现在(`mem := NewMemoryAccessor(...)`)对比,这一步消失。

### 9.2 middleware_chain.go 新 wiring(关键)

`GetChatModelMiddlewares` 签名只多接一个 `chatModel`。`store` 和 `updater`
都在函数内部按需建:

```go
func GetChatModelMiddlewares(
    ctx context.Context,
    cfg *config.Config,
    rt RuntimeContext,
    chatModel model.BaseChatModel,
) []adk.ChatModelAgentMiddleware {
    middlewareList := []adk.ChatModelAgentMiddleware{
        middlewares.NewAgentState(),
        middlewares.NewTitle(),
        middlewares.NewToolErrorHandling(),
        middlewares.NewLoopDetection(),
    }

    // memory wiring: store + updater 都在这里建; updater 必须在外层声明,
    // 才能被 memory hooks 和 summarization flushHook 共享同一实例。
    var (
        store   *memorystore.Store
        updater *MemoryUpdater
    )
    if cfg.Memory.Enabled {
        store   = memorystore.NewStoreFromConfig(cfg)
        updater = NewMemoryUpdater(store)

        hooks := middlewares.MemoryHooks{
            Inject: func(ctx context.Context, msgs []*schema.Message) []*schema.Message {
                return InjectMemory(store, cfg.Memory, rt.AgentName, msgs)
            },
            Extract: func(ctx context.Context, msgs []*schema.Message) {
                err := updater.Run(ctx, chatModel, cfg.Memory, rt.AgentName, msgs, false /*force*/)
                if err != nil {
                    slog.Warn("memory update failed", "agent", rt.AgentName, "err", err)
                }
            },
        }
        middlewareList = append(middlewareList, middlewares.NewMemory(hooks))
    }

    // ... (TokenUsage / ToolSearch / SubagentLimit / HITL 不变)

    if cfg.Summarization.Enabled {
        summaryModel := buildSummaryChatModel(ctx, cfg)

        // flushHook 闭包跟 memory hooks 共用同一个 updater 实例 (上面 var 声明的);
        // memory disabled 时 updater == nil, 这里 short-circuit return nil。
        flushHook := func(ctx context.Context, before, after adk.ChatModelAgentState) error {
            if updater == nil { return nil }
            flushCtx, cancel := context.WithTimeout(ctx, memoryFlushTimeout)  // 5s
            defer cancel()
            return updater.Run(flushCtx, chatModel, cfg.Memory, rt.AgentName, before.Messages, true /*force*/)
        }

        summaryMW, err := middlewares.NewSummarization(
            ctx,
            cfg.Summarization.Enabled,
            0, 0,
            cfg.Summarization.SummaryPrompt,
            summaryModel,
            flushHook,
        )
        // ... (不变)
    }

    // ... (Trace / Clarification 不变)
    return middlewareList
}
```

要点:
- `var updater *MemoryUpdater` **必须**在 memory branch 之外声明,否则
  `cfg.Summarization.Enabled` 那个 branch 看不见它。两个 hook 必须共用
  同一个 updater 实例,否则 `lastRunAt` 不共享、debounce 失效。
- `cfg.Memory.Enabled == false` 时 `updater = nil`,flushHook 内部 short
  circuit。
- `slog.Warn` 在 Extract 闭包里 log,`Run` 自己只 return error;避免双层
  log 同一件事。

⚠️ 测试影响:

```go
// middleware_chain_test.go / middleware_chain_phase3_test.go: 旧
GetChatModelMiddlewares(ctx, cfg, NewMemoryAccessor(nil), rt)
// 新 (cfg.Memory.Enabled = false 不会触发 store 派生; chatModel 测试场景传 nil)
GetChatModelMiddlewares(ctx, cfg, rt, nil /*chatModel*/)
```

```go
// prompt_test.go: 旧
out := GetSystemPrompt(RuntimeContext{AgentName: "default"}, cfg, nil)
// 新 (内部自建 store, 测试要把 cfg.RootDir 设到 t.TempDir() 隔离副作用)
cfg.RootDir = t.TempDir()
out := GetSystemPrompt(RuntimeContext{AgentName: "default"}, cfg)
```

`cfg.Memory.Enabled = false` 的测试不需要管 cfg.RootDir(getMemoryPrompt
早期 short-circuit,store 虽然被建但 Load 不会被调到)。但 Memory.Enabled
= true 的 prompt_test 必须设 `cfg.RootDir = t.TempDir()`,否则 store 会落
到当前工作目录,污染 / 失败。

---

## 10. 测试策略

新加单测(覆盖核心路径):

**store 层**(`store_test.go`):
- `Load` 不存在文件 → empty
- `Load` 坏 JSON → empty + 不 error
- `Save` + `Load` round-trip
- `Save` 原子性: 用 `os.Stat` 验中间不出现 `.tmp` 残留
- `agentName == ""` 落到 `global.json`
- `agentName == "foo"` 落到 `agents/foo.json`
- 非法 agentName(`../etc/passwd` / `foo/bar` / 空)→ Save error
- `coerceConfidence`: NaN/Inf/负数/2.0 → 0/0/0/1

**renderer**(`memory_render_test.go`):
- 全空 MemoryData → ""
- 只有 `user.workContext.summary` → `User Context:\n- Work: ...`
- facts 按 confidence 降序
- correction + sourceError 走特殊行
- 超 `maxTokens` → 二次截断 `\n...` 收尾
- `maxTokens == 0` → 不截断

**updater**(`memory_updater_test.go`):
- debounce: `lastRunAt` 在窗口内 → skip(直接设字段)
- `force=true` 绕过 debounce
- LLM 返回 ` ```json\n{...}\n``` ` → 正确 strip
- LLM 返回坏 JSON → return err,store 没被改,`lastRunAt` 不更新
- `shouldUpdate=true` → section 被覆盖,`UpdatedAt` 是 now
- `shouldUpdate=false` → 不变
- `newFacts` confidence 低于阈值 → 丢
- `factsToRemove` 按 ID 删
- `MaxFacts` cap 后保留 confidence 高的

**fake LLM**: 实现 `model.BaseChatModel` 的小 mock,直接返回预设字符串。
不打真实网络。

**测试隔离要点**(因为 store 在 `GetSystemPrompt` / `GetChatModelMiddlewares`
内部按 cfg 派生):
- 任何 `cfg.Memory.Enabled = true` 且会触发到 store 的 test,都必须先
  `cfg.RootDir = t.TempDir()`,否则会污染 cwd 或 home。
- `cfg.Memory.Enabled = false` 的 test 不受影响——early short-circuit
  路径不会调到 store。

---

## 11. 推进顺序(commit plan: 两步切换)

最终采用两步切换。每步内部 store + reader + wiring 一起改,中间不允许
"编译失败"窗口期。

### Commit 1: store + reader 切换(读路径完整对齐 deer-flow)

新增:
- `backend/memory/store/data.go`(§3.1)
- `backend/memory/store/agent_name.go`(§4.3)
- `backend/agent/memory_render.go`(§5.1)
- `backend/agent/memory_updater.go`: **stub 版**——只放 `MemoryUpdater`
  类型、`NewMemoryUpdater` 构造器、空 `Run` 方法(直接 return nil)。
  这样 §9.2 的闭包能引用到稳定签名,commit 2 才往 `Run` 里填内容。

重写:
- `backend/memory/store/store.go`: 删 `LoadAll` / `Save(Memory)` / `Find` /
  `Memory{}` / `TaskMemoryPrefix`,新增 `Load(agentName)` /
  `Save(agentName, data)` + `NewStoreFromConfig(cfg)`(§4.4)
- `backend/memory/store/store_test.go`
- `backend/agent/memory.go`: 删 `MemoryAccessor` 类型 + 全部相关 method,
  替换为顶层 `GetMemoryPromptBlock` / `InjectMemory`(§6.5)
- `backend/agent/memory_test.go`: 测试目标改为顶层函数,seed `MemoryData{}`

改调用点(必须在同一 commit 跟随,否则编译挂):
- `backend/agent/lead_agent.go`: 删 `mem := NewMemoryAccessor(...)` 这一
  整行;`GetSystemPrompt(rt, cfg)` 不再传 mem;`GetChatModelMiddlewares`
  调用补 `chatModel` 入参(§9.1)。
- `backend/agent/prompt.go`: `GetSystemPrompt(rt, cfg)` 去掉第三参数,
  内部自建 `store := memorystore.NewStoreFromConfig(cfg)`;`getMemoryPrompt`
  签名改成接 `*memorystore.Store`(§6.2)。
- `backend/agent/middleware_chain.go`: 签名改成 `(ctx, cfg, rt, chatModel)`,
  内部按 §9.2 组装两个闭包。stub 版 `MemoryUpdater.Run` 总返回 nil,所以
  commit 1 里 Extract 闭包 log 不会触发(因为 err 永远 nil)。
- `backend/agent/middleware_chain_test.go`、`middleware_chain_phase3_test.go`:
  `GetChatModelMiddlewares(ctx, cfg, rt, nil /*chatModel*/)`。
- `backend/agent/prompt_test.go`: `GetSystemPrompt(rt, cfg)` 去掉第三参数;
  Memory.Enabled=true 的 test 加 `cfg.RootDir = t.TempDir()`(§9.2 末尾)。

验收:
- `go build ./...` 通过
- `go test ./...` 通过

### Commit 2: updater 接入(写路径完整对齐 deer-flow)

新增:
- `backend/agent/memory_update_prompt.go`: 模板 const + 4 个 helpers(§7.9)
- `backend/agent/memory_updater_test.go`: 单测 `Run` / `parseUpdatePayload` /
  `applyUpdate` / debounce 行为(§10)

改:
- `backend/agent/memory_updater.go`: 把 commit 1 里的 stub `Run` 替换成
  完整实现(§7.2 + §7.5 + §7.6)。类型 + `NewMemoryUpdater` 构造器不变。
- 不需要碰 `lead_agent.go` / `middleware_chain.go` / `prompt.go`(commit 1
  已经把所有调用面布好,只填 `Run` 函数体)。

验收:
- `go test ./...` 通过(updater 单测用 fake `model.BaseChatModel`,不打
  真实网络)
- 集成验证: 本地跑 `eino-cli`,跑几轮对话,检查
  `{cfg.RootDir}/.eino-cli/memory/agents/<rt.AgentName>.json` 是否生成
  内容。

每个 commit 都做到: `go build ./...` 通过 + `go test ./...` 通过。

---

## 12. 风险

- **LLM 输出 JSON 不合规**: warn log,这一轮 update 丢弃,下一轮再试。
  `parseUpdatePayload` 已覆盖剥壳 + unmarshal 错误。
- **LLM 输出超长占用主模型 quota**: 每轮一次 + `DebounceSeconds` 已经控住
  成本。极端情况要再加个 token 上限,目前先不加(AGENTS.md "不写 user
  没要求的功能")。
- **`/clear` 时强制 flush 阻塞 UI**: flushHook 闭包用
  `memoryFlushTimeout = 5 * time.Second` 短 ctx,超时即放弃这次 update。
  下次正常对话恢复后会在 debounce 窗口外补上。
- **goroutine 泄漏**: `Run` 内部所有阻塞调用都接受 ctx,ctx cancel 时
  `chatModel.Generate` 必然返回。`mu.Unlock` 由 `defer` 保证。无泄漏。
- **agentName 跨进程切换**: 用户切换 default_agent 后,新进程读写
  `agents/<new>.json`;老进程的内存不会污染新文件(进程边界天然隔离)。
- **测试场景下 chatModel/store/updater = nil**: `middleware_chain.go` 闭包
  里 `if updater == nil { return }` / `Run` 早期 `if chatModel == nil { return nil }` /
  `GetMemoryPromptBlock` 早期 `if store == nil { return "" }` 三层防御已
  覆盖,nil 入参不会 panic。

---

设计已锁定,可按 §11 两步切换开始写代码。
