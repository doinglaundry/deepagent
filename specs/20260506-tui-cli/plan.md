# Implementation Plan: Bubbletea TUI CLI (helixent port)

**Feature**: `20260506-tui-cli` | **Date**: 2026-05-06
**Reference**: `/Users/bytedance/PycharmProjects/helixent` (TypeScript / Bun / Ink)

## Summary

Port the helixent CLI's interactive TUI experience to the Go LLM repo.
helixent is a Claude-Code-style chat TUI built on Ink/React: a header,
a message-history view, a streaming "thinking..." indicator, an input
box, and a footer, with the agent loop streaming completed messages
back into the UI. We rebuild the same UX in Go on top of
[Bubbletea] (the Elm-style equivalent of Ink) plus
[Lipgloss] for styling and [Glamour] for markdown rendering, reusing
the existing `eino-cli` building blocks (`config.Load`,
`eino.BuildRuntime`, `runtime.ExecuteStream`).

This is a parallel binary, not a replacement: `eino-cli` (the
existing line-oriented REPL with planner / memory / sessions / tool
approval) keeps its life. The new `eino-tui` is a focused chat client
that demonstrates the helixent UX pattern.

[Bubbletea]: https://github.com/charmbracelet/bubbletea
[Lipgloss]: https://github.com/charmbracelet/lipgloss
[Glamour]: https://github.com/charmbracelet/glamour

## helixent — Architecture in detail

helixent is structured in three layers plus a TUI layer.

### Layer 1 — Foundation (`src/foundation`)

- `Model` — abstraction over a chat-completion provider with
  `stream(ModelContext)` returning an async iterable of
  `AssistantMessage` snapshots (each snapshot carries the full
  message-so-far, with a `streaming: true` flag while the stream is
  in progress).
- `Message` types: `UserMessage`, `AssistantMessage`,
  `ToolMessage`, plus `NonSystemMessage = User | Assistant | Tool`.
  `AssistantMessage.content[]` is a discriminated union of `text`,
  `thinking`, and `tool_use` blocks.
- `Tool` — name, description, input schema, `invoke(input, signal)`.

### Layer 2 — Agent loop (`src/agent`)

The `Agent` class drives the ReAct loop. Its public surface is

```ts
async *stream(message: UserMessage): AsyncGenerator<AgentEvent>
abort(): void
clearMessages(): void
```

`AgentEvent` is one of:

- `{ type: "message", message }` — a fully-completed assistant or
  tool message has just been appended to the transcript.
- `{ type: "progress", subtype: "thinking" }` — the model is
  streaming text/thinking but no `tool_use` has appeared yet.
- `{ type: "progress", subtype: "tool", name, input }` — a tool_use
  is forming; `input` may be a partial JSON value.

Internal flow per loop iteration:

1. `_beforeAgentStep`
2. `_think()` — calls `model.stream()`, yields progress snapshots,
   collects the final assistant message, runs `afterModel`.
3. `yield { type: "message", message: assistantMessage }`.
4. If no tool_use → `afterAgentRun` and return.
5. Otherwise `_act(toolUses)` — runs tools concurrently,
   yielding one `{type: "message", message: toolMessage}` per
   completed tool result.
6. `afterAgentStep`.

Eight middleware hooks let callers extend behaviour:
`beforeAgentRun`, `afterAgentRun`, `beforeAgentStep`,
`afterAgentStep`, `beforeModel`, `afterModel`, `beforeToolUse`,
`afterToolUse`.

### Layer 3 — Coding agent (`src/coding`)

Pre-wires a domain agent with coding tools (`read_file`,
`write_file`, `str_replace`, `bash`, `list_files`, `glob_search`,
`grep_search`, `apply_patch`, `file_info`, `mkdir`, `move_path`),
the skills middleware, the todo middleware, and an HITL approval
middleware backed by a `globalApprovalManager`.

### TUI layer (`src/cli/tui`)

Built on [Ink] (a React renderer for the terminal).

Top-level layout (`app.tsx`):

```
+------------------------------------+
|  Header  (only when messages == 0) |
|                                    |
|  Last message (UserMsg / AsstMsg)  |
|                                    |
|  StreamingIndicator                |
|                                    |
|  TodoPanel                         |
|                                    |
|  InputBox  | ApprovalPrompt        |
|            | AskUserQuestionPrompt |
|                                    |
|  Footer                            |
+------------------------------------+
```

