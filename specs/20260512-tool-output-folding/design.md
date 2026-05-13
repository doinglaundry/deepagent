# Tool 输出折叠 — 调研 & 技术方案

> 日期: 2026-05-12
> 关联截图(用户给的目标 UX, Claude Code TUI):
>
> ```
> ⏺ Bash(git log --oneline -30)
>   ⎿  7dec1a6 agent: replace working_directory section with concrete root pa
>      th
>      876e155 agent: wrap chat model with retry, circuit breaker, and fallba
>      … +23 lines (ctrl+o to expand)
> ```

---

## 0. 决策 / 待确认

| 项 | 倾向决策 | 备注 |
|---|---|---|
| 落地形态 | **从 `/debug` 模式独立出来,变成默认主流程的一部分** | 工具调用 / 工具结果以专门 block 渲染到 scrollback,跟 user echo / assistant 文本并列;`/debug` 继续保留(走旧的扁平 dump 路径)用于深度排错 |
| 数据来源 | **Trace.Before 事件里抽 tool_calls + Tool 消息** | 不新增 TracePhase;`Before` 已经携带完整 messages,够用 |
| 触发时机 | **下一轮 model call 开始时**(post-tool-execution,pre-model) | 简单;代价是当 turn 内多次工具调用时,中间状态不可见(只在每轮 LLM 调用前批量出现) |
| 折叠阈值 | **5 行**(content lines,不含 header) | 与 Claude Code 视觉接近;阈值可改但默认 5 |
| 折叠展示 | **header `⏺ ToolName(args)` + 5 行预览 + `… +N lines (ctrl+o to expand)` footer** | 字符 ⏺ 和 ⎿ 直接照搬 Claude Code(Unicode 实心圆 + ⎿ U+23BF) |
| Ctrl+O 行为 | **toggle 最近一个尚可折叠的 block**(最近优先,无差别 cycle) | 兼容性最简;不需要 cursor / focus 概念 |
| 状态存储 | **`Model.toolBlocks []*toolBlock`** + scrollback 用占位字符串引用 | block 自带 id / collapsed / lines;render 时按 id 查表生成最终文本 |
| Args 渲染 | **字段感知**:`write_file` / `Edit` 类工具只显示 `path` + content 字节数;其他工具 args 整体 JSON dump 后截 60 chars | 长 `content` 不撑爆 header;§4.4 给一个小规则表 |
| 工具调用空输出 | **不显示 block** | `tool_call` 没对应 Tool 消息(被取消 / 还没执行)就跳过;避免空骨架污染 scrollback |
| Tool 失败的视觉 | **不区分**,沿用同样 header / body 样式 | 失败信息会出现在 lines 里,用户自己读;不引入红色让 header 变嘈杂 |
| `/debug on` 时 | **两套并存**:既有 debug-input/debug-output dump 继续画,折叠 block 也照画 | debug 是开发者视角,折叠是用户视角,职责不同 |
| 运行时开关 | **只有 config**(`tool_blocks.enabled: false`),不加 slash 命令 | 配置静态化;真要临时关掉重启 CLI 一次,YAGNI |
| Ctrl+O 无 collapsed block 时 | **footer 状态行短暂闪一句 "nothing to expand"** | 用户立刻知道按键被识别但无事可做,而不是怀疑卡键 |
| 渲染时机 | **post-tool**(下一轮 Trace.Before 时批量出现) | 复用现成事件,不动 eino 工具执行层;延迟 < 1s 在 CLI 体验里可接受 |
| 配置入口 | **`tool_blocks:` yaml 段**(`enabled: true`, `preview_lines: 5`, `args_max_chars: 60`) | 与 `error_handling` / `memory` 同级 |

---

## 1. 现状 & 痛点

### 现状

