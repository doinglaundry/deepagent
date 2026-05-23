### Goal

修复 `ask_clarification` tool call 触发后 CLI 显示 `deep runtime returned empty output` 的问题。现象来自 `.eino-cli/agent-messages.md`：模型最后一轮只返回了 `ask_clarification` tool call，没有普通 assistant 文本。

当前 runtime 只收集“assistant 且没有 tool calls”的消息：

```48:60:backend/runtime/deepagent/events.go
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil || msg == nil {
			continue
		}
		if msg.Role != schema.Assistant || len(msg.ToolCalls) > 0 {
			continue
		}

		if onChunk != nil && msg.Content != "" {
			onChunk(msg.Content)
		}
		if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
			outputs = append(outputs, trimmed)
		}
```

当 collector 没拿到普通文本时，`ExecuteStream` 把这次 clarification 误判成空输出错误：

```88:97:backend/runtime/deepagent/runtime.go
	if summary.Interrupted {
		r.mu.Lock()
		r.pendingCheckpointID = checkpointID
		r.mu.Unlock()
		return rt.Result{Success: false, Code: rt.ErrorCodeRuntime, Message: "execution interrupted", NeedsUser: true}, nil
	}

	if strings.TrimSpace(summary.Output) == "" {
		return rt.Result{}, fmt.Errorf("deep runtime returned empty output")
	}
```

`Clarification` middleware 已经尝试把 `ask_clarification` 改写成普通 assistant 内容，但当前事件收集看到的仍可能是改写前的 tool-call event：

```42:85:backend/agent/middlewares/clarification.go
func (m *Clarification) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	last := state.Messages[len(state.Messages)-1]
	if last == nil || last.Role != schema.Assistant || len(last.ToolCalls) == 0 {
		return ctx, state, nil
	}

	var question string
	found := false
	for _, call := range last.ToolCalls {
		if call.Function.Name == AskClarificationToolName {
			question = parseClarificationArgs(call.Function.Arguments)
			found = true
			break
		}
	}
	if !found {
		return ctx, state, nil
	}

	// ... existing code ...
	last.ToolCalls = nil
	last.Content = display

	return ctx, state, nil
}
```

预期结果：

- 模型调用 `ask_clarification` 时，CLI 展示 clarification 问题，不再报 `deep runtime returned empty output`。
- 普通工具调用仍不作为最终输出。
- 普通 assistant 输出的收集逻辑不变。

### Implementation

修改 `backend/runtime/deepagent/events.go`，在 collector 里识别 `ask_clarification` tool call。按 `AGENTS.md` 的 "Behavior lives in plain top-level functions"：解析逻辑放在本文件的 top-level helper，不引入新的 middleware 状态或依赖注入。

新增两个 helper：

```go
func getClarificationOutput(msg *schema.Message) string {
	if msg == nil || msg.Role != schema.Assistant {
		return ""
	}
	for _, call := range msg.ToolCalls {
		if call.Function.Name == consts.AskClarificationToolName {
			return parseClarificationOutput(call.Function.Arguments)
		}
	}
	return ""
}

func parseClarificationOutput(raw string) string {
	var args struct {
		Question string   `json:"question"`
		Prompt   string   `json:"prompt"`
		Message  string   `json:"message"`
		Context  string   `json:"context"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return fmt.Sprintf("(unparsed clarification args: %s)", raw)
	}
	question := strings.TrimSpace(args.Question)
	if question == "" {
		question = strings.TrimSpace(args.Prompt)
	}
	if question == "" {
		question = strings.TrimSpace(args.Message)
	}
	if question == "" {
		return strings.TrimSpace(raw)
	}
	var parts []string
	if context := strings.TrimSpace(args.Context); context != "" {
		parts = append(parts, context, "")
	}
	parts = append(parts, question)
	for i, option := range args.Options {
		if option = strings.TrimSpace(option); option != "" {
			parts = append(parts, fmt.Sprintf("%d. %s", i+1, option))
		}
	}
	return strings.Join(parts, "\n")
}
```

然后在 `collectAgentEventsWithSink` 的 tool-call 分支前处理 clarification：

```go
		if msg.Role != schema.Assistant {
			continue
		}
		if clarification := getClarificationOutput(msg); clarification != "" {
			if onChunk != nil {
				onChunk(clarification)
			}
			outputs = append(outputs, clarification)
			continue
		}
		if len(msg.ToolCalls) > 0 {
			continue
		}
```

测试放在 `backend/runtime/deepagent/runtime_test.go`。复用已有 `adk.NewAsyncIteratorPair` 事件测试风格，新增：

```go
func TestCollectAgentEventsUsesClarificationToolCall(t *testing.T) {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	msg := schema.AssistantMessage("", nil)
	msg.ToolCalls = []schema.ToolCall{{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name: consts.AskClarificationToolName,
			Arguments: `{
				"question":"Which environment should I deploy to?",
				"context":"I need the target environment.",
				"options":["development","staging","production"]
			}`,
		},
	}}
	gen.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{Message: msg}}})
	gen.Close()

	summary, err := collectAgentEventsWithSink(iter, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "I need the target environment.\n\nWhich environment should I deploy to?\n1. development\n2. staging\n3. production"
	if summary.Output != want {
		t.Fatalf("clarification output:\ngot:  %q\nwant: %q", summary.Output, want)
	}
}
```

Verification:

```bash
go test ./backend/runtime/deepagent ./backend/agent/middlewares
go test ./backend/...
```

### Tradeoffs

Design choice: duplicate the small clarification-argument formatter in `events.go` instead of exporting `parseClarificationArgs` from `backend/agent/middlewares`. This follows `AGENTS.md` "Push less stack": runtime event collection should not import a middleware package just to format one terminal output path.

Side effects:

- `backend/runtime/deepagent/events.go` gains one import of `encoding/json`, `fmt`, and `eino-cli/backend/consts`.
- `backend/runtime/deepagent/runtime_test.go` gains 1 collector test.
- `backend/agent/middlewares/clarification_test.go` remains valid; middleware rewrite behavior is unchanged.
- `agent-messages.md` will still record the raw tool call; only CLI runtime output changes.

Rollback:

- Hard rollback: revert the helper functions and collector branch in `backend/runtime/deepagent/events.go`; remove `TestCollectAgentEventsUsesClarificationToolCall` from `backend/runtime/deepagent/runtime_test.go`.
