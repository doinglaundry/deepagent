### Goal

把 DeerFlow 文档里的 Runtime/Run 执行层收敛到当前 LLM 仓库的 CLI 路径：只服务 `backend/cli/tui`，不改 gateway，不改 agent harness。

当前 CLI 一次用户输入的路径是：

```38:57:backend/cli/tui/stream.go
func startStream(rt eino.Runtime, prompt string) (<-chan tea.Msg, context.CancelFunc) {
	streamCh := make(chan tea.Msg, 64)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = middlewares.WithTraceConsumer(ctx, streamConsumer{ctx: ctx, ch: streamCh})

	go func() {
		defer close(streamCh)
		result, err := rt.ExecuteStream(ctx, prompt, func(chunk string) {
			select {
			case streamCh <- chunkMsg(chunk):
			case <-ctx.Done():
			}
		})
		if err != nil {
			streamCh <- doneMsg{err: err}
			return
		}
		streamCh <- doneMsg{output: result.Output}
	}()
	return streamCh, cancel
}
```

`startStream` 现在同时负责 context、trace consumer、goroutine、chunk 转发、done/error 转换。`DeepAgentRuntime.ExecuteStream` 同时负责裁剪 history、构造 messages、调用 ADK runner、收集事件、保存 history：

```62:106:backend/runtime/eino/deep_runtime.go
func (r *DeepAgentRuntime) ExecuteStream(ctx context.Context, prompt string, onChunk StreamChunkHandler) (Result, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Result{}, fmt.Errorf("prompt is required")
	}

	r.mu.Lock()
	if len(r.history) > r.maxHistoryTurns*2 {
		r.history = r.history[len(r.history)-r.maxHistoryTurns*2:]
	}

	messages := make([]*schema.Message, len(r.history)+1)
	copy(messages, r.history)
	messages[len(messages)-1] = schema.UserMessage(prompt)
	runner := r.runner
	r.mu.Unlock()

	// ... existing code ...
	return SuccessResult(summary.Output), nil
}
```

目标不是替换这两层，而是在 CLI 外围补一层轻量 run 调度：每次 submit 有 `runID`、状态、取消函数和统一事件流。Agent 构建、middleware、tool 注册继续留在 `agent.MakeLeadAgent`、`GetChatModelMiddlewares`、`tools.BuildBuiltinTools`。这个边界对应 `AGENTS.md` 的 "Surgical changes" 和 "Behavior lives in plain top-level functions"：只把当前混在 `startStream` 里的执行生命周期抽出来，不顺手重写 agent。

预期结果：

- `backend/cli/tui/stream.go` 只负责把 runtime run event 转回现有 `tea.Msg`。
- `backend/runtime/eino` 新增 CLI run 执行层，封装 run 状态、取消、trace/chunk/done/error/end 的有序事件。
- TUI 外部行为不变：仍接收 `chunkMsg`、`doneMsg`、`middlewares.TraceEvent`。
- 不实现 gateway、不实现 HTTP SSE、不实现持久化 run store、不实现 rollback。

### Implementation

新增文件建议：

- `backend/runtime/eino/run.go`：run 状态、事件类型、内存 record、CLI 单 in-flight run 管理、worker。
- `backend/runtime/eino/run_test.go`：验证事件顺序和 in-flight 拒绝。
- 修改 `backend/cli/tui/stream.go`：`startStream` 调新 run API，然后消费事件转成现有 `tea.Msg`。

`RunRecord` 只保留同一次执行必须一起流转的数据，符合 `AGENTS.md` 的 "Structs hold data. Functions hold behavior."：

```go
package eino

import (
	"context"
	"time"
)

type RunStatus string

const (
	RunPending     RunStatus = "pending"
	RunRunning     RunStatus = "running"
	RunSuccess     RunStatus = "success"
	RunError       RunStatus = "error"
	RunInterrupted RunStatus = "interrupted"
)

type RunRecord struct {
	ID        string
	Status    RunStatus
	CreatedAt time.Time
	UpdatedAt time.Time
	Cancel    context.CancelFunc
	Output    string
	Err       error
}
```

事件类型只表达 CLI 需要的输出，不引入 DeerFlow 的 stream modes：

```go
package eino

import "eino-cli/backend/agent/middlewares"

type RunEventType string

const (
	RunEventMetadata RunEventType = "metadata"
	RunEventChunk    RunEventType = "chunk"
	RunEventTrace    RunEventType = "trace"
	RunEventDone     RunEventType = "done"
	RunEventError    RunEventType = "error"
	RunEventEnd      RunEventType = "end"
)

type RunEvent struct {
	RunID string
	Type  RunEventType
	Chunk string
	Trace *middlewares.TraceEvent
	Output string
	Err error
}
```

