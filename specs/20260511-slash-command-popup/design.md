# TUI Slash-Command 自动补全弹层

> 仿 Claude Code CLI 的命令选单：用户在输入框首字符敲 `/`，input 上方弹出
> 一个候选列表，列出当前所有内置命令；继续敲字符实时过滤；`↑/↓` 切选项，
> `Tab` 接受并补全到 input，`Enter` 接受并直接提交，`Esc` 关闭弹层。
>
> 本期对应 `specs/20260511-tui-claude-style/design.md` 里被显式延后的
> 那一条：「slash 自动补全 / header 单行化 / `/clear` 二次确认」中的
> **slash 自动补全**。`header 单行化` 和 `/clear 二次确认` 仍延后。

---

## 0. 决策(已锁定)

| # | 项 | 决定 | 备注 |
|---|---|---|---|
| 1 | 触发条件 | **input 首字符必须是 `/`，且光标位置之前的内容中没有空格** | `hello /clear` 不触发；`/clear ` 一旦敲空格就关闭弹层 —— 进入参数填写区不再补全命令名 |
| 2 | 匹配算法 | **大小写不敏感的前缀匹配** | 内置命令池 < 10 个，fuzzy 是过度设计 |
| 3 | 数据源 | **包级常量 registry** `[]slashCommand{ {Name, Desc, Args} }` | 单一真源；`builtinHelp()` 后续可以从同一份 registry 生成（本期不重写 help，外科手术化） |
| 4 | dispatch 是否改 | **本期不改** `handleBuiltin` 的 switch dispatch | registry 仅承担补全与渲染的元数据；dispatch 改造（switch → registry 反射）跟"加补全"是两件事，留到独立 commit / 下一期 |
| 5 | 选中行为 / Tab | **替换 input 为 `/<name>`**；零参命令补尾空格其实没用，**统一不补空格**（用户要么 Enter 提交、要么再敲空格自己加参数） | 替换 = `m.input.SetValue("/" + name)`，光标移到末尾 |
| 6 | 选中行为 / Enter | **接受 + 立刻提交**（等同于 Tab 接受后再按 Enter） | 让 `/help` / `/clear` 一键完成；带参数的 `/plan on` 用户改用 Tab 接、自己敲 `on`、再 Enter |
| 7 | Esc 行为 | **优先级最高：弹层开着时只关弹层，不 abort、不清 input** | 弹层关闭后再按 Esc 才走 `specs/20260511-tui-claude-style` 里那条 `abort / clear input` 链 |
| 8 | 光标 / 字符输入 | **不拦截** —— 字符照常进 textinput，update tick 之后重新派生 `popupActive` 即可 | 不引入"补全模式输入流"的二次状态机；少压调用栈 |
| 9 | 高度上限 | **最多渲染 8 条**；超出加 `… (+N more)` 单行尾巴，不做分页/滚动 | 当前 6 条命令，预留 2 条余量；真有 8+ 条命令时再上分页 |
| 10 | 流式中是否可弹 | **可以** —— 用户在等回复期间敲 `/clear` 是合理的；popup 渲染与 streaming 互不干扰 | submit 已经被 `if m.streaming { return m, nil }` 守住；Enter-from-popup 走同一条 submit，自然被同一道闸拦截 |
| 11 | 输入为空时按 `/` | **正常触发** —— input 变成 `/` 就显示全部候选 | 跟 Claude 一致：单字符 `/` 即开菜单 |

---

## 1. 目标视觉

```
❯ 上一轮的问题

⏺ 上一轮的回答 ...

✻ Cogitated for 6s
                                                              ← ★ 候选弹层从这里开始
  /clear     clear the in-memory conversation history
▍ /debug     show / hide the model's exact input & output      ← ★ 选中行(灰底)
  /exit      exit the TUI session
  /help      show built-in command help
  /plan      toggle plan mode
  /quit      exit the TUI session
  /todos     expand / collapse the todo panel

────────────────────────────────────────────────────────────  ← inputBorderStyle 已有
❯ /
────────────────────────────────────────────────────────────
  /help · ctrl-c to quit
```

继续敲字符过滤的中间态：

