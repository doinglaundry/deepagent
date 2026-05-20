### Goal

Add interactive CLI `/history` and rollback to a selected run's **post-run** state. If the user selects run `R`, the CLI restores the state as it was immediately after `R` finished, removing later conversation, checkpoint, and controlled artifact state.

This is stricter than phase 1 run history: rollbackable runs must block untracked host shell side effects. In rollback-protected runs, `shell` and `execute` return a tool message instead of running. This keeps rollback consistent without pretending we can reverse arbitrary host commands.

Controlled rollback state:

- `DeepAgentRuntime.history`
- `.eino-cli/checkpoints`
- `.eino-cli/users/local/threads/cli/user-data`
- `.eino-cli/memory`
- `.eino-cli/skill-*`
- `.eino-cli/runs`

Not included:

- Whole git workspace snapshots.
- Gateway/server mode.
- Arbitrary host files outside controlled roots.
- AIO orphan container process state.

### Implementation

Add `backend/session/rollback` with a file snapshot store:

```go
type Store struct {
	root string
}

type Snapshot struct {
	RunID     string          `json:"run_id"`
	CreatedAt time.Time       `json:"created_at"`
	History   json.RawMessage `json:"history,omitempty"`
}
```

`SavePost(ctx, runID, history)` writes to `.eino-cli/rollback/<run_id>/post.tmp`, copies controlled directories, writes `snapshot.json`, then renames to `post`. `RestorePost(ctx, runID)` restores the copied directories and returns the serialized runtime history.

Add runtime helpers on `DeepAgentRuntime`:

```go
func (r *DeepAgentRuntime) ExportHistory() ([]byte, error)
func (r *DeepAgentRuntime) ImportHistory(payload []byte) error
func (r *DeepAgentRuntime) RollbackToHistory(payload []byte) error
```

`RollbackToHistory` imports history and resets the trace turn. It does not toggle plan mode.

Add rollback metadata to `runs.Record`:

```go
Rollbackable bool   `json:"rollbackable,omitempty"`
RollbackPath string `json:"rollback_path,omitempty"`
RollbackError string `json:"rollback_error,omitempty"`
```

`RunManager` gets an optional rollback store. In `startRunWorker`, after a terminal run finishes, if rollback store exists, export runtime history, save a post snapshot, update the run record, and save it again. Snapshot failure leaves the run non-rollbackable with `RollbackError`.

Add rollback-protected context in middleware state:

```go
func WithRollbackProtected(ctx context.Context, on bool) context.Context
func IsRollbackProtected(ctx context.Context) bool
```

`startRunWorker` stamps this context when a rollback store exists. `shell` and `execute` deny under this flag.

Add interactive `/history`:

- Register `history` in `backend/cli/tui/commands.go`.
- Add model fields: `runHistoryOpen`, `runHistoryRows`, `runHistorySel`, `runHistoryStore`.
- Open with `/history`; list newest first using `runs.Store.List`.
- Render a panel like HITL/todo, not the slash popup.
- Keys: Up/Down select, Esc/q close, Enter restores selected row if rollbackable.
- Restore sequence: `rollback.Store.RestorePost`, `rt.RollbackToHistory`, reload run list, rebuild TUI messages from successful records up to selected row, clear transient stream/tool/todo/token state, push one system confirmation.

### Tradeoffs

Strict shell blocking is intentionally conservative. It is better to deny an unsafe tool during rollbackable runs than to show a rollback button that leaves hidden host-side mutations behind.

The snapshot copies only `.eino-cli` controlled roots. This preserves sandbox uploads/outputs/workspace, checkpoints, run metadata, memory, and skill artifacts, while avoiding expensive and risky whole-repo snapshots.

The first `/history` UI is modal and keyboard-only. It follows existing HITL/todo patterns and avoids reusing the slash popup because history rows are persisted run records, not command candidates.
