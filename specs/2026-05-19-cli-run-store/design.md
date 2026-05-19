### Goal

第一阶段（[specs/2026-05-19-cli-runtime-run/design.md](../2026-05-19-cli-runtime-run/design.md)）建立了 CLI 的 run 生命周期与有序事件流，但 run 完成后 `RunRecord` 只活在内存里：进程退出后无任何痕迹，无法做 run 历史、postmortem、`/history` 命令、未来的 resume。

本阶段只补一件事：把已完成的 `RunRecord` 落到磁盘。范围严格限定为 **RunStore**，不动事件、不做 rollback。

当前状态：

```103:111:backend/runtime/eino/run.go
func finishRun(mgr *RunManager, run *RunRecord, status RunStatus, output string, err error) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	run.Status = status
	run.Output = output
	run.Err = err
	run.UpdatedAt = time.Now()
}
```

`finishRun` 在三条终止路径（success / interrupted / error）都会被调用，已经是 run 状态收敛的唯一点 —— 持久化只需要在这之后挂一个 sink，不引入新的状态机分叉。

预期结果：

- 新增 `backend/session/runs/store.go`，与 [backend/session/checkpoint/store.go](backend/session/checkpoint/store.go) 同形态：单一职责、文件存储、API 三件套（`Save / Get / List`）。
- `RunManager` 拿到一个可选 `RunStore`；为 nil 时退化为内存模式（兼容现有测试）。
- worker 在 `finishRun` 之后、`publishRunEvent(end)` 之前同步写盘；写失败只 log 不影响 run 协议。
- 落盘路径：`<RootDir>/.eino-cli/runs/<run_id>.json`，与 checkpoint 目录同根，便于 `/clear` 之类的清理。
- 不实现：`RunEventStore`（事件 JSONL）、rollback、`/history` slash 命令、跨进程 RunManager 状态恢复。这些是后续 spec。

### Implementation

新增文件：

- `backend/session/runs/store.go`：`Store`、`Record`、`Save`、`Get`、`List`。
- `backend/session/runs/store_test.go`：Save→Get 往返、List 排序、Get not-found 不报错。

修改文件：

- `backend/runtime/eino/run.go`：`RunManager` 增加 `store *runs.Store` 字段；`NewRunManager` 改签名或新增 `NewRunManagerWithStore`；`finishRun` 后写盘。
- `backend/cli/tui/model.go`：构造 manager 时传入 store，store 目录从 `cfg.RootDir` 派生。

`Record` 是 wire 格式，与运行时 `RunRecord` 解耦。运行时结构带 `context.CancelFunc` 和 `error`，这两个都不能直接 JSON。字段集覆盖未来 `/history` 一行能展示的所有信息（who / how long / how much）：

```go
package runs

import "time"

type Record struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Prompt     string    `json:"prompt,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Output     string    `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
	Tokens     int64     `json:"tokens,omitempty"`
}
```

`DurationMS` 用毫秒整数而不是 `time.Duration`：后者 JSON marshal 成纳秒整数，人读不出来。`Tokens` 是累计 total tokens（input+output），来源是 `middlewares.TokenUsageStats.TotalTokens`，与 TUI footer 已经在用的 `m.tokenTotal` 同义，避免引入第二种 token 口径。

Store 沿用 checkpoint 的形态。`Save` 用临时文件 + `os.Rename` 做原子替换，避免 Ctrl-C 留下半截 JSON：

```go
package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Save(_ context.Context, rec Record) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create runs directory: %w", err)
	}
	payload, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run %s: %w", rec.ID, err)
	}
	final := s.path(rec.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return fmt.Errorf("write run %s: %w", rec.ID, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("rename run %s: %w", rec.ID, err)
	}
	return nil
}

func (s *Store) Get(_ context.Context, id string) (Record, bool, error) {
	payload, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("read run %s: %w", id, err)
	}
	var rec Record
	if err := json.Unmarshal(payload, &rec); err != nil {
		return Record{}, false, fmt.Errorf("decode run %s: %w", id, err)
	}
	return rec, true, nil
}