管理器首期只支持 CLI 当前 run；用户再次提交或 ESC 都走同一个 cancel。这里用函数而不是给所有行为都加 receiver，是为了 "Push less stack"：

```go
package eino

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type RunManager struct {
	mu      sync.Mutex
	current *RunRecord
}

func NewRunManager() *RunManager {
	return &RunManager{}
}

func createRun(ctx context.Context, mgr *RunManager) (*RunRecord, context.Context, error) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if mgr.current != nil && (mgr.current.Status == RunPending || mgr.current.Status == RunRunning) {
		return nil, nil, fmt.Errorf("run already in progress")
	}

	runCtx, cancel := context.WithCancel(ctx)
	now := time.Now()
	run := &RunRecord{
		ID:        fmt.Sprintf("run-%d", now.UnixNano()),
		Status:    RunPending,
		CreatedAt: now,
		UpdatedAt: now,
		Cancel:    cancel,
	}
	mgr.current = run
	return run, runCtx, nil
}

func cancelRun(mgr *RunManager) {
	mgr.mu.Lock()
	run := mgr.current
	mgr.mu.Unlock()
	if run != nil && run.Cancel != nil {
		run.Cancel()
	}
}

func finishRun(mgr *RunManager, run *RunRecord, status RunStatus, output string, err error) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	run.Status = status
	run.Output = output
	run.Err = err
	run.UpdatedAt = time.Now()
}
```

事件桥用一个 channel 保证 trace、chunk、done 的顺序和当前 TUI 一致。不做 replay、heartbeat、SSE，避免把 gateway 设计带进 CLI：

```go
package eino

import "context"

type runEventBridge struct {
	ch chan RunEvent
}

func newRunEventBridge(buffer int) *runEventBridge {
	return &runEventBridge{ch: make(chan RunEvent, buffer)}
}

func publishRunEvent(ctx context.Context, bridge *runEventBridge, ev RunEvent) bool {
	select {
	case bridge.ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func closeRunEvents(bridge *runEventBridge) {
	close(bridge.ch)
}
```

worker 复用现有 `Runtime.ExecuteStream`。`TraceConsumer` 在这里挂入 context，`onChunk` 也写入同一条 run event 队列：

```go
package eino

import (
	"context"

	"eino-cli/backend/agent/middlewares"
)

type runTraceConsumer struct {
	ctx    context.Context
	runID  string
	bridge *runEventBridge
}

func (c runTraceConsumer) Send(ev middlewares.TraceEvent) {
	publishRunEvent(c.ctx, c.bridge, RunEvent{
		RunID: c.runID,
		Type:  RunEventTrace,
		Trace: &ev,
	})
}

func startRunWorker(ctx context.Context, rt Runtime, prompt string, mgr *RunManager, run *RunRecord, bridge *runEventBridge) {
	defer closeRunEvents(bridge)

	markRunRunning(mgr, run)
	ctx = middlewares.WithTraceConsumer(ctx, runTraceConsumer{ctx: ctx, runID: run.ID, bridge: bridge})
	publishRunEvent(ctx, bridge, RunEvent{RunID: run.ID, Type: RunEventMetadata})

	result, err := rt.ExecuteStream(ctx, prompt, func(chunk string) {
		publishRunEvent(ctx, bridge, RunEvent{RunID: run.ID, Type: RunEventChunk, Chunk: chunk})
	})
	if err != nil {
		finishRun(mgr, run, RunError, "", err)
		publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventError, Err: err})
		publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventEnd})
		return
	}
	if ctx.Err() != nil || result.NeedsUser {
		finishRun(mgr, run, RunInterrupted, result.Output, ctx.Err())
		publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventDone, Output: result.Output})
		publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventEnd})
		return
	}

	finishRun(mgr, run, RunSuccess, result.Output, nil)
	publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventDone, Output: result.Output})
	publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventEnd})
}
```

结束事件的约定：每个 run 的事件流都必须以 `RunEventEnd` 作为最后一个业务事件，然后关闭 channel。`close(events)` 只是 Go channel 的传输结束信号，不表达 run 协议；`RunEventEnd` 才是上层消费者可以依赖的生命周期事件。三条路径的顺序是：

```text
success:     metadata -> chunk* -> trace* -> done -> end -> close
interrupted: metadata -> chunk* -> trace* -> done -> end -> close
error:       metadata -> chunk* -> trace* -> error -> end -> close
```

TUI 当前不需要处理 `RunEventEnd`，因为 `doneMsg` / `error` 已经足够驱动画面收尾；但 runtime 层仍然发布 `end`，为后续 `RunEventStore` 或 CLI 事件录制留下稳定协议。