`backend/agent/middlewares/trace.go` 的 `Trace` middleware:
- `BeforeModelRewriteState`: emit `TracePhaseBefore`, `Messages` 字段携带**全部历史消息**(含 tool_calls / Tool 角色 / Assistant)
- `AfterModelRewriteState`: emit `TracePhaseAfter`, `Messages` 只装最后一条 assistant 消息

`backend/cli/tui/update.go` L299-317 `handleTraceEvent`:
```go
case middlewares.TracePhaseBefore:
    if m.debug {
        m.pushMessage("debug-input", formatDebugInput(ev))
    }
case middlewares.TracePhaseAfter:
    if m.debug {
        m.pushMessage("debug-output", formatDebugOutput(ev))
    }
```
**只有 `/debug on` 才看得见**。默认主流程下,用户只看到:
- 自己输入的 prompt(灰块 echo)
- 流式过来的 assistant chunks
- 最终 assistant 文本进 scrollback

工具调用对用户**完全不可见**。

### 痛点

1. **用户不知道 agent 在干什么**:模型说"我帮你查一下"然后 5 秒没动静,其实在跑 `bash -lc "find . -name ..."`,但 TUI 上没任何指示
2. **工具结果完全淹没**:模型拿到长 git log 后倾向"全部转述",上一轮我们已经分析过(`20260512` 上下文里的可读性问题)
3. **Debug 模式开关粗暴**:要么完全看不见,要么看到一堆原始 byte 长度 + 截断 dump,没有适合普通用户的中间档

### 不解决会怎样

模型继续往用户对话里塞 200 行 git log 文本(因为它没别的方式让用户看到工具结果)。即使 prompt 里写"summarize don't enumerate",一个长 tool 结果还是会拐弯抹角地泄漏到回复里。从源头(工具结果直接以可折叠 block 渲染)解,LLM 就有动机只在文本里写概括语 + 让 block 自己负责"原始数据可查"。

---

## 2. 目标 UX

### 视觉示例(目标态)

```
> show me recent commits

⏺ Bash(git log --oneline -30)
  ⎿  7dec1a6 agent: replace working_directory section with concrete root path
     876e155 agent: wrap chat model with retry, circuit breaker, and fallback
     42d529b config: add error_handling section types
     7288e05 agent: insert patchtoolcalls middleware to repair dangling tool calls
     2f0acf1 tui: wire popup navigation and accept
     … +25 lines (ctrl+o to expand)

Recent activity: 30 commits over ~32h, mostly TUI work (~14) plus the
error handling wrapper and memory refactor. Most recent: 7dec1a6 (the
prompt cleanup we just discussed).
```

按 Ctrl+O 后,**最近一个折叠的 block 就地展开**到全文:

```
⏺ Bash(git log --oneline -30)
  ⎿  7dec1a6 agent: replace working_directory section with concrete root path
     876e155 agent: wrap chat model with retry, circuit breaker, and fallback
     42d529b config: add error_handling section types
     ...(全部 30 行)...
     2a1cdd7 memory: switch reader to rich JSON store; stub updater
```

再按一次 Ctrl+O,**回到折叠态**(可逆 toggle)。

### 行为规则

| 场景 | 渲染 |
|---|---|
| 工具调用,输出 ≤ `preview_lines` 行 | header + 全部输出,无 footer,**不计为可折叠** |
| 工具调用,输出 > `preview_lines` 行 | header + 前 N 行 + `… +M lines (ctrl+o to expand)` footer,**默认折叠** |
| 工具调用,无输出(被取消 / 失败) | 不渲染 block(避免空骨架) |
| `/debug on` | 折叠 block + 旧的 debug-input/debug-output 全部展示 |
| Ctrl+O 但没有任何可折叠 block | 静默(footer 状态行可以加一条暗提示,可选) |

---

## 3. 数据流