```
  /debug     show / hide the model's exact input & output
▍ /debug on  
                                                              ← ★ 单一匹配时整行高亮，按 Tab 接受
────────────────────────────────────────────────────────────
❯ /deb
────────────────────────────────────────────────────────────
```

> 注：右半边的 `Args` 字段如 `[on|off|toggle]`（来自 registry）也显示在
> 描述左侧 / 命令名右侧，但本期视觉先不做"两栏对齐"——左侧 `/<name>`
> 一段、空两格、剩下一段拼成 `<args> — <desc>` 的纯文本即可。

参数填写区（敲了空格之后）—— 弹层消失：

```
                                                              ← popup 隐身
────────────────────────────────────────────────────────────
❯ /plan on
────────────────────────────────────────────────────────────
```

---

## 2. 数据流 / 状态机

```
              ┌────────────── popup hidden ──────────────┐
              │                                          │
              │     input 首字符 != '/'  或  含空格      │
              │     或 matches == 0                      │
              │                                          │
keystroke ────┤                                          ├──── render
              │                                          │
              │     input 首字符 == '/'  且  无空格      │
              │     且 matches > 0                       │
              │                                          │
              └────────────── popup visible ─────────────┘
```

**只有一份新增状态**：`popupSel int`（选中下标，0-based）。其余统统派生：

| 派生量 | 来源 |
|---|---|
| `popupActive bool` | `inputStartsWithSlashNoSpace(m.input.Value())` && `len(matches) > 0` |
| `popupMatches []slashCommand` | `filterCommands(commands, query)` 每次 `View()` 调一次 |
| `query string` | `strings.TrimPrefix(m.input.Value(), "/")` |

**为什么不缓存 matches**：候选池 < 10，过滤是 O(n) 的字符串比较；每次 View
重渲只多花几百纳秒。缓存反而要在每次按键里同步一份 `popupMatches`，引入
"input 改了但 matches 没刷新"的 bug 风险。AGENTS.md：少传数据。

**`popupSel` 何时被改写**：

| 事件 | 行为 |
|---|---|
| popup 从 inactive 翻转到 active（首次出现 / 重新出现） | `popupSel = 0` |
| 过滤后 `popupSel >= len(matches)` | `popupSel = 0`（候选缩短到选不中了，回顶部） |
| `↓` / `Ctrl-N` | `popupSel = (popupSel + 1) % len(matches)`（不循环也可，本期循环） |
| `↑` / `Ctrl-P` | `popupSel = (popupSel - 1 + len(matches)) % len(matches)` |
| Tab | 接受后 input 已变 `/<name>`，新一轮派生通常 matches 只剩一条，`popupSel` 归 0；逻辑上不需要单独维护 |
| Enter / Esc | popup 这一侧不再相关 |

---

## 3. 改动点拆解

### 3.1 Registry(commit 1)

#### 新文件 `backend/cli/tui/commands.go`

```go
package tui

import "strings"

// slashCommand is the static metadata for one built-in slash command.
// Description and Args drive popup rendering only; dispatch still lives
// in update.go's handleBuiltin switch (intentionally — registry-driven
// dispatch is a separate refactor).
type slashCommand struct {
	Name string // without the leading "/"
	Args string // e.g. "[on|off|toggle]" or ""; rendered after Name
	Desc string // one short line; popup truncates to viewport width
}

// commands is the single source of truth for the popup. Order = display
// order; keep alphabetical so the menu is predictable when matches==all.
// Keep in sync with handleBuiltin in update.go — a command listed here
// but not handled there silently submits to the LLM as a prompt.
var commands = []slashCommand{
	{Name: "clear", Args: "", Desc: "clear the in-memory conversation history"},
	{Name: "debug", Args: "[on|off|toggle]", Desc: "show / hide the model's exact input & output per turn"},
	{Name: "exit", Args: "", Desc: "exit the TUI session"},
	{Name: "help", Args: "", Desc: "show this help"},
	{Name: "plan", Args: "[on|off|toggle]", Desc: "toggle plan mode"},
	{Name: "quit", Args: "", Desc: "exit the TUI session"},
	{Name: "todos", Args: "[open|close|toggle]", Desc: "expand / collapse the todo panel"},
}

// shouldShowPopup mirrors the state-machine rule from §2: input must
// start with "/" AND have no space yet (still typing the command name).
// Callers further gate on len(matches) > 0.
func shouldShowPopup(input string) bool {
	if !strings.HasPrefix(input, "/") {
		return false
	}
	// A space anywhere means we've moved into the argument region — the
	// menu should disappear and let the user fill /plan's "on", /debug's
	// "off" etc. uninterrupted.
	return !strings.ContainsAny(input, " \t")
}

// filterCommands returns the entries whose Name starts with the slash-
// stripped query, case-insensitive. Empty query returns the full list
// (so just "/" pops the whole menu, matching Claude Code's UX).
func filterCommands(all []slashCommand, query string) []slashCommand {
	query = strings.ToLower(strings.TrimPrefix(query, "/"))
	if query == "" {
		// Return a copy-free slice; callers must not mutate.
		return all
	}
	out := make([]slashCommand, 0, len(all))
	for _, c := range all {
		if strings.HasPrefix(strings.ToLower(c.Name), query) {
			out = append(out, c)
		}
	}
	return out
}
```