`run_test.go` 需要覆盖 success 和 error 两条事件序列。error 测试只验证 runtime event 协议，不涉及 TUI：

```go
package eino

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestStartRunPublishesErrorThenEnd(t *testing.T) {
	wantErr := errors.New("boom")
	rt := runTestRuntime{run: func(context.Context, string, StreamChunkHandler) (Result, error) {
		return Result{}, wantErr
	}}
	mgr := NewRunManager()

	events, _, err := StartRun(context.Background(), rt, "hi", mgr)
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	var got []RunEventType
	var runErr error
	for ev := range events {
		got = append(got, ev.Type)
		if ev.Type == RunEventError {
			runErr = ev.Err
		}
	}

	want := []RunEventType{RunEventMetadata, RunEventError, RunEventEnd}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !errors.Is(runErr, wantErr) {
		t.Fatalf("error = %v, want %v", runErr, wantErr)
	}
	if mgr.current == nil || mgr.current.Status != RunError {
		t.Fatalf("run status = %#v, want error", mgr.current)
	}
}
```

`stream.go` 保持 Bubbletea 边界不变，把 run event 转回旧消息类型。这里的函数逻辑清晰，不需要注释：

```go
package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"eino-cli/backend/runtime/eino"
)

func startStream(rt eino.Runtime, prompt string, runs *eino.RunManager) (<-chan tea.Msg, context.CancelFunc) {
	streamCh := make(chan tea.Msg, 64)
	events, cancel, err := eino.StartRun(context.Background(), rt, prompt, runs)
	if err != nil {
		streamCh <- doneMsg{err: err}
		close(streamCh)
		return streamCh, func() {}
	}

	go consumeRunEvents(streamCh, events)
	return streamCh, cancel
}

func consumeRunEvents(streamCh chan<- tea.Msg, events <-chan eino.RunEvent) {
	defer close(streamCh)
	for ev := range events {
		switch ev.Type {
		case eino.RunEventChunk:
			streamCh <- chunkMsg(ev.Chunk)
		case eino.RunEventTrace:
			if ev.Trace != nil {
				streamCh <- *ev.Trace
			}
		case eino.RunEventDone:
			streamCh <- doneMsg{output: ev.Output}
		case eino.RunEventError:
			streamCh <- doneMsg{err: ev.Err}
		}
	}
}
```

实际实现时，导出的函数名可以按包边界调整：TUI 需要调用的函数导出，包内 helper 保持小写。简单字段、简单分支不加注释；只有 run 层边界和取消语义需要一行说明。

### Tradeoffs

设计选择：先 CLI 后 gateway。依据 `AGENTS.md` 的 "Surgical changes"，本阶段只处理用户要求的 CLI 执行链路，不碰 [backend/gateway/handlers.go](backend/gateway/handlers.go)。副作用是 HTTP 模式仍然没有 `run_id`、run status、断线继续执行。软回滚不需要配置开关；硬回滚是删除新增的 `backend/runtime/eino/run_*.go`，并把 [backend/cli/tui/stream.go](backend/cli/tui/stream.go) 恢复到直接调用 `ExecuteStream`。

设计选择：不把 DeerFlow 的 `StreamBridge` 原样搬过来。依据 `AGENTS.md` 的 "Pass less data"，CLI 只需要有序事件 channel，不需要 SSE event id、heartbeat、Last-Event-ID、replay buffer。副作用是断线重连和事件回放仍不可用，但 CLI 本来也没有这个协议面。

设计选择：首期不做 rollback。当前 [backend/runtime/eino/deep_runtime.go](backend/runtime/eino/deep_runtime.go) 的 `history` 是内存字段，checkpoint 由 [backend/session/checkpoint/store.go](backend/session/checkpoint/store.go) 按 `checkpointID` 写 blob；DeerFlow 的 rollback 依赖 thread checkpoint 快照。强行恢复 `pendingCheckpointID` 会让 `history`、session todos、sandbox thread data 不一致。硬回滚无需额外动作，因为本阶段不写 rollback 代码。

设计选择：保留 `DeepAgentRuntime.ExecuteStream` 作为执行核心。依据 `AGENTS.md` 的 "Behavior lives in plain top-level functions"，run worker 只编排生命周期，不重写 `adk.Runner.Run`、middleware 链或工具链。副作用是 run status 只能感知 `ExecuteStream` 返回的 success/error/interrupted，暂时拿不到更细的 ADK node-level 状态。

设计选择：文档草案中的函数尽量不用注释。符合本仓库规则“如果代码简单并且读起来清楚，跳过注释”。实际实现时只有包级边界、取消语义、checkpoint/rollback 的限制需要保留短注释；字段赋值、switch 分支、channel 转发不加说明性注释。