```
                 ┌──────────────────────────────────┐
   Tool call ───>│ adk + tool runner(eino 内部)    │
   Tool result   └──────────────────────────────────┘
                                │
                                ▼
              ┌──────────────────────────────────────┐
              │ Trace.BeforeModelRewriteState        │
              │   Phase: Before                      │
              │   Messages: 完整历史 + 最新工具结果  │
              └──────────────────────────────────────┘
                                │
                                ▼
              ┌──────────────────────────────────────┐
              │ TUI handleTraceEvent(Before)         │
              │   extractNewToolBlocks(prev, curr)   │
              │   → []toolBlock                      │
              └──────────────────────────────────────┘
                                │
                                ▼
              ┌──────────────────────────────────────┐
              │ pushToolBlock → m.toolBlocks 追加    │
              │ pushMessage 占位字符串 [tool:#id]    │
              └──────────────────────────────────────┘
                                │
                                ▼
                renderViewport: 遇到 [tool:#id]
                替换为该 block 的当前态文本
```

### 关键设计:增量提取

`Before` 事件携带的是**全部历史**,我们关心的是"上一次 Before → 这次 Before 之间新增的 tool_call + Tool 消息"。

简单做法:
- 每轮 `Before` 记录 `len(state.Messages)` 到 `m.lastSeenMsgCount`
- 下一次 `Before` 时,处理 `messages[lastSeenMsgCount:]`,扫出新增的 Tool 消息和对应 tool_call

边界:
- `/clear` 重置 `lastSeenMsgCount = 0`
- 同一 turn 内多个工具调用 → 都在一次 `Before` 里冒出来(因为 model loop 已经 round-trip 过工具),按出现顺序逐个 push

---

## 4. 实现方案

### 4.1 文件清单

| 文件 | 角色 |
|---|---|
| `backend/cli/tui/tool_block.go` | 新建:`toolBlock` 结构 + 提取 + 渲染顶层函数 |
| `backend/cli/tui/tool_block_test.go` | 新建:提取 / 折叠 / Ctrl+O toggle 测试 |
| `backend/cli/tui/model.go` | 加 `toolBlocks []*toolBlock` / `lastSeenMsgCount` 字段;view 渲染时遇到 `[tool:#id]` 占位符替换 |
| `backend/cli/tui/update.go` | `handleTraceEvent` 的 `TracePhaseBefore` 分支:除原有 `/debug` 渲染外,调用 `extractNewToolBlocks` 并 push 占位行 |
| `backend/cli/tui/update.go` | `handleKey` 加 `ctrl+o` 分支:toggle 最近一个 collapsed block |
| `backend/cli/tui/styles.go` | 加 `toolHeaderStyle` / `toolBodyStyle` / `toolFooterStyle` |
| `backend/config/yaml.go` + `types.go` | 加 `ToolBlocks` 段 |

### 4.2 数据结构

```go
// toolBlock represents one rendered tool invocation in the scrollback.
// One block per (tool_call, tool_result) pair; pairs without a result
// are skipped during extraction (see §3).
type toolBlock struct {
    id        int          // monotonic, matches the placeholder [tool:#id]
    name      string       // function.name from the tool_call
    argsLine  string       // truncated args, ready to drop into the header
    lines     []string     // result content split by '\n', no trailing empty
    collapsed bool         // true → render preview + footer; false → render all
}
```

字段都"绑在一起读才有意义",符合 AGENTS.md "结构体只装必须一起出现的状态"。

### 4.3 Model 字段

```go
// in Model struct
toolBlocks         []*toolBlock
lastSeenMsgCount   int    // length of state.Messages from the prior Before event
toolBlockSeq       int    // monotonic id source for placeholder lookups
toolPreviewLines   int    // from config; default 5
toolArgsMaxChars   int    // from config; default 60
```

### 4.4 提取(顶层函数,无 receiver)

