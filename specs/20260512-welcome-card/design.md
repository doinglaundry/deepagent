# 启动 Welcome Card — 技术方案

> 日期: 2026-05-12  
> 目标 UX 参照 (Claude Code v2.1 启动屏):
>
> ```
> ╭─── Claude Code v2.1.139 ─────────────────────────────────────────────────────╮
> │                                       │ What's new                           │
> │             Welcome back!             │ Added agent view (Research Preview)… │
> │                                       │ Added `/goal` command: set a comple… │
> │               ▗ ▗   ▖ ▖               │ Added `/scroll-speed` command to tu… │
> │                                       │ /release-notes for more              │
> │                                       │                                      │
> │   gpt-5.3-codex · API Usage Billing   │                                      │
> │           /Users/bytedance            │                                      │
> ╰──────────────────────────────────────────────────────────────────────────────╯
> ```

---

## 0. 决策 / 待确认

| 项 | 倾向决策 | 备注 |
|---|---|---|
| 替换路径 | **直接替换现有 `renderBanner()`**,不并存两种形态 | 现有 figlet "SGADK" + 副标题/版本/作者三行不再用;`/clear` 也走新 banner |
| 数据来源 | **3 个进程内字段 + 1 个 const slice** | `m.modelName` / `m.cwd` / `bannerVersion` 已存在;release notes 用代码内 const slice(随版本号同 commit 改) |
| Release notes 来源 | **代码内 const(`banner_notes.go`)**,不读文件 | 启动零 I/O;notes 跟版本号绑死,bump version 必须同 commit 改 notes,避免"版本号变了但 What's new 还是上一版"漂移 |
| 显示条数 | **取 const slice 前 3 条**,溢出截断尾巴加 `…` | Claude Code 显示 3 行加 "/release-notes for more";我们没有 `/release-notes` 命令,先 silent 截断 |
| 两栏阈值 | **`m.width >= 80` 时双栏 box,否则回退现有竖向布局** | 终端窄到撑不开两栏时,boxed 双栏会自动 fallback;不引入第三套"中等宽度"形态 |
| 标题嵌顶边 | **手写嵌入** `"╭─── eino-cli v1.1.0 ───…─╮"`,不用 lipgloss 内置标题(无此 API) | lipgloss `Border` 只画框,不放标题字符串;自己拼一行替代框的 top |
| 颜色 | **保留 weekdayPalette**,只染 box 边框,左栏 "Welcome back!" 跟模型/cwd 走 dimStyle | ROYGBIV 是仓库视觉资产,搬到边框上保留 |
| Ascii 小 glyph | **沿用现有 `bannerASCII` 5 行"SGADK" figlet**,缩到 box 内左栏 | 不为新 banner 重新设计 logo;能塞进左栏宽度的话保留 |
| 版本号位置 | **嵌在顶边标题**,左栏不再单独列 "Version: 1.0.1" 一行 | 信息折叠;只在一个地方出现 |
| Author 显示 | **删** | Claude Code 没有 author 字段;签名信息走 commit / README,banner 上不必占行 |
| Billing 标签 | **`<provider>` 直接当 billing 字段**(如 `kimi`、`openai`)| 不引入 `cfg.Billing` 段;provider 已经在 `cfg.Models[name].Provider`,take it |
| `/clear` 行为 | **重新 `renderWelcomeCard()`** | 每次 /clear 都是一次新的"会话起点",notes 也跟着刷新一遍(虽然内容一样,日期颜色会变) |

---

## 1. 现状

[backend/cli/tui/banner.go](backend/cli/tui/banner.go) 当前 57 行:

```go
const (
    bannerSubtitle = "AI-Driven Development Kit"
    bannerVersion  = "1.0.1"
    bannerAuthor   = "YINGBAO SUN"
)

func renderBanner() string {
    parts := []string{
        bannerArtStyle().Render(bannerASCII),
        "",
        dimStyle.Render(bannerSubtitle),
        dimStyle.Render("Version: " + bannerVersion),
        dimStyle.Render("Author:  " + bannerAuthor),
    }
    return strings.Join(parts, "\n")
}
```