#### 测试 `backend/cli/tui/commands_test.go`

```go
package tui

import "testing"

func TestShouldShowPopup(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"hello", false},
		{"/", true},
		{"/cl", true},
		{"/clear", true},
		{"/plan on", false},   // space → in args region
		{"hello /clear", false}, // slash not at column 0
		{"/clear\t", false},   // tab also counts
	}
	for _, tc := range cases {
		if got := shouldShowPopup(tc.in); got != tc.want {
			t.Errorf("shouldShowPopup(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFilterCommands_PrefixCaseInsensitive(t *testing.T) {
	got := filterCommands(commands, "/DE")
	if len(got) != 1 || got[0].Name != "debug" {
		t.Errorf("expected only [debug], got %#v", got)
	}
}

func TestFilterCommands_EmptyReturnsAll(t *testing.T) {
	got := filterCommands(commands, "/")
	if len(got) != len(commands) {
		t.Errorf("/ should return all %d commands, got %d", len(commands), len(got))
	}
}

func TestFilterCommands_NoMatchReturnsEmpty(t *testing.T) {
	got := filterCommands(commands, "/zzz")
	if len(got) != 0 {
		t.Errorf("/zzz should return empty, got %#v", got)
	}
}
```

> 这个 commit **不动 UI、不动 Model、不动 dispatch**，纯加数据 + 函数 + 测试。
> 风险最低，先合进 main。

---

### 3.2 Popup 渲染 + 布局接入(commit 2)

#### Model 加字段

`backend/cli/tui/model.go`，在 `// pendingExit:` 那段附近：

```go
// popupSel is the selected index inside the currently-visible match
// set (recomputed from m.input.Value() on every render — see
// shouldShowPopup / filterCommands). When the popup is hidden the
// value is moot; handleKey resets it to 0 on every show/grow edge.
popupSel int
```

不需要 `popupActive bool`、不需要 `popupMatches []slashCommand` —— 全派生。

#### 样式新增

`backend/cli/tui/styles.go`：

```go
// Slash-command popup. The selected row reuses userBlockStyle's grey
// background so the visual vocabulary stays consistent ("filled grey
// block = where focus is"). Name is bold accent; args/desc stay dim.
popupNameStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
popupArgsStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
popupDescStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
popupRowStyle     = lipgloss.NewStyle().PaddingLeft(2)
popupSelectedRow  = lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("15")).
		PaddingLeft(2).PaddingRight(1)
```

`popupSelectedRow` 故意不 Bold —— `popupNameStyle` 渲染命令名时已经 bold，
嵌套会被 lipgloss 的内层 reset 截掉（参考 `userBlockStyle` 的同款踩坑注释）。

#### 新文件 `backend/cli/tui/popup.go`