```go
// extractNewToolBlocks scans messages[prevCount:] for tool_call / Tool
// message pairs and returns one toolBlock per matched pair. tool_calls
// without a matching subsequent Tool message (cancelled, in-flight) are
// dropped — the placeholder would just dangle.
func extractNewToolBlocks(messages []*schema.Message, prevCount int, idSeq *int, argsMax int) []*toolBlock {
    if prevCount >= len(messages) {
        return nil
    }
    callsByID := map[string]schema.ToolCall{}
    for _, msg := range messages[prevCount:] {
        if msg.Role == schema.Assistant {
            for _, c := range msg.ToolCalls {
                callsByID[c.ID] = c
            }
        }
    }
    var out []*toolBlock
    for _, msg := range messages[prevCount:] {
        if msg.Role != schema.Tool || msg.ToolCallID == "" {
            continue
        }
        call, ok := callsByID[msg.ToolCallID]
        if !ok {
            continue
        }
        *idSeq++
        out = append(out, &toolBlock{
            id:        *idSeq,
            name:      call.Function.Name,
            argsLine:  formatArgsLine(call.Function.Name, call.Function.Arguments, argsMax),
            lines:     splitLines(msg.Content),
            collapsed: true, // 默认折叠;render 时再决定是否真的有 footer
        })
    }
    return out
}
```

注意:tool_calls 可能跨越多条 Assistant 消息 + Tool 消息可能不与对应 Assistant 紧邻(eino 工具循环允许并发);所以分两次扫——先 index 所有 calls,再为每条 Tool 消息查 call。

### 4.4.1 字段感知 args 渲染

```go
// formatArgsLine renders the header's "(...)" part. Most tools dump JSON
// args as-is and truncate; file-writing tools collapse the verbose `content`
// field to just a byte count so headers stay scannable.
func formatArgsLine(name, rawArgs string, max int) string {
    switch name {
    case "write_file", "Write", "edit", "Edit", "str_replace", "StrReplace":
        if path, bodyLen, ok := extractFileWriteArgs(rawArgs); ok {
            return fmt.Sprintf("%s, %d bytes", path, bodyLen)
        }
    }
    return truncateUTF8(rawArgs, max)
}

// extractFileWriteArgs returns (path, len(content), true) for file-writing
// tool args; (_, _, false) when the args JSON doesn't have the expected
// shape — fall back to generic truncation.
func extractFileWriteArgs(rawArgs string) (path string, bodyLen int, ok bool) {
    var v struct {
        Path        string `json:"path"`
        FilePath    string `json:"file_path"`
        Content     string `json:"content"`
        NewString   string `json:"new_string"`
    }
    if err := json.Unmarshal([]byte(rawArgs), &v); err != nil {
        return "", 0, false
    }
    p := v.Path
    if p == "" {
        p = v.FilePath
    }
    body := v.Content
    if body == "" {
        body = v.NewString
    }
    if p == "" {
        return "", 0, false
    }
    return p, len(body), true
}
```

设计原则:tool name 白名单 + 字段尝试解析 + 解析失败回退到通用截断。新增工具时往 switch 加一条就行;不识别也不会出错,只是 header 略长。

### 4.5 渲染(顶层函数)

```go
// renderToolBlock returns the multi-line string for one tool block,
// honouring collapsed state and the preview-lines threshold.
func renderToolBlock(b *toolBlock, previewLines int) string {
    var sb strings.Builder
    fmt.Fprintf(&sb, "%s %s(%s)\n", toolHeaderBullet, b.name, b.argsLine)

    if !b.collapsed || len(b.lines) <= previewLines {
        // expanded OR short enough that there's nothing to fold
        for i, line := range b.lines {
            sb.WriteString(toolBodyPrefix(i == 0))
            sb.WriteString(line)
            sb.WriteByte('\n')
        }
        return strings.TrimRight(sb.String(), "\n")
    }

    // collapsed + over threshold → preview + footer
    for i, line := range b.lines[:previewLines] {
        sb.WriteString(toolBodyPrefix(i == 0))
        sb.WriteString(line)
        sb.WriteByte('\n')
    }
    fmt.Fprintf(&sb, "     … +%d lines (ctrl+o to expand)", len(b.lines)-previewLines)
    return sb.String()
}

const toolHeaderBullet = "⏺"

// toolBodyPrefix returns "  ⎿  " for the first body line, "     " for the rest.
// The ⎿ bracket is only drawn once per block to mimic Claude Code's tree look.
func toolBodyPrefix(first bool) string {
    if first {
        return "  ⎿  "
    }
    return "     "
}
```

