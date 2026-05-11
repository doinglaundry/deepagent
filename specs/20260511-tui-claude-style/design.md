# TUI Claude-style 流式 UX

> 仿 Claude Code CLI 的几个关键 UX 元素,改进现有 TUI 在长回复 / 等待 / 中断
> 三个场景下的体验。本期不动 slash 自动补全、不动 header 单行化、不动
> `/clear` 二次确认 —— 它们是独立改进点,延后一期。

## 0. 决策(已锁定)

| # | 项 | 决定 | 备注 |
|---|---|---|---|
| 1 | 流式期间是否保留 dim 6 行预览 | **去掉** | streaming 时只显示一行思考状态;done 后整段 markdown 进 scrollback |
| 2 | 思考耗时摘要(`✻ Cogitated for 6s`)是否设阈值 | **设阈值 ≥ 2s** | 避免短 prompt 出 `for 0s` 噪声 |
| 3 | `⏺` 是否摘掉、改纯色差 | **不摘** | Claude 也保留了 `⏺`;之前讨论的方案 1 撤回 |
| 4 | slash 自动补全 / header 单行化 / `/clear` 二次确认 / footer token usage / plan 徽章 | **本期不做** | 留下一期 |

## 1. 目标视觉

```
❯ 你现在是什么样的模型

⏺ 我在这个会话里是 Claude Code 助手，当前底层模型是 gpt-5.3-codex。
  主要能力是代码阅读、修改、调试、命令行与工程化协作。           ← ★ 续行 2-cell indent

✻ Cogitated for 6s                                            ← ★ 完成态摘要(留在 scrollback)

❯ 你都有哪些skill

✶ Moonwalking… (6s · thinking)                                ← ★ 实时计时的思考状态

────────────────────────────────────────────────────────────  ← inputBorderStyle 已有
❯
────────────────────────────────────────────────────────────
  esc to interrupt                                            ← ★ streaming 时 footer
```

## 2. 改动点拆解

### 2.1 Assistant body 续行 indent(commit 1)

#### 现状

```125:135:backend/cli/tui/model.go
func (m *Model) renderMessage(msg chatMessage) string {
    switch msg.Role {
    case "user":
        return userPrefixStyle.Render("❯ ") + msg.Content
    case "assistant":
        body := msg.Rendered
        if body == "" {
            body = msg.Content
        }
        return assistantPrefixStyle.Render("⏺ ") + body
    ...
```

glamour 内部会做 word-wrap(`WithWordWrap(width)`),但 wrap 出的换行从第 0 列开始,跟 prefix 不对齐 —— body 的第 2 行视觉上"脱离"了 `⏺` 块。

#### 改

```go
case "assistant":
    body := msg.Rendered
    if body == "" {
        body = msg.Content
    }
    // Indent continuation lines by 2 cells so they align under the
    // prefix glyph. glamour does word-wrap but the wrapped lines
    // start at column 0; without this, the second line looks like
    // it left the message block.
    body = strings.ReplaceAll(body, "\n", "\n  ")
    return assistantPrefixStyle.Render("⏺ ") + body
```

#### glamour 的 wrap 宽度配套扣 2 cell

```128:140:backend/cli/tui/model.go
func (m *Model) renderMarkdown(content string) string {
    width := m.viewport.Width
    if width <= 0 {
        width = 80
    }
```
→
```go
func (m *Model) renderMarkdown(content string) string {
    width := m.viewport.Width - 2 // reserve 2 cells for the "⏺ " prefix
    if width <= 0 {
        width = 80
    }
```

否则窄屏下续行会超出 viewport.Width 2 个字符,出现末行越界。

#### 测试

`TestRenderMessage_AssistantContinuationIndent`:输入一个 `"line one\nline two"`(强制 raw,跳过 markdown),断言输出中 `"\n  line two"` 而非 `"\nline two"`。

---

### 2.2 思考状态指示器(commit 2)

#### 数据结构

新文件 `backend/cli/tui/verbs.go`:

```go
package tui

import "math/rand"

// verbs holds (present, past) pairs used by the thinking indicator.
// Present-tense (with "…") drives the live readout while streaming;
// past-tense lands in scrollback as the completion summary.
// Indexes must stay aligned so a turn's verb is consistent across
// states — picking past-tense on completion uses the same i.
var verbs = []struct{ Present, Past string }{
    {"Moonwalking", "Moonwalked"},
    {"Cogitating", "Cogitated"},
    {"Pondering", "Pondered"},
    {"Brewing", "Brewed"},
    {"Marinating", "Marinated"},
    {"Tinkering", "Tinkered"},
    {"Conjuring", "Conjured"},
    {"Distilling", "Distilled"},
    {"Stewing", "Stewed"},
    {"Noodling", "Noodled"},
    {"Mulling", "Mulled"},
    {"Percolating", "Percolated"},
    {"Plotting", "Plotted"},
    {"Reasoning", "Reasoned"},
    {"Scheming", "Schemed"},
}

// pickVerb returns a random (present, past) pair. Tests can wrap rand
// via the seed if determinism is needed.
func pickVerb() (present, past string) {
    v := verbs[rand.Intn(len(verbs))]
    return v.Present, v.Past
}
```

#### Model 新字段

```go
// streamStart marks when the current turn's submit fired; elapsed is
// rounded to seconds and ticked from spinner.TickMsg (no extra ticker
// goroutine). verbPresent / verbPast are picked once per submit and
// shared between the live indicator and the completion summary.
streamStart  time.Time
elapsed      time.Duration
verbPresent  string
verbPast     string
```

#### `submit()` 头部抽词 + 记时

```go
m.verbPresent, m.verbPast = pickVerb()
m.streamStart = time.Now()
m.elapsed = 0
```

#### spinner.TickMsg 顺带更新 elapsed

```go
case spinner.TickMsg:
    if !m.streaming {
        return m, nil
    }
    var cmd tea.Cmd
    m.spin, cmd = m.spin.Update(msg)
    m.elapsed = time.Since(m.streamStart).Round(time.Second) // ← 加这一行
    return m, cmd
```

spinner 默认 100ms tick,view 已经 100ms 重渲染一次,加一行 time math 不增成本。**不另起 `tea.Tick(time.Second)` 协程**(`AGENTS.md`:少压调用栈、少传数据)。

#### `renderStreamPanel` 重写

```go
func (m *Model) renderStreamPanel() string {
    if m.streaming {
        secs := int(m.elapsed.Seconds())
        return fmt.Sprintf("%s %s %s",
            thinkingMarkerStyle.Render("✶"),
            thinkingPresentStyle.Render(m.verbPresent+"…"),
            dimStyle.Render(fmt.Sprintf("(%ds · thinking)", secs)),
        )
    }
    if m.lastErr != nil {
        return errorStyle.Render(fmt.Sprintf("error: %s", m.lastErr))
    }
    return ""
}
```

> `(thinking)` tag 目前固定。未来若想跟 plan mode / tool execution 联动
> 显示 `(planning)` / `(running shell)`,扩展点在这一行,不动结构。

#### 流式预览整段删除

`renderStreamPanel` 里 `body := strings.TrimSpace(m.streamBuf.String())` 那段以及
`truncateForStream` 整个函数都删。**`m.streamBuf` 累积逻辑保留** —— `handleDone`
的 error 分支仍要它做 fallback 输出。

#### `handleResize` 的 streamH

```go
streamH := 0
if m.streaming || m.lastErr != nil {
    streamH = 1 // was 3 (header + 2 preview lines)
}
```

#### 样式新增(styles.go)

```go
thinkingMarkerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))            // ✶ magenta
thinkingPresentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")) // bold white
```

#### 测试

- `TestPickVerb_PresentAndPastIndexAligned`:扫所有 verbs,断言 Past 不为空且不含 "…"
- `TestRenderStreamPanel_DuringStreaming_ContainsVerbAndElapsed`:m.streaming=true、
  m.verbPresent="Mooning"、m.elapsed=6s → 输出含 `Mooning…` 和 `(6s · thinking)`
- `TestRenderStreamPanel_IdleEmpty`:m.streaming=false、m.lastErr=nil → 空串
- `TestSpinnerTick_UpdatesElapsed`:模拟 spinner.TickMsg,断言 elapsed 跟随
  `time.Since(streamStart)` 变化(用 `streamStart = now-3s`,断言 elapsed ≥ 3s)

---

### 2.3 完成态摘要进 scrollback(commit 3)

#### `handleDone` 改