```go
package tui

import (
	"fmt"
	"strings"
)

// popupMaxRows caps the visible candidate count. Pool currently holds 7
// commands, so 8 is comfortable headroom; bumping the cap is one-line.
const popupMaxRows = 8

// renderPopup returns "" when the popup is hidden so callers can
// unconditionally concat its output. Hidden = no `/` prefix, or the
// user has typed a space (entered argument mode), or zero matches.
//
// Layout matches the visual target in §1: one row per match, selected
// row carries the grey background. A "+N more" footer appears when
// matches exceed popupMaxRows.
func (m *Model) renderPopup() string {
	input := m.input.Value()
	if !shouldShowPopup(input) {
		return ""
	}
	matches := filterCommands(commands, input)
	if len(matches) == 0 {
		return ""
	}

	visible := matches
	overflow := 0
	if len(visible) > popupMaxRows {
		overflow = len(visible) - popupMaxRows
		visible = visible[:popupMaxRows]
	}

	sel := m.popupSel
	if sel >= len(visible) {
		sel = 0 // popupSel got out of bounds (race with filtering); render-safe fallback
	}

	lines := make([]string, 0, len(visible)+1)
	for i, c := range visible {
		body := popupRowBody(c)
		if i == sel {
			lines = append(lines, popupSelectedRow.Render(body))
		} else {
			lines = append(lines, popupRowStyle.Render(body))
		}
	}
	if overflow > 0 {
		lines = append(lines, popupRowStyle.Render(popupDescStyle.Render(
			fmt.Sprintf("… (+%d more)", overflow))))
	}
	return strings.Join(lines, "\n")
}

// popupRowBody renders one row's content sans row-level background. Format:
//
//	/<name>  <args> — <desc>
//
// Empty Args drops the gap so the row reads as `/clear — clear the …`.
func popupRowBody(c slashCommand) string {
	name := popupNameStyle.Render("/" + c.Name)
	if c.Args == "" {
		return fmt.Sprintf("%s  %s", name, popupDescStyle.Render("— "+c.Desc))
	}
	return fmt.Sprintf("%s %s  %s",
		name,
		popupArgsStyle.Render(c.Args),
		popupDescStyle.Render("— "+c.Desc),
	)
}

// popupHeight is the line count renderPopup will emit; used by
// recomputeLayout to subtract from the viewport budget. Must stay in
// lockstep with renderPopup's line count — same overflow logic.
func (m *Model) popupHeight() int {
	if !shouldShowPopup(m.input.Value()) {
		return 0
	}
	matches := filterCommands(commands, m.input.Value())
	if len(matches) == 0 {
		return 0
	}
	h := len(matches)
	if h > popupMaxRows {
		h = popupMaxRows + 1 // popupMaxRows visible rows + 1 overflow tail
	}
	return h
}
```

#### `recomputeLayout` 把 popup 算进 chrome

`backend/cli/tui/update.go`：

```go
func (m *Model) recomputeLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	headerH := 3
	streamH := 0
	if m.streaming || m.lastErr != nil {
		streamH = 1
	}
	todoH := m.todoPanelHeight()
	popupH := m.popupHeight() // NEW
	inputH := 3
	footerH := 1
	chrome := headerH + 1 + streamH + todoH + popupH + inputH + footerH
	// ... 后续不变
}
```

#### `View` 在 input 之前插入 popup

`backend/cli/tui/view.go`：

```go
func (m *Model) View() string {
	if !m.ready {
		return "Initializing..."
	}
	var sb strings.Builder
	sb.WriteString(m.renderHeader())
	sb.WriteString("\n\n")
	sb.WriteString(m.viewport.View())
	if streamPanel := m.renderStreamPanel(); streamPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(streamPanel)
	}
	if todoPanel := m.renderTodoPanel(); todoPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(todoPanel)
	}
	if popup := m.renderPopup(); popup != "" { // NEW
		sb.WriteString("\n")
		sb.WriteString(popup)
	}
	sb.WriteString("\n")
	sb.WriteString(m.renderInput())
	sb.WriteString("\n")
	sb.WriteString(m.renderFooter())
	return sb.String()
}
```

#### 关键不变量

> `popupHeight` 与 `renderPopup` 必须输出同一行数。`renderPopup` 改了
> 还要顺手改 `popupHeight`，否则 chrome 高度算错，input 被挤出屏幕底。
> 这条注释要写在 `popupHeight` 文档里（上面 commit 2 的 `popup.go`
> 注释已经覆盖）。

#### 测试 `backend/cli/tui/popup_test.go`