### 4.6 Trace.Before 接入(`update.go`)

```go
case middlewares.TracePhaseBefore:
    if m.debug {
        m.pushMessage("debug-input", formatDebugInput(ev))
    }
    if blocks := extractNewToolBlocks(ev.Messages, m.lastSeenMsgCount, &m.toolBlockSeq, m.toolArgsMaxChars); len(blocks) > 0 {
        for _, b := range blocks {
            m.toolBlocks = append(m.toolBlocks, b)
            m.pushMessage("tool-block", fmt.Sprintf("[tool:#%d]", b.id))
        }
    }
    m.lastSeenMsgCount = len(ev.Messages)
```

### 4.7 view 渲染时占位符替换

`view.go` 现有 `renderMessage` 按 role 分发渲染。加新 case:

```go
case "tool-block":
    // body 是 "[tool:#NN]"; 取 id, 查 m.toolBlocks
    id, ok := parseToolPlaceholder(msg.Body)
    if !ok {
        return msg.Body // 兜底, 不应该发生
    }
    block := m.findToolBlockByID(id)
    if block == nil {
        return ""
    }
    return renderToolBlock(block, m.toolPreviewLines)
```

> **为什么不直接把 `renderToolBlock` 结果存进 scrollback?**
> 折叠状态可变 — 用户按 Ctrl+O 后同一个 block 要重渲染。占位符 + 实时查表保证一处 toggle、整条 scrollback 自然更新。

### 4.8 Ctrl+O 处理(`update.go` 的 `handleKey`)

```go
case key.Matches(msg, keys.ToggleToolBlock):
    if b := m.latestCollapsibleToolBlock(); b != nil {
        b.collapsed = !b.collapsed
        m.recomputeLayout()
        return m, nil
    }
    // 无可 toggle 的 block:在 footer 状态行闪一句, 3 秒后自动清掉。
    // 既确认按键被识别,也不污染 scrollback。
    m.footerHint = "nothing to expand"
    return m, hintExpireAfter(3 * time.Second)

// latestCollapsibleToolBlock returns the most recent block whose
// line count exceeds the preview threshold; nil when there's nothing
// to toggle. We scan reverse-chronologically and return the first hit.
func (m *Model) latestCollapsibleToolBlock() *toolBlock {
    for i := len(m.toolBlocks) - 1; i >= 0; i-- {
        if len(m.toolBlocks[i].lines) > m.toolPreviewLines {
            return m.toolBlocks[i]
        }
    }
    return nil
}
```

`keys.ToggleToolBlock` 在 keymap 注册为 `ctrl+o`。注意:Bubbletea/Bubbles 的 `key` package 用 `key.NewBinding(key.WithKeys("ctrl+o"))` 注册。

### 4.9 `/clear` 联动

```go
case "clear":
    ...
    m.toolBlocks = nil
    m.lastSeenMsgCount = 0
    m.toolBlockSeq = 0
```

不然清屏后旧 block 还存在,占位符 `[tool:#5]` 查不到(虽然 scrollback 也清了,但保持状态一致更稳)。

---

## 5. 配置

### 5.1 yaml 段

```yaml
tool_blocks:
  enabled: true
  preview_lines: 5   # 折叠时显示前 N 行;输出 ≤ N 行不折叠
  args_max_chars: 60 # header 里 args 最大字符数;超出截 "…"
```

### 5.2 Go types (`backend/config/yaml.go`)

```go
type ToolBlocks struct {
    Enabled       bool `yaml:"enabled"`
    PreviewLines  int  `yaml:"preview_lines"`
    ArgsMaxChars  int  `yaml:"args_max_chars"`
}
```