func (s *Store) List(_ context.Context) ([]Record, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read runs directory: %w", err)
	}
	records := make([]Record, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		rec, ok, err := s.Get(nil, id)
		if err != nil || !ok {
			continue
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	return records, nil
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}
```

`RunRecord` 增加两个字段以便落盘时不丢信息：`Prompt`（创建时写入）和 `TotalTokens`（运行中通过 trace consumer 累加）。`RunManager` 加一个可选 store；保留 `NewRunManager()` 零参构造（已有 `run_test.go` 依赖）：

```go
type RunRecord struct {
	ID          string
	Prompt      string
	Status      RunStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Cancel      context.CancelFunc
	Output      string
	Err         error
	TotalTokens int64
}

type RunManager struct {
	mu      sync.Mutex
	current *RunRecord
	store   *runs.Store
}

func NewRunManager() *RunManager { return &RunManager{} }

func NewRunManagerWithStore(store *runs.Store) *RunManager {
	return &RunManager{store: store}
}
```

`createRun` 签名增加 `prompt`；`StartRun` 直接透传：

```go
func createRun(ctx context.Context, mgr *RunManager, prompt string) (*RunRecord, context.Context, error) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if mgr.current != nil && isRunInFlight(mgr.current.Status) {
		return nil, nil, fmt.Errorf("run already in progress")
	}

	runCtx, cancel := context.WithCancel(ctx)
	now := time.Now()
	run := &RunRecord{
		ID:        fmt.Sprintf("run-%d", now.UnixNano()),
		Prompt:    prompt,
		Status:    RunPending,
		CreatedAt: now,
		UpdatedAt: now,
		Cancel:    cancel,
	}
	mgr.current = run
	return run, runCtx, nil
}
```

Token 累加点：`runTraceConsumer.Send` 已经能拿到所有 `TraceEvent`，在 `TracePhaseTokens` 分支额外写一次 `run.TotalTokens`。这复用了 phase-1 已经建立的 trace bridge，不引入新的事件通路：

```go
type runTraceConsumer struct {
	ctx    context.Context
	mgr    *RunManager
	run    *RunRecord
	bridge *runEventBridge
}

func (c runTraceConsumer) Send(ev middlewares.TraceEvent) {
	publishRunEvent(c.ctx, c.bridge, RunEvent{
		RunID: c.run.ID,
		Type:  RunEventTrace,
		Trace: &ev,
	})
	if ev.Phase == middlewares.TracePhaseTokens && ev.Tokens != nil {
		updateRunTokens(c.mgr, c.run, ev.Tokens.TotalTokens)
	}
}

func updateRunTokens(mgr *RunManager, run *RunRecord, total int64) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	run.TotalTokens = total
}
```

`worker` 注入 consumer 时需要传入 mgr 和 run（取代之前只传 `runID`）：

```go
ctx = middlewares.WithTraceConsumer(ctx, runTraceConsumer{
	ctx:    ctx,
	mgr:    mgr,
	run:    run,
	bridge: bridge,
})
```

写盘点放在 `finishRun` 内部，让所有终止路径共享同一行为；写盘错误只 log，不返回，避免污染 run 协议：

```go
func finishRun(mgr *RunManager, run *RunRecord, status RunStatus, output string, err error) {
	mgr.mu.Lock()
	run.Status = status
	run.Output = output
	run.Err = err
	run.UpdatedAt = time.Now()
	store := mgr.store
	snapshot := toRecord(run)
	mgr.mu.Unlock()

	if store == nil {
		return
	}
	if saveErr := store.Save(context.Background(), snapshot); saveErr != nil {
		slog.Warn("run store: save failed", "run_id", run.ID, "err", saveErr)
	}
}