```go
func (m *Model) handleDone(msg doneMsg) (tea.Model, tea.Cmd) {
    elapsed := time.Since(m.streamStart).Round(time.Second)
    m.streaming = false
    m.cancel = nil
    m.chunkCh = nil

    if msg.err != nil {
        m.lastErr = msg.err
        if buf := strings.TrimSpace(m.streamBuf.String()); buf != "" {
            m.pushMessage("assistant", buf)
        }
        m.pushMessage("system", fmt.Sprintf("error: %s", msg.err))
        m.streamBuf.Reset()
        return m, nil
    }

    final := strings.TrimSpace(msg.output)
    if final == "" {
        final = strings.TrimSpace(m.streamBuf.String())
    }
    if final != "" {
        m.pushMessage("assistant", final)
    }

    // Threshold: short turns (< 2s) don't warrant a "for 0s" summary
    // — visual noise without info. The 2s line is a single point of
    // tuning; if user feedback says even 2s is too chatty, raise it.
    const summaryThreshold = 2 * time.Second
    if elapsed >= summaryThreshold {
        m.pushMessage("thinking-summary",
            fmt.Sprintf("%s for %ds", m.verbPast, int(elapsed.Seconds())))
    }

    m.streamBuf.Reset()
    return m, nil
}
```

**顺序**:assistant 在前,thinking-summary 在后(跟 Claude 截图一致:`⏺ ...`
块,然后空行,然后 `✻ ...`,然后下一轮 `❯`)。

#### `renderMessage` 加 `thinking-summary` 角色

```go
case "thinking-summary":
    return thinkingSummaryStyle.Render("✻ " + msg.Content)
```

#### `chatMessage.Role` 文档注释更新

```go
// Role: "user" | "assistant" | "system" | "debug-input" | "debug-output"
//     | "thinking-summary" | "banner"
Role string
```

#### 样式新增

```go
thinkingSummaryStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("13")) // dim magenta ✻
```

#### 测试

- `TestHandleDone_PushesSummaryAboveThreshold`:`m.streamStart = now - 3s`、
  doneMsg.output="hi" → messages 末尾 2 条是 assistant + thinking-summary
- `TestHandleDone_NoSummaryBelowThreshold`:`m.streamStart = now - 500ms` → 末尾只有 assistant,没有 thinking-summary
- `TestHandleDone_ErrorPathSkipsSummary`:error 时不发 summary(已经够吵)
- `TestRenderMessage_ThinkingSummary`:role=thinking-summary、content="Cogitated for 6s" → 输出含 "✻ Cogitated for 6s"

---

### 2.4 ESC 中断 + footer 简化(commit 4)

#### `handleKey` 加 ESC 分支

```go
case tea.KeyEsc:
    if m.abortStream() {
        return m, nil
    }
    // Idle: clear the input so user gets a fresh prompt instead of
    // mid-typed garbage; mirrors what Ctrl-U does in most shells.
    m.input.SetValue("")
    return m, nil
```

#### `renderFooter` 重写

当前:
```go
func (m *Model) renderFooter() string {
    left := footerStyle.Render(m.modelName)
    hint := "Enter to send · /help for commands · Ctrl-C to abort/quit"
    if m.streaming {
        hint = "Streaming... · Ctrl-C to abort"
    }
    right := footerStyle.Render(hint)
    gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
    if gap < 1 {
        gap = 1
    }
    return left + strings.Repeat(" ", gap) + right
}
```

新:
```go
func (m *Model) renderFooter() string {
    var hint string
    if m.streaming {
        hint = "esc to interrupt"
    } else {
        hint = "/help · ctrl-c to quit"
    }
    return "  " + footerStyle.Render(hint)
}
```

> model name 从 footer 撤下 —— header 已经显示了,重复了一份没价值;
> footer 现在专做 "当下能按什么键" 的 hint。

#### 测试

- `TestHandleKey_EscDuringStreamAborts`:模拟 streaming + cancel hook,按 ESC
  → cancel 被调用、不退出
- `TestHandleKey_EscIdleClearsInput`:idle、input 含 "abc" → 按 ESC → input 空
- `TestRenderFooter_StreamingShowsInterrupt`:m.streaming=true → 含 "esc to interrupt"
- `TestRenderFooter_IdleShowsHelpHint`:m.streaming=false → 含 "/help" + "ctrl-c"

---

## 3. 跟现有结构的兼容性

### 3.1 不动的部件

- `inputBorderStyle`(上下分隔线)已经跟 Claude 一致
- `pushMessage` / `rebuildHistory` / `renderMessage` 的整体形态保留
- `m.spin` (`bubbles/spinner`) 继续转;只是它的 view 不再单独 render —— 思考
  指示器自己渲染,spinner 只贡献 TickMsg(用于 elapsed 计时)