```go
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
)

func newModelForPopup(value string) *Model {
	ti := textinput.New()
	ti.SetValue(value)
	return &Model{input: ti}
}

func TestRenderPopup_HiddenWhenNoSlash(t *testing.T) {
	m := newModelForPopup("hello")
	if got := m.renderPopup(); got != "" {
		t.Errorf("popup must be hidden for non-slash input; got %q", got)
	}
}

func TestRenderPopup_HiddenWhenInArgRegion(t *testing.T) {
	m := newModelForPopup("/plan on")
	if got := m.renderPopup(); got != "" {
		t.Errorf("popup must hide once a space is typed; got %q", got)
	}
}

func TestRenderPopup_ShowsAllOnBareSlash(t *testing.T) {
	m := newModelForPopup("/")
	out := m.renderPopup()
	for _, c := range commands {
		if !strings.Contains(out, "/"+c.Name) {
			t.Errorf("bare slash must list every command; missing /%s", c.Name)
		}
	}
}

func TestRenderPopup_PrefixFilters(t *testing.T) {
	m := newModelForPopup("/de")
	out := m.renderPopup()
	if !strings.Contains(out, "/debug") {
		t.Errorf("/de must surface /debug; got %q", out)
	}
	if strings.Contains(out, "/clear") {
		t.Errorf("/de must filter out /clear; got %q", out)
	}
}

func TestPopupHeight_MatchesRenderLineCount(t *testing.T) {
	for _, value := range []string{"/", "/de", "/zzz", "hello", "/plan on"} {
		m := newModelForPopup(value)
		out := m.renderPopup()
		want := 0
		if out != "" {
			want = strings.Count(out, "\n") + 1
		}
		if got := m.popupHeight(); got != want {
			t.Errorf("popupHeight=%d but renderPopup has %d lines for input %q",
				got, want, value)
		}
	}
}
```

最后一个测试是**结构性不变量护栏**，防止 `popupHeight` 和 `renderPopup`
长期漂移（这是 layout bug 的常见源头）。

---

### 3.3 交互接线(commit 3)

#### `handleKey` 的新调度顺序

`backend/cli/tui/update.go` 的 `handleKey`：

```go
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Popup-active keys claim priority — when the menu is up, Up/Down,
	// Tab, Enter, Esc and Backspace all mean things specific to the
	// menu. Falling through to textinput / global handlers would (a)
	// move the textinput cursor in invisible ways, and (b) submit
	// half-typed commands.
	if m.popupShown() {
		if cmd, handled := m.handlePopupKey(msg); handled {
			return m, cmd
		}
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		// ... unchanged
	case tea.KeyEsc:
		// ... unchanged (popup already consumed Esc above if it was open)
	case tea.KeyEnter:
		// ... unchanged
	}

	// Feed key to input + viewport.
	var cmds []tea.Cmd
	var cmd tea.Cmd
	prevValue := m.input.Value() // NEW
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	// Recompute popupSel & layout on any input-value edge. popup shown
	// ↔ hidden flips, or match-set shrinking, both need a relayout.
	if m.input.Value() != prevValue {
		m.onInputChanged() // NEW
	}
	return m, tea.Batch(cmds...)
}
```

#### 新增辅助方法