Key rendering choices:

- **Only the last message is rendered live.** Older messages are
  flushed into the terminal scrollback with `useStdout().write()`
  via the `useFlushToScrollback` effect so the React tree stays
  small and re-renders are cheap.
- **Streaming text is NOT shown chunk-by-chunk.** During streaming
  the user sees only the animated `StreamingIndicator` (spinner +
  shimmer over a "Thinking..." string + the next-todo line below).
  When the assistant turn completes, the full message snaps into
  history.
- **Slash commands** are picked up live as the user types: typing
  `/` triggers a filtered `CommandList` picker overlay; arrow-keys
  navigate, Enter/Tab inserts the chosen command, Esc dismisses.
- **HITL** is inline: `ApprovalPrompt` and `AskUserQuestionPrompt`
  *replace* the InputBox while a tool needs approval / questions
  need answers.
- **TodoPanel** replays the latest `todo_write` snapshot.

State management (`use-agent-loop.ts`):

```ts
const [streaming, setStreaming] = useState(false);
const [messages, setMessages] = useState<NonSystemMessage[]>([]);

onSubmit = async (submission) => {
  // built-ins: /exit /quit -> process.exit, /clear -> clearMessages,
  //            /help -> append synthetic help message.
  setStreaming(true);
  setMessages(prev => [...prev, userMessage]);
  for await (const event of agent.stream(userMessage)) {
    if (event.type === "message") enqueueMessage(event.message);
  }
  setStreaming(false);
};
```

Messages are enqueued and **batched on a 50 ms timer** before
calling `setMessages` so a burst of tool results doesn't trigger a
storm of re-renders.

The InputBox uses a custom editor (`use-command-input.ts`) with
cursor / word movement, history navigation (up/down arrows when
the input is empty), and the slash picker. Ctrl-C / Esc → `abort()`
on the agent (cancels the in-flight model request via
`AbortController`).

### Entry point (`src/cli/index.tsx`)

```ts
const args = process.argv.slice(2);
if (args.length > 0) {
  await program.parseAsync(process.argv);   // commander subcommands
} else {
  // first-run wizard, load config, build provider+model+agent,
  // load slash commands from skills dirs, render <App>.
}
```

Subcommands cover model config (`config model add/list/remove/set-default`).
The default no-arg invocation drops into the TUI.

## Mapping to Go (LLM repo)

| helixent piece                     | Go equivalent                                                      |
|------------------------------------|--------------------------------------------------------------------|
| Ink (React terminal renderer)      | [Bubbletea] (Elm-style TEA loop)                                   |
| Ink `<Box>` / `<Text>` styling     | [Lipgloss] styles                                                  |
| `react-markdown`                   | [Glamour] (`glamour.NewTermRenderer`)                              |
| `bubbles/spinner` for animation    | [Bubbles] spinner                                                  |
| `bubbles/textinput`                | single-line input (v1); custom editor in v2                        |
| `bubbles/viewport`                 | scrollable history pane                                            |
| `agent.stream()` event iterator    | Goroutine bridges `runtime.ExecuteStream` chunks → tea.Cmd channel |
| `AbortController`                  | `context.WithCancel` plus a tea.Cmd that cancels the active run    |
| commander.js                       | argv flag parsing (`flag` stdlib) — TUI-only for v1                |
| `useAgentLoop` React context       | bubbletea `Model` struct (single source of truth)                  |
| `useFlushToScrollback`             | bubbletea's `viewport` already manages off-screen rendering        |
| 50 ms message batch                | not needed — bubbletea's message loop is naturally batched         |
| middleware hooks                   | already implemented under `backend/agent/middlewares/*.go`         |
| `createCodingAgent`                | already implemented as `agent.MakeLeadAgent`                       |
| `loadConfig` + provider wiring     | already implemented as `config.Load` + `eino.BuildRuntime`         |

[Bubbles]: https://github.com/charmbracelet/bubbles

### What we reuse from the LLM repo