Config 加字段:`ToolBlocks ToolBlocks \`yaml:"tool_blocks"\``。

### 5.3 默认值

`Enabled=false`(YAML 零值)→ 在 TUI Model 初始化时,如果 cfg 段是零,启用 hard-coded 默认 `enabled=true, preview_lines=5, args_max_chars=60`。

> **为什么不在 Load() 里 fix-up?**
> 与现有 `error_handling` 一致:yaml/config.yaml 是 skip-worktree(每个开发者本地),Go 端不强加默认,TUI 拉起时按需 fallback。

---

## 6. 测试计划

`backend/cli/tui/tool_block_test.go`:

**提取**

| 用例 | 验证点 |
|---|---|
| `TestExtractToolBlocks_SingleCall` | 1 个 tool_call + 1 个 Tool 消息 → 1 block,name / argsLine / lines 都对 |
| `TestExtractToolBlocks_MultipleInOneTurn` | 一次 Before 携带 3 个 tool_call + 3 个 Tool 消息 → 3 blocks,顺序与消息顺序一致 |
| `TestExtractToolBlocks_DanglingCallDropped` | tool_call 没匹配 Tool → 不出 block |
| `TestExtractToolBlocks_ArgsTruncated` | args 长度 > argsMax → 末尾带 "…",总长度 ≤ argsMax + 1 |
| `TestExtractToolBlocks_PrevCountRespected` | prevCount=5,messages[0:5] 里的 call/result 不再出 block |
| `TestFormatArgsLine_WriteFile` | `name="write_file", args={"path":"a.md","content":"hello"}` → `"a.md, 5 bytes"` |
| `TestFormatArgsLine_Edit_NewString` | `name="Edit", args={"file_path":"a.go","new_string":"abc"}` → `"a.go, 3 bytes"` |
| `TestFormatArgsLine_UnknownToolFallback` | unknown name → 通用 truncate 路径,不抛错 |

**渲染**

| 用例 | 验证点 |
|---|---|
| `TestRenderToolBlock_Expanded` | collapsed=false → 输出全部 lines,无 footer |
| `TestRenderToolBlock_ShortNoFooter` | len(lines)=3, previewLines=5 → 无 footer,无论 collapsed 与否 |
| `TestRenderToolBlock_CollapsedFooter` | len(lines)=20, previewLines=5 → 前 5 行 + "… +15 lines (ctrl+o to expand)" |
| `TestRenderToolBlock_HeaderFormat` | header 形如 "⏺ Bash(git log --oneline -30)" |

**Toggle**

| 用例 | 验证点 |
|---|---|
| `TestLatestCollapsibleToolBlock_PicksLastLong` | 有 3 个 block,只有第 1 和第 3 超阈值 → 返回第 3 |
| `TestLatestCollapsibleToolBlock_NoneCollapsible` | 所有 block 都短 → 返回 nil |
| `TestHandleKey_CtrlOToggles` | 模拟 Ctrl+O msg → 最新 long block.collapsed 翻转 |
| `TestHandleKey_CtrlONoBlocksSetsHint` | 无 collapsible block,Ctrl+O → `m.footerHint == "nothing to expand"`,返回的 cmd 是 hintExpireAfter |
| `TestFooterHintExpires` | hintExpireAfter 触发 → `m.footerHint == ""` |

**集成**