```go
// popupShown is the runtime equivalent of shouldShowPopup-and-has-matches;
// callers gate keyboard routing on this.
func (m *Model) popupShown() bool {
	return m.popupHeight() > 0
}

// onInputChanged is called by handleKey after any keypress that mutated
// the input value. Two responsibilities:
//   1. Clamp popupSel into the new match range (or reset to 0 on the
//      hidden→shown edge, which is also covered by clamp-to-zero).
//   2. Trigger a relayout so popup growth/shrink rebalances the viewport.
func (m *Model) onInputChanged() {
	matches := filterCommands(commands, m.input.Value())
	if m.popupSel >= len(matches) {
		m.popupSel = 0
	}
	m.recomputeLayout()
}

// handlePopupKey routes keys while the popup is visible. Returns
// (cmd, true) when the popup consumed the key; otherwise (nil, false)
// to let handleKey fall through to the default path.
func (m *Model) handlePopupKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	matches := filterCommands(commands, m.input.Value())
	if len(matches) == 0 {
		return nil, false // defensive — popupShown() implied >0
	}
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.popupSel = (m.popupSel - 1 + len(matches)) % len(matches)
		return nil, true
	case tea.KeyDown, tea.KeyCtrlN:
		m.popupSel = (m.popupSel + 1) % len(matches)
		return nil, true
	case tea.KeyTab:
		m.acceptPopup(matches[m.popupSel]) // sets input to "/<name>"
		m.onInputChanged()
		return nil, true
	case tea.KeyEnter:
		// Accept + submit: lets /help, /clear etc. fire in one keystroke.
		m.acceptPopup(matches[m.popupSel])
		m.onInputChanged()
		// Fall through to the normal Enter path — submit reads input,
		// resets it, dispatches handleBuiltin / starts streaming. We do
		// NOT return here; let the outer switch's KeyEnter run.
		return nil, false
	case tea.KeyEsc:
		// Esc only closes the popup; abort/clear-input fall through to
		// the outer Esc handler on the NEXT Esc press.
		m.input.SetValue("") // emptying value also hides popup
		m.onInputChanged()
		return nil, true
	}
	return nil, false
}

// acceptPopup replaces the entire input value with "/<name>" and
// parks the cursor at end-of-line. Mutations go through textinput's
// SetValue so its internal cursor state stays consistent.
func (m *Model) acceptPopup(c slashCommand) {
	m.input.SetValue("/" + c.Name)
	m.input.SetCursor(len(m.input.Value()))
}
```

> ⚠️ Esc 选择"清空 input"而非"仅关闭弹层但保留 `/de` 半字串"，目的：
> popup 是输入 `/` 的副作用；用户按 Esc 表达"我反悔了"，留着 `/de`
> 又看不到弹层会很怪。"清空"是最低惊讶。

#### 走"接受+提交"那条线时为啥要 fall-through

`KeyEnter` 在 popup 分支里返回 `(nil, false)` 而不是 `(nil, true)`，是
故意的：

1. `handlePopupKey` 先把 input 改成完整命令 `/clear`；
2. 控制流跌回 `handleKey` 的外层 switch，撞上 `case tea.KeyEnter`；
3. 外层走原来的 `submit(text)` 路径，进 `handleBuiltin`，命中 `/clear`。

不这样做就要在 popup 分支里复制一遍 submit 的逻辑（重复代码），违反
AGENTS.md 的"少压调用栈、少传数据"。

#### 测试 `backend/cli/tui/popup_keys_test.go`

```go
package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func newModelForPopupKeys(value string, sel int) *Model {
	ti := textinput.New()
	ti.SetValue(value)
	return &Model{input: ti, popupSel: sel}
}

func TestHandlePopupKey_DownArrowAdvancesSelection(t *testing.T) {
	m := newModelForPopupKeys("/", 0)
	_, handled := m.handlePopupKey(tea.KeyMsg{Type: tea.KeyDown})
	if !handled {
		t.Fatal("Down must be consumed when popup is open")
	}
	if m.popupSel != 1 {
		t.Errorf("popupSel after Down = %d, want 1", m.popupSel)
	}
}

func TestHandlePopupKey_UpArrowWraps(t *testing.T) {
	m := newModelForPopupKeys("/", 0)
	m.handlePopupKey(tea.KeyMsg{Type: tea.KeyUp})
	if m.popupSel != len(commands)-1 {
		t.Errorf("popupSel after Up from 0 = %d, want %d (wrap to last)",
			m.popupSel, len(commands)-1)
	}
}

func TestHandlePopupKey_TabAcceptsSelectedCommand(t *testing.T) {
	m := newModelForPopupKeys("/de", 0)
	// at "/de" only /debug matches, so sel=0 → debug
	m.handlePopupKey(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != "/debug" {
		t.Errorf("Tab must rewrite input to /debug; got %q", got)
	}
}

func TestHandlePopupKey_EnterFallsThroughForSubmit(t *testing.T) {
	m := newModelForPopupKeys("/cl", 0)
	cmd, handled := m.handlePopupKey(tea.KeyMsg{Type: tea.KeyEnter})
	if handled {
		t.Error("Enter must fall through (handled=false) so outer KeyEnter submits")
	}
	if cmd != nil {
		t.Error("Enter must not return a tea.Cmd from popup branch")
	}
	if m.input.Value() != "/clear" {
		t.Errorf("Enter must accept selected command first; input=%q", m.input.Value())
	}
}

func TestHandlePopupKey_EscClosesPopupByEmptyingInput(t *testing.T) {
	m := newModelForPopupKeys("/de", 0)
	_, handled := m.handlePopupKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled {
		t.Fatal("Esc must be consumed when popup is open")
	}
	if m.input.Value() != "" {
		t.Errorf("Esc must clear input (and thus hide popup); got %q", m.input.Value())
	}
}

func TestOnInputChanged_ResetsSelOnRangeShrink(t *testing.T) {
	m := newModelForPopupKeys("/", 5) // sel=5 is valid for full pool
	m.input.SetValue("/de")           // shrinks matches to [debug] only
	m.onInputChanged()
	if m.popupSel != 0 {
		t.Errorf("popupSel must reset to 0 when matches shrink below it; got %d",
			m.popupSel)
	}
	_ = textinput.New // appease imports
}
```