- TodoPanel 不动
- Trace pipeline / `m.debug` / `/debug`-`/plan`-`/todos` 不动

### 3.2 spinner 的去留

`m.spin` 现在还需不需要?

- **需要**:它的 TickMsg 是 elapsed 计时的唯一驱动(100ms 频率,够刷新 "6s")
- **不需要**:它的 `.View()` 不再被渲染(指示器自己用静态 `✶`)

结论:**保留**,只用它的 tick。**但 `sp.Spinner = spinner.Dot` / `sp.Style = primaryStyle`
这两行可以删** —— 不再有视觉输出,style/glyph 无所谓。

### 3.3 streaming 时 viewport 高度

- 旧:streamH = 3(spinner + 6 行预览,实际占 3 行高)
- 新:streamH = 1(单行指示器)

→ viewport **多 2 行可用**,长对话 scrollback 直接受益。

## 4. Commit 拆分

按 `AGENTS.md` "commit 粒度":每个 commit 一句话能描述,纯重命名 / 行为变更
不混。

### Commit 1 — `tui: indent assistant continuation lines`

- `renderMessage` 的 assistant 分支加续行 indent
- `renderMarkdown` 的 wrap width 扣 2 cell
- 1 个测试

### Commit 2 — `tui: replace stream preview with thinking indicator`

行为变更(摘了流式预览 + 加思考指示器),整体一个 commit 反而比拆"先摘
预览"+"再加指示器"更清晰 —— 摘了之后中间状态是空 panel,不健康。

- 新文件 `verbs.go`
- `Model` 加 `streamStart/elapsed/verbPresent/verbPast`
- `submit()` 抽词记时
- spinner.TickMsg 顺带更新 elapsed
- `renderStreamPanel` 重写
- 删 `truncateForStream`
- `handleResize` streamH 3→1
- styles.go 加 `thinkingMarkerStyle/thinkingPresentStyle`
- 4 个测试

### Commit 3 — `tui: persist thinking summary in scrollback`

- `handleDone` 在 ≥2s 时 push `thinking-summary`
- `renderMessage` 加 `thinking-summary` 分支
- styles.go 加 `thinkingSummaryStyle`
- `chatMessage.Role` doc 注释更新
- 4 个测试

### Commit 4 — `tui: ESC to interrupt + simpler footer`

- `handleKey` 加 `KeyEsc`
- `renderFooter` 重写
- 4 个测试

---

## 5. 测试策略汇总

总共 13 个测试,所有都在 `backend/cli/tui` 包内:

| 文件 | 测试 |
|---|---|
| `continuation_indent_test.go` (新) | TestRenderMessage_AssistantContinuationIndent |
| `verbs_test.go` (新) | TestPickVerb_PresentAndPastIndexAligned |
| `thinking_indicator_test.go` (新) | TestRenderStreamPanel_DuringStreaming_ContainsVerbAndElapsed / IdleEmpty / TestSpinnerTick_UpdatesElapsed |
| `thinking_summary_test.go` (新) | TestHandleDone_PushesSummaryAboveThreshold / NoSummaryBelowThreshold / ErrorPathSkipsSummary / TestRenderMessage_ThinkingSummary |
| `esc_footer_test.go` (新) | TestHandleKey_EscDuringStreamAborts / EscIdleClearsInput / TestRenderFooter_StreamingShowsInterrupt / IdleShowsHelpHint |

无 e2e 测试(本期不动 runtime / agent / middlewares 任何代码)。

---

## 6. 风险 / 不确定项

1. **`✶` glyph 在某些终端宽度不一**(部分老 Linux console 把它显示为 2 cell)
   → 影响指示器对齐。**对策**:已知 macOS Terminal / iTerm2 / Alacritty / kitty /
   WezTerm / VS Code 内置都按 1 cell 渲染;若回归,fallback 到 ASCII `*`。
2. **glamour wrap width 扣 2 后**,渲染输出可能跟之前对比有 ±1 行差异,glamour
   行为不完全确定。**对策**:实测一下 80 列长段落,如果出问题再调。
3. **rand 全局**:`pickVerb` 用 `math/rand` 全局;Go 1.20+ 默认自动 seed,
   不需要手动 `rand.Seed`。低风险。
4. **`thinking-summary` 跟 `/clear`**:`/clear` 应该一并清掉旧 thinking-summary —
   它就是普通 `chatMessage`,跟着 `m.messages = freshMessages()` 一起没了,
   不用专门处理。