启动和 `/clear` 都通过 [model.go:140](backend/cli/tui/model.go#L140) / [update.go:330](backend/cli/tui/update.go#L330) 的 `freshMessages()` 注入一条 `role:"banner"` 消息,内容 = `renderBanner()` 输出。

`Model` 上已有可用字段:
- `m.modelName string` (= `rt.Name()`,init 时设好)
- `m.cwd string` (= `os.Getwd()`,init 时设好)
- `m.width int` (bubbletea `WindowSizeMsg` 同步)

测试在 [banner_test.go](backend/cli/tui/banner_test.go) — 4 个用例,断言 figlet 行 / subtitle / version / author / weekday palette 完整性。**改 banner 形态时,这 4 个测试要同步改**(尤其 author 字段会消失)。

---

## 2. 目标 UX

宽屏(width >= 80):

```
╭─── eino-cli v1.1.0 ──────────────────────────────────────────────────────────╮
│                                       │ What's new                           │
│             Welcome back!             │ • rebuilt fs/shell tools; tool-call  │
│                                       │   slog.Debug observability hook      │
│           ____   ____    _            │ • slog default handler now honors    │
│          / ___| / ___|  / \           │   cfg.LogLevel                       │
│          \___ \| |  _  / _ \          │ • yaml/config.yaml untracked;        │
│           ___) | |_| |/ ___ \         │   yaml/CHANGELOG.md is the contract  │
│          |____/ \____/_/   \_\        │                                      │
│                                       │                                      │
│   kimi · moonshot-v1-auto             │                                      │
│   /Users/bytedance/go/src/content/LLM │                                      │
╰──────────────────────────────────────────────────────────────────────────────╯
```

窄屏(width < 80,或 width 未初始化的首屏):回退现有竖向布局,只把版本号合并到 ASCII art 头部:

```
 ____   ____    _    ____  _  __     eino-cli v1.1.0
/ ___| / ___|  / \  |  _ \| |/ /
\___ \| |  _  / _ \ | | | | ' /      kimi · moonshot-v1-auto
 ___) | |_| |/ ___ \| |_| | . \      /Users/bytedance/go/src/content/LLM
|____/ \____/_/   \_\____/|_|\_\
```

---

## 3. 落点 & 文件结构

修改:

- [backend/cli/tui/banner.go](backend/cli/tui/banner.go) — 重写 `renderBanner(width int, modelName, cwd string)`,按 width 分发到 `renderWelcomeCard` 或 `renderBannerCompact`。新增 `boxTitleLine` / `splitColumns` / `getRow` / `chooseColumnWidths` / `renderLeftColumn` / `renderRightColumn` / `wordWrap` / `colorizeBorders` 一组 helper。
- [backend/cli/tui/banner_test.go](backend/cli/tui/banner_test.go) — 旧用例(figlet 行 / version / author 全在)需要改;增加宽/窄两路径用例 + box 标题断言 + SIGWINCH 重渲回归。
- [backend/cli/tui/update.go](backend/cli/tui/update.go) — `handleResize` 内的 `for i := range m.messages` 循环里多加一支 `case "banner"`,把 banner 行重新过一遍 `renderBanner(m.width, ...)`。见 §4.4。

新增:

- [backend/cli/tui/banner_notes.go](backend/cli/tui/banner_notes.go) — `releaseNotes []string` const slice。渲染逻辑直接内联在 `renderRightColumn` 里(`wordWrap` + 加 bullet 前缀),没必要单独抽 `renderReleaseNotes` 函数。
- [backend/cli/tui/banner_box_test.go](backend/cli/tui/banner_box_test.go) — `boxTitleLine` / `splitColumns` / `getRow` 三个纯 helper 的单测,跟 banner 高层逻辑的测试分文件。

调用点 —— `freshMessages` 签名改为 `freshMessages(width int, modelName, cwd string)`,**free function**,不挂 receiver(AGENTS.md "行为住在普通顶层函数里。不挂 receiver"):

- [model.go:140](backend/cli/tui/model.go#L140): `freshMessages(0, rt.Name(), cwd)` —— 构造期 WindowSizeMsg 还没到,width=0 直接走 compact。
- [update.go:330](backend/cli/tui/update.go#L330): `freshMessages(m.width, m.modelName, m.cwd)`(`/clear` 路径)。
- 测试文件 6 处 `freshMessages()` → `freshMessages(0, "", "")` 同步迁移:`banner_test.go`(3)、`plan_test.go`(1)、`todo_render_test.go`(5)。

---

## 4. 数据细节

### 4.1 顶边标题嵌入

lipgloss 的 `Border` API 只画 4 个角 + 4 条边,**不支持**在边里插标题。手写一个 helper:

```go
// boxTitleLine returns the top border with `title` centred / left-padded into
// the dash run. Result width == width, always; if title overflows the available
// dash budget it's truncated with "…".
func boxTitleLine(width int, title string) string {
    // ╭─── title ───…──╮  shape:
    //  L pad: 3 dashes after the left corner before the title spacer
    //  R pad: fill the remaining cells with dashes, plus 1 closing corner
    const leftPad = 3
    inner := width - 2 // strip ╭ and ╮
    label := " " + title + " "
    if leftPad+len(label) > inner {
        label = " " + ansiTruncate(title, inner-leftPad-2) + " "
    }
    leftDashes := strings.Repeat("─", leftPad)
    rightDashes := strings.Repeat("─", inner-leftPad-len(label))
    return "╭" + leftDashes + label + rightDashes + "╮"
}
```

为啥手写:lipgloss 升级到含此能力的版本前,自己拼一行总宽 `width` 的字符串是最便宜的方案。所有 box 内部行也手拼 `"│"+content+"│"`,统一一处控制 padding。

### 4.2 两栏 join

左右两栏先各自渲染成 `[]string`(行序),再 `splitColumns` 把每行包成 `│ left │ right │`。两栏宽度按 box 总宽分:左栏占 40%,右栏占余下减 3(两个分隔 `│` + 中间一个 `│`)。

```go
// splitColumns lays left/right as parallel columns separated by │.
// Shorter column is padded with empty lines; longer column is truncated to
// the longer side's length (we never grow the box height beyond the taller
// column). Both sides hard-pad to leftW / rightW so the right `│` aligns.
func splitColumns(left, right []string, leftW, rightW int) []string {
    n := max(len(left), len(right))
    out := make([]string, n)
    for i := 0; i < n; i++ {
        l := getRow(left, i, leftW)
        r := getRow(right, i, rightW)
        out[i] = "│ " + l + " │ " + r + " │"
    }
    return out
}
```

`getRow` 负责 pad/truncate 到固定宽度。**测宽用 `lipgloss.Width`,不是 `runewidth.StringWidth`**:lipgloss.Width 会先剥 ANSI 再算 cell,这样 `renderLeftColumn` 里 `lipgloss.PlaceHorizontal` 预先 pad 好的 styled row(里面有 ANSI escape)走 `getRow` 时不会被当成超宽错误截断。纯文本场景下 lipgloss.Width 跟 runewidth.StringWidth 一致。截断兜底仍用 `runewidth.Truncate`(纯文本输入路径,不会塞 ANSI 进来)。

`runewidth` 当前是 `go.mod` 的 indirect 依赖(lipgloss 带进来的),用前提到 require 块作为 direct 依赖即可,不引入新包。

### 4.3 Release notes 数据

```go
// releaseNotes is the right-column "What's new" content, newest first.
// Bump bannerVersion in the same commit you prepend a new entry — drift
// here is a code review red flag.
var releaseNotes = []string{
    "rebuilt fs/shell tools; tool-call slog.Debug observability hook",
    "slog default handler now honors cfg.LogLevel",
    "yaml/config.yaml untracked; yaml/CHANGELOG.md is the contract",
}
```

显示规则:取前 `maxRows`(= 3),每条按 right-column 宽度 word-wrap;首字符前置 `• `,后续 wrap 行缩进 2 空格。

### 4.4 Width-aware 分发

```go
func renderBanner(width int, modelName, cwd string) string {
    if width >= bannerMinWidth {
        return renderWelcomeCard(width, modelName, cwd)
    }
    return renderBannerCompact(modelName, cwd)
}
```

free function,显式吃 3 个参数,不挂 `*Model` receiver。

**SIGWINCH 重渲不是免费的** —— 原设计假设 "WindowSizeMsg 一到,recomputeLayout 把 banner 当 scrollback 重走 `renderMessage`,自然就拿到新 width"。实测**不成立**:`renderMessage(chatMessage{Role:"banner"})` 是 verbatim 返回(`TestRenderMessage_BannerVerbatim` 钉死的契约),banner 内容在 `freshMessages` 那一刻就冻住了,resize 不会自己刷。

修法:`handleResize` 已经有一段 "走一遍 messages,重渲 assistant 的 markdown" 的循环,把 banner 一起塞进去:

```go
for i := range m.messages {
    switch m.messages[i].Role {
    case "banner":
        m.messages[i].Content = renderBanner(m.width, m.modelName, m.cwd)
    case "assistant":
        m.messages[i].Rendered = m.renderMarkdown(m.messages[i].Content)
    }
}
```

4 行,跟 markdown 重渲同一条路径。`TestHandleResize_RebakesBannerForNewWidth` 钉死回归:width=0 启动 → 内容里没有 `╭` → resize 到 120 → 内容里有 `╭`。

首屏闪一下(compact → boxed 一帧切换)还是存在的;如果之后要消,在 `New()` 里走 `term.GetSize` 提前拿宽度,代价 1 个 syscall,但启动一次性。

### 4.5 ASCII glyph 适配

左栏可用宽度 = `leftW - 2 padding`(约 30~40 cells)。现有 `bannerASCII` 5 行,最长行 33 字符,可以塞下。检测一下:`runewidth.StringWidth(longestLine) <= leftW-4` 真才渲染 glyph,否则只显示 "Welcome back!" + model + cwd。

---

## 5. 测试要点

[banner_test.go](backend/cli/tui/banner_test.go) 改/增:

- ✅ **保留** `TestWeekdayPalette_AllSevenDaysDistinct` / `TestBannerArtStyle_UsesPaletteEntry`(weekday 逻辑没变)
- ✏️ **改** `TestRenderBanner_ContainsIdentityTokens`:
  - 删 `bannerAuthor` 断言(字段移除)
  - 删 `bannerSubtitle` 断言(段移除)
  - 加 `bannerVersion` 必须出现在 box top line 中
  - 加 model name + cwd 各出现一次
- ✏️ **改** `TestRenderMessage_BannerVerbatim`:`role:"banner"` 仍走 verbatim,内容由 `renderWelcomeCard` 生成 —— 测试逻辑不变,只是构造 model 时给出 `width=120` 走 boxed 路径
- ➕ **新增** `TestRenderWelcomeCard_BoxedAtWideWidth(width=120)`:断言输出第一行以 `╭` 开头、最后一行以 `╰` 开头,行数 == `boxHeight`(常量,如 10)
- ➕ **新增** `TestRenderBanner_CompactBelow80(width=70)`:断言不含 `╭` / `╰`(回退 compact)
- ➕ **新增** `TestReleaseNotes_NewestFirstNonEmpty`:`releaseNotes` 至少 1 条且第一条不能是空串(防 bump version 时漏写)

---

## 6. 不做(non-goals)

- **不**新增 `/release-notes` slash command(Claude Code 有,因为 notes 多;我们 3 条够看)
- **不**在 banner 里塞 token usage / cost(那是会话进行时的状态,启动屏不该显示)
- **不**从远端拉 release notes(零 I/O 启动,bundled with binary)
- **不**支持用户自定义 banner 字段(YAGNI;就一个启动屏)
- **不**为 `/help` 重写视觉(`/help` 走自己的 cmd registry,不复用 banner)

---

## 7. Todo(按 §8 渐进次序排)

```yaml
todos:
  - id: bump-version-and-notes
    content: "bannerVersion 升 1.0.1 → 1.1.0,banner_notes.go 写入 3 条 release notes"
    status: pending
  - id: write-helpers
    content: "实现 boxTitleLine + splitColumns + getRow,三者都是纯函数,单测先行"
    status: pending
  - id: render-welcome-card
    content: "renderWelcomeCard(width, modelName, cwd) 顶层入口:布局 + glyph 自适应 + dim/palette 着色"
    status: pending
  - id: width-aware-dispatch+rewire-callers
    content: "renderBanner / freshMessages 签名变 free function + width-aware 分发;同提交把 model.go / update.go / 4 个 _test.go 共 9 处调用点全量迁移(签名变更与调用点必须同提交,否则编译不过)"
    status: pending
  - id: update-tests
    content: "banner_test.go 按 §5 清单调整:删 Author/Subtitle 断言、加 boxed/compact 双路径用例 + release notes 检查 + SIGWINCH 回归"
    status: pending
  - id: verify-window-resize
    content: "见 §4.4:实测发现 banner 不会自动随 resize 重渲,在 handleResize 加 4 行 + 回归测试钉死"
    status: pending
```

---

## 8. 渐进次序

1. **bump-version-and-notes** —— 没有外部依赖,先把版本号 + notes 数据落地,后续 helper 直接引用。
2. **write-helpers** —— `boxTitleLine` / `splitColumns` / `getRow` 都是纯函数,单测先行,跟 banner 逻辑解耦。
3. **render-welcome-card** —— 用 helpers 拼装 boxed 形态。这一步只产出新函数,**不接通**到调用点,旧 banner 还在跑。
4. **width-aware-dispatch + rewire-callers**(原 4 + 5 合并)—— `renderBanner` / `freshMessages` 签名从 `()` 变 `(width, modelName, cwd)`,同提交把所有调用点(model.go / update.go / 4 个测试文件,共 9 处)一起迁。**强制合并**:签名变更跟调用点重写不能分提交,否则中间态 Go 编译不过。这一步上线 = 用户能看到变化。
5. **update-tests** —— 按 §5 改 banner_test.go,跑 `go test ./backend/cli/tui/...` 绿。
6. **verify-window-resize** —— 见 §4.4。原设计以为 SIGWINCH 会自动重渲 banner,实测不成立,要在 `handleResize` 里加 4 行外加回归测试。**这一步是代码补丁,不是手动验证**。

每一步是一个独立 commit,符合 AGENTS.md "每个 commit 的 diff 要能用一句话说清"。

---

## 9. 风险 & 兜底

- **lipgloss 不提供 inline border title** → 自己手拼一行,版本升级到提供该能力后再 swap。
- **runewidth 在某些 emoji 上算错** → release notes 文案别用 emoji,纯 ASCII + 中文字符即可(中文已经稳定占 2 cells)。
- **alt-screen 下 first paint 闪一下** → §4.4 备注;实测下来 < 16ms 切换,通常人眼不察觉。如果用户反馈闪,可以在 `New()` 里走 `term.GetSize` 提前拿到 width,绕过等 WindowSizeMsg(代价:多一个 syscall,但启动一次性的)。

---

## 10. 实施回顾(post-mortem,2026-05-12)

设计阶段跟实施阶段对得不齐的几处,留在这里方便后人查:

- **method vs free function**(§3):原设计写的是 `m.renderBanner()` / `m.freshMessages()` method。实施时按 AGENTS.md "行为住在普通顶层函数里。不挂 receiver" 改为 free function `renderBanner(width, modelName, cwd)` / `freshMessages(width, modelName, cwd)`。代价:每个调用点多写 2 个参数;收益:不需要给测试构造完整 `*Model`,test 写起来更轻。
- **runewidth 不是仓库其它地方已经在用**(§4.2):原文这个说法是错的,实测它只是 `go.mod` 里的 indirect 依赖(lipgloss 带进来),代码里没有直接调用点。提到 direct require 即可。同时 `getRow` 测宽改用 `lipgloss.Width`(对 ANSI 透明),不是 `runewidth.StringWidth`。
- **SIGWINCH 自动重渲是个谎言**(§4.4):原文断言 "WindowSizeMsg 到达后 banner 会重新走 renderMessage 自动 rebuild"。实测 `renderMessage(banner)` 是 verbatim 返回,resize 后 banner 内容根本不变。补丁在 §4.4,4 行加进 `handleResize`。`TestHandleResize_RebakesBannerForNewWidth` 钉死回归。
- **§8 step 4 / step 5 合并**:原文分两步("先加 method","再切调用点")。Go 编译的现实是签名变更跟调用点重写必须同一个 commit,否则中间态过不了 build。一个 commit。