---

## 4. 跟现有结构的兼容性

### 4.1 不动的部件

- `handleBuiltin` switch dispatch —— registry 仅服务于弹层，dispatch 保留原样
- `builtinHelp()` —— 后续可以重写成 `for _, c := range commands { ... }`，
  本期不动（一致性 vs 改动面，外科手术化）
- TodoPanel、stream panel、header、footer、user/assistant 渲染、`/clear`
  二次确认（后者本就延后）—— 全部不变
- `submit()` / `handleBuiltin` / `handlePlanCmd` 等 —— 不变
- Trace pipeline、`m.debug` —— 不变

### 4.2 KeyMsg 路由变化

唯一全局影响：`handleKey` 顶部新增了 popup-active 优先调度。Bubbletea
里键事件本来就只走 `Update` 一条，加一层"if popup-active 先 try-claim"
不会破坏既有路径——claim 失败就退回原 switch。

`textinput` 自身也消费 `KeyUp`/`KeyDown`（左右移光标在 textinput 是
KeyLeft/KeyRight，Up/Down 在单行 textinput 实际不消费，是 no-op）—— 抢
到 popup 用安全。

### 4.3 推荐位的高度上限

`recomputeLayout` 的 `vpMax < 3` 兜底：极端情况下窗口很矮 + popup 8 行 +
todo 面板展开 + streaming，能把 viewport 挤到 3 行以下。这种边界保留
viewport=3 的下限即可（已有），popup **不**做"窗口太矮就不显示"的特殊
分支——按 Claude 同款行为，宁可挤掉 viewport 也要显示 popup（用户正在
输入，input 区域必须在视野里）。

### 4.4 跟 `specs/20260511-tui-claude-style` 的 ESC 链关系

那一期定义了：
- streaming 时 Esc → abort
- idle 时 Esc → clear input

本期插入到最前：
- popup 显示中 Esc → close popup（通过清空 input 来做）

三条规则的优先级：`popup > streaming > idle`。前一期的两个测试
（`TestHandleKey_EscDuringStreamAborts` / `TestHandleKey_EscIdleClearsInput`）
仍然必须通过 —— 本期改动只在 popup 显示中拦截，两个旧测试场景里
popup 都是隐的，不冲突。

### 4.5 `/clear` 与 popup 的相互作用

`/clear` 进 `handleBuiltin` 后会 `m.rt.ClearHistory()` + 重建消息。
input 在 submit 入口被 `m.input.Reset()`（update.go 第 160 行）清空，
所以 popup 一并消失。无需特别处理。

---

## 5. Commit 拆分

按 `AGENTS.md` "commit 粒度"，每个 commit 一句话能说清，纯数据 / 渲染 /
交互三件事独立 ship。

### Commit 1 — `tui: introduce slash-command registry`

- 新文件 `commands.go`（registry + `shouldShowPopup` + `filterCommands`）
- 新文件 `commands_test.go`（4 个测试）
- **零 UI 改动、零行为变更**
- 若 review 卡住可单独回滚

### Commit 2 — `tui: render slash-command popup above input`