- `config.Load()` — YAML config + env-var resolution.
- `eino.BuildRuntime(ctx, cfg)` — wires DeepAgent + middlewares
  + checkpoint store and returns a `Runtime` interface.
- `runtime.ExecuteStream(ctx, prompt, onChunk)` — the streaming
  entry point. It already calls back per chunk and returns a
  `Result{Output, Success, Code, Message, NeedsUser}`.
- `runtime.ClearHistory()` — used by `/clear`.
- `runtime.Name()` — model name for the header / footer.

The new TUI does **not** route through `backend/cli/repl/repl.go`
(the line-oriented REPL with planner / memory / sessions). That
REPL stays unchanged. Both binaries share the runtime layer.

### Streaming UX choice for v1: live chunks

helixent shows a spinner *only* during streaming and reveals the
finished message in one frame. We do something **slightly richer**
for v1: we render the partial assistant text live as chunks arrive
(below the spinner, as plain text), and re-render it as markdown
once the stream completes. Rationale:

1. The eino runtime already passes a chunk callback — wiring it
   into the TUI is one extra `tea.Msg` type, almost free.
2. Live chunks give a much better "the model is working" signal
   than a spinner alone, especially on slower providers.
3. Glamour can re-render the final text as markdown when the
   stream finishes, so we don't lose the rich-text experience.

If this turns out to feel chatty, switching back to spinner-only
is one `if m.streaming { return spinner }` change in `view.go`.

## Project structure

```text
specs/20260506-tui-cli/
└── plan.md                     # this file

cmd/
├── cmd.go                      # existing REPL entry (untouched)
└── tui/
    └── main.go                 # NEW: tui binary entry

backend/cli/
├── render/                     # existing (untouched)
├── repl/                       # existing line REPL (untouched)
├── router/                     # existing (untouched)
├── status/                     # existing (untouched)
├── taskview/                   # existing (untouched)
└── tui/                        # NEW: bubbletea-based TUI
    ├── model.go                #   - Model struct, Init
    ├── update.go               #   - Update (key/window/stream messages)
    ├── view.go                 #   - View (header / history / spinner /
    │                           #          input / footer composition)
    ├── stream.go               #   - tea.Cmd that runs ExecuteStream and
    │                           #          forwards chunks via channel
    └── styles.go               #   - lipgloss styles (theme)
```

## Scope

### v1 — this PR

- `cmd/tui/main.go` binary that wires `config.Load` →
  `eino.BuildRuntime` → `tea.Program` and runs in alt-screen mode.
- Header: logo (the existing `eino-cli` text + version-ish suffix),
  resolved model name, working directory.
- Scrollable history viewport (Bubbles `viewport.Model`) with
  per-role rendering: `❯ user message`, `⏺ assistant message`
  (markdown via Glamour).
- Live streaming text panel below the spinner (chunk-by-chunk).
- Animated spinner (Bubbles `spinner.Spinner`) while streaming.
- Single-line input box (Bubbles `textinput.Model`).
- Built-in slash commands: `/exit`, `/quit`, `/clear`.
- Ctrl-C: abort current stream (cancel context); from idle state,
  Ctrl-C twice quits.
- Footer: model name, idle/streaming hint.

### v2+ — deferred

- Multi-line editor with cursor / word movement / history.
- Slash command autocomplete picker with skill discovery.
- Inline `ApprovalPrompt` / `AskUserQuestionPrompt` HITL flows
  (the runtime already supports HITL via `RuntimeExtras`; v2
  routes its callbacks through tea.Msg instead of the current
  stdin scanner).
- TodoPanel sourced from `todo_write` tool calls.
- Per-tool-use rendering (current `ToolUseContentItem` variants).
- Input history (up-arrow when empty).
- First-run model wizard (`config model add` flow).

## Risks & mitigations

- **bubbletea + glamour are large dep additions.** Acceptable —
  TUIs are the point of this work and these are the standard
  Go libraries (Charm).
- **Streaming chunk pacing on fast providers may flicker.**
  Mitigation: bubbletea coalesces messages naturally; if it
  becomes an issue, throttle in `stream.go` with a 50 ms timer
  mirroring helixent's batch.
- **Glamour markdown rendering can re-flow on resize.** v1 just
  re-renders on `tea.WindowSizeMsg` — acceptable for chat-length
  text.