| 用例 | 验证点 |
|---|---|
| `TestHandleTraceEvent_ExtractsAndPushesBlock` | Before event 带 1 个 tool round-trip → m.toolBlocks 长度 +1,scrollback 多一行 [tool:#1] 占位 |
| `TestClearResetsToolBlocks` | /clear → toolBlocks 清空,lastSeenMsgCount=0 |

---

## 7. 不做的事

- ❌ **Live 渲染(工具执行中实时出现)**:需要 hook eino 工具执行层,本仓里没现成 hook;post-tool 在下一轮 Before 出现已经够用,延迟一般 < 1s
- ❌ **多 block 同时 toggle / Ctrl+O 切换不同 block**:简化 UX;真要看多个就滚回去逐个按 Ctrl+O(每次 toggle 最近 collapsed block,展开后再按一次 toggle 同一个,所以没法"跳到上一个"——但实际场景里"展开最近一个看完就继续"足够)
- ❌ **Block 内 grep / 搜索**:viewport 自带滚动,展开后用终端原生选择 / 翻页
- ❌ **Tool 失败的视觉特殊化**:不区分(error trace 出现在 lines 里,用户自己读)
- ❌ **运行时 slash 命令开关**:只走 config;真要临时关掉重启即可
- ❌ **TUI 配色主题化**:沿用 styles.go 现有色;不为这次新增主题
- ❌ **流式 Tool 消息(chunk 进来即追加)**:工具返回都是一次性 string,无需流式

---

## 8. 实施步骤

按 AGENTS.md "每个 commit 一句话能说清":

1. **加 yaml schema + 默认值**(`types.go` + `yaml.go` + 本地 `yaml/config.yaml`)→ `go test ./backend/config/...` 通过
2. **写 `tool_block.go`:`toolBlock` struct + `extractNewToolBlocks` + `renderToolBlock` + 表驱动单测** → §6 提取 / 渲染用例全绿
3. **接入 Model 字段 + `handleTraceEvent` Before 分支 push 占位符 + view 占位符替换** → 集成测试通过;启动 CLI 跑一轮带工具的对话能看到 block
4. **加 Ctrl+O keybinding + `latestCollapsibleToolBlock` + toggle 单测** → §6 toggle 用例全绿
5. **`/clear` 联动重置** → `TestClearResetsToolBlocks` 通过
6. **更新 `/help` 文案,提到 `ctrl-o` 可展开** → 手工冒烟

---

## 9. 与现状的差异

| 维度 | 现状 | 本方案 |
|---|---|---|
| 工具调用可见性 | `/debug on` 才看得见,且是扁平 dump | 默认可见,结构化 block |
| 渲染时机 | 全部消息一起 dump(debug) | 每个工具 round-trip 一个 block |
| 折叠 / 展开 | 无 | 默认折叠,Ctrl+O toggle |
| 占用 scrollback | debug 模式下大块文本 | 折叠态 ≤ 7 行 / 工具调用 |
| 与 LLM 输出关系 | 模型转述工具结果(臃肿) | 模型可短回复 + 引导用户看 block |

---

## 10. 已锁定决策

1. **Header bullet**: 用 Unicode `⏺` / `⎿`,跟 Claude Code 视觉对齐。现代终端字体都能渲染;如未来发现某终端字体不支持,改 styles.go 一处即可
2. **`write_file` / `Edit` / `StrReplace` 的 args**: 字段感知 — 解析 JSON 抽 `path` + `len(content)`,header 显示 "path, N bytes";其他工具走通用 60 字符截断。详见 §4.4.1
3. **Tool 失败时**: 不区分样式,error trace 直接显示在 lines 里
4. **Ctrl+O 无 collapsed block**: footer 状态行闪 "nothing to expand" 3 秒后清掉
5. **运行时开关**: 只走 `tool_blocks.enabled` config,不加 slash 命令
6. **渲染时机**: post-tool(下一轮 `Trace.Before` 时批量出现),不 hook eino 工具执行层
7. **Block ID 不持久化**: 进程内自增 / `/clear` 重置;不跨 session 保留状态(本就没必要)

---

## 11. 范围之外(明确不属于本方案)

- LLM prompt 里增加"鼓励简短回复"的 response_style 修订 — 是另一条改动线(上一轮分析已提),与本方案并行不耦合
- Bash 工具输出的 head/tail 截断 — 同上,防御纵深可选,不在这里做
- TUI 整体配色 / 字体 / DPI 适配
- 工具调用历史导出 / 持久化到磁盘