func toRecord(run *RunRecord) runs.Record {
	rec := runs.Record{
		ID:         run.ID,
		Status:     string(run.Status),
		Prompt:     run.Prompt,
		CreatedAt:  run.CreatedAt,
		UpdatedAt:  run.UpdatedAt,
		DurationMS: run.UpdatedAt.Sub(run.CreatedAt).Milliseconds(),
		Output:     run.Output,
		Tokens:     run.TotalTokens,
	}
	if run.Err != nil {
		rec.Error = run.Err.Error()
	}
	return rec
}
```

TUI 构造 manager 时传入 store；目录与 checkpoint 同根：

```go
runsStore := runs.NewStore(filepath.Join(rootFromConfig(cfg), ".eino-cli", "runs"))
m.runs = eino.NewRunManagerWithStore(runsStore)
```

测试要覆盖：

- `TestStoreSaveGetRoundTrip`：写一条全字段 `Record`（含 Prompt / Duration / Tokens）→ 读回字段完整。
- `TestStoreListSortedByCreatedAtDesc`：写三条不同 `CreatedAt` → List 返回最新在前。
- `TestStoreGetMissingReturnsFalse`：未写过的 ID → `ok==false, err==nil`。
- `TestStoreSaveSurvivesPartialWrite`：tmp 已存在时 Save 仍能覆盖（rename 原子性的回归测试）。
- `run_test.go` 新增 `TestStartRunPersistsRecord`：用临时目录的 store + `NewRunManagerWithStore`，跑 success 路径，断言落盘 JSON 的 `Status / Prompt / Output / DurationMS >= 0`。
- `run_test.go` 新增 `TestStartRunPersistsTotalTokens`：runtime stub 通过 `TraceConsumer` 发一个 `TracePhaseTokens` 事件，run 结束后落盘 `Tokens` 正确。

### Tradeoffs

设计选择：单一职责的文件 Store，不上 SQLite。依据 `AGENTS.md` 的 "Simplicity" 和 "Pass less data"。CLI 是单进程单用户，文件 + `os.Rename` 已经覆盖崩溃一致性；引入 SQLite 会带来 schema migration、并发连接池、空库初始化等成本，但 CLI 没有任何场景需要。副作用：未来如果 run 数量过万，`List` 的 `ReadDir + N 次 ReadFile` 会变慢；可在第二增量改成 manifest 文件。软回滚不需要开关：store 为 nil 时整条路径退化为内存模式。硬回滚是删除 `backend/session/runs/`、回退 `RunManager` 字段和 `model.go` 一行注入。

设计选择：写盘点放在 `finishRun` 而不是 worker 末尾。依据 `AGENTS.md` 的 "Behavior lives in plain top-level functions"。`finishRun` 已经是 status / output / err 收敛的唯一点；如果把 Save 散到 worker 三条分支，每加一种终止路径都要记得复制一遍。副作用：`finishRun` 现在做两件事（更新内存 + 写盘），但通过把写盘外移到 `mgr.mu` 之外保持锁粒度最小。

设计选择：写盘失败只 log。依据 `AGENTS.md` 的 "Goal-driven execution" —— 持久化失败不应该破坏 "run 协议必须以 RunEventEnd 终止" 这个第一阶段确立的不变量。副作用：磁盘满或权限错误时，run 在 TUI 看起来正常，但磁盘里没有记录；可观测性放在 `slog.Warn`，未来 `/history` 命令显示空列表时用户能从日志反查。

设计选择：`Record` 与 `RunRecord` 解耦。运行时结构带 `context.CancelFunc` 和 `error`，前者完全不可序列化、后者序列化后就是字符串；维护两份 struct 比靠 `json:"-"` 标签清楚。副作用：每次 `finishRun` 多一次 `toRecord` 浅拷贝，9 个字段，可忽略。

设计选择：`Record` 现在就带上 `Prompt / DurationMS / Tokens` 三字段。`/history` 是肉眼可见的下一个消费场景，一行展示需要的最小信息就是这三项加 status。等到那一刻才回头改 schema 会让旧 JSON 文件缺字段，反而要写迁移代码。副作用：每条 run JSON 体积从约 200B 涨到约 400B–1KB（视 prompt 长度），CLI 场景下可忽略。

设计选择：本阶段不做 `RunEventStore`、不做 rollback、不做 `/history`。依据 plan 中阶段 6 的拆解。`RunEventStore` 需要决定事件序列化粒度（trace 的 `[]*schema.Message` 不便直接 JSON）、保留策略、TUI 是否复用，单独立项更合适。Rollback 仍受限于第一阶段 spec 已经指出的根本问题：`history` / sandbox thread data / session todos 的 thread-level 权威状态还未定义，强写 checkpoint 会引入新的不一致。