- 新文件 `popup.go`（`renderPopup` + `popupHeight`）
- `model.go` 加 `popupSel int` 字段
- `styles.go` 加 popup 系列 style
- `update.go` 的 `recomputeLayout` 加 `popupH`
- `view.go` 的 `View()` 在 input 上方插 popup
- 新文件 `popup_test.go`（5 个测试，含 height/render 不变量护栏）
- **可见但不可交互**：popup 跟着 input value 显隐，但 ↑↓Tab 没接，
  textinput 自身收掉。中间态可观察、可截图，对 review 友好。

### Commit 3 — `tui: wire popup navigation and accept`

- `update.go` 的 `handleKey` 加 popup 优先调度
- 新增 `popupShown` / `onInputChanged` / `handlePopupKey` / `acceptPopup`
- 新文件 `popup_keys_test.go`（6 个测试）
- 完整交互上线

---

## 6. 测试策略汇总

总共 **15** 个测试，全部在 `backend/cli/tui` 包内：

| 文件 | 测试 |
|---|---|
| `commands_test.go`（新） | TestShouldShowPopup / TestFilterCommands_PrefixCaseInsensitive / EmptyReturnsAll / NoMatchReturnsEmpty |
| `popup_test.go`（新） | TestRenderPopup_HiddenWhenNoSlash / HiddenWhenInArgRegion / ShowsAllOnBareSlash / PrefixFilters / TestPopupHeight_MatchesRenderLineCount |
| `popup_keys_test.go`（新） | TestHandlePopupKey_DownArrowAdvancesSelection / UpArrowWraps / TabAcceptsSelectedCommand / EnterFallsThroughForSubmit / EscClosesPopupByEmptyingInput / TestOnInputChanged_ResetsSelOnRangeShrink |

**不涉及** runtime / agent / middleware / trace 任何代码，纯 TUI 层。

跑通命令：

```bash
go test ./backend/cli/tui/...
```

---

## 7. 风险 / 不确定项

1. **`SetCursor` 兼容性**：bubbles textinput 的 `SetCursor` 在某些
   版本叫法不一。落地时确认现版本（go.mod 锁定的 `bubbles` 版本）的
   API；若没有 `SetCursor`，回退到 `m.input.CursorEnd()` 即可，效果
   一致。

2. **Tab 跟 textinput 的潜在冲突**：bubbles textinput 默认不消费 Tab；
   单行模式里 Tab 是 no-op。`handlePopupKey` 在 popup 显示中抢走 Tab
   是安全的。**风险点**：弹层关闭后用户敲 Tab —— 此时 fall-through 到
   textinput，行为依然是 no-op，**没有**意外副作用。

3. **`popupMaxRows = 8` 的截断顺序**：当前 `filterCommands` 不排序，
   依赖 registry 的声明顺序（字母序）。命令池长大到 9+ 时，"被截掉
   的命令永远是字母序末尾几个"，例如新增 `/version` 不会被显示直到
   用户敲 `/v`。这是预期行为；如要加"被选中过的命令置顶"等启发，
   是单独的产品决策，下期再做。

4. **viewport 高度被挤**：低分辨率 + popup 8 行 + todos 展开同时出现
   时，viewport 会被压到下限 3 行。Bubbletea 在此场景里 viewport 仍
   可正常滚动；用户体验略差但不崩。**对策**：暂不处理，靠 §4.3 的
   下限兜底；真有用户抱怨再考虑"窗口 < N 时自动折叠 todo 面板"。

5. **CJK 输入法的影响**：macOS 中文输入法的 candidate window 会浮在
   terminal 之上 —— 用户敲半角 `/` 没问题；如果用了全角 `／`（U+FF0F），
   `shouldShowPopup` 用 `strings.HasPrefix(_, "/")` 不会命中。这跟
   `handleBuiltin` 已有的 `/` 同款假设一致，本期不处理（用户既然能敲
   `/clear` 就一定是半角）。

6. **`commands` 与 `handleBuiltin` 分两处的漂移风险**：commit 1 的注释
   已经强调一次同步。落地时建议建一个 lint 风格的测试 `TestCommands_AllDispatched`
   ——遍历 registry，模拟 `submit("/<name>")`，断言 `handleBuiltin` 返回
   `handled=true`。可以放进 commit 1，也可以延到 commit 3 跟交互测试
   并肩。**初版本不强制**，但建议加，drift 防御成本很低。
