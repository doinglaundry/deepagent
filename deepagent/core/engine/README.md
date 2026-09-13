# 迁入的旧执行链（过渡保留）

这里来自已移除的顶层 core，不是用户提供的新 DeepAgent 执行实现。旧 Worker 暂时仍通过本包执行；新主线位于 deepagent/core 与 deepagent/core/agentthread。后续应适配 Worker 到新主线，再移除这个过渡执行链。

以下为迁入前的实现说明，标识符沿用旧代码：

# Core execution contracts

`agentthread.New(Config)` loads shared model history. `SubmitInput` creates one Run or queues input for that Run; model and finalization boundaries drain pending input under the same mutex. Input IDs survive history reload and compaction to prevent duplicate messages on redelivery. `ErrDraining` is retryable and must not acknowledge delivery.

`core.New` compiles a real Eino `model -> tools -> model` graph with a `continue -> model` route for pending input. Model nodes consume `ToolCallingChatModel.Stream` and emit actual provider deltas. Tool streams are forwarded incrementally. `MaxSteps` counts graph nodes; `MaxModelCalls` counts model calls independently. Both counters, usage totals, messages and the exact pending tool index survive an approval checkpoint.

Approvals and clarification pause before the tool runs. A checkpoint contains the Run, thread and namespace identities plus the correlated interrupt. `ResumeRun` restores that state on a new Thread. Completed checkpoints are invalidated by replacing their active block with the progressed state. External tool effects and checkpoint persistence cannot be one transaction; a process crash during a tool can require reconciliation before repeating an effect.

`Close` cancels the Run and waits for its final events. **Continue draining `Events()` concurrently until the channel closes.** A completed Run is cleared only after its events have been forwarded into the Thread channel. Worker must persist those events before releasing its execution permit. Initial history loading respects `Config.Context` and a ten-second cap.

Filesystem operations use Go `os.Root` to confine read, list, literal search, write and exact text replacement. Mutations and host shell commands require approval by default. Shell commands run on the host, use the thread working directory, and are not an operating-system sandbox. Plan mode binds and executes only tools declaring `ReadOnly() == true`; unknown capabilities are denied. The internal subagent has a separate bounded graph with read-only tools and no recursive delegation.

`CompactThresholdTokens` defaults to 24000 when a summary model is configured; a negative value disables automatic compaction. The trigger estimates serialized history bytes divided by four, rather than using a provider tokenizer. `KeepRecentMessages` defaults to six and moves the cut back to a complete user turn, preserving tool-call pairs. Explicit and automatic compaction compare the history version before committing. An optional `History.SaveCompacted` method persists the compaction record with the replacement context.

`checkpointer.NewFile` and `NewRedis` implement the same byte `Get`/`Set` contract as shared Manager storage. File checkpoint directories must be on a shared volume when workers use different hosts. Redis prefixes must be namespace scoped. Checkpoint IDs are path-free identifiers. File writes use atomic replacement and directory synchronization.

`memory.New` supports a durable `api.MemoryStore` with independent renewable job leases and atomic artifact/baseline completion. Stage one extracts source histories. Stage two runs an internal graph with only memory-workspace read/write tools and validates its `MEMORY.md` output before storing it. `Scope` groups source and consolidation records by the configured user or session. The local fallback uses shared-filesystem locks and atomic files. Worker owns periodic source discovery and ordinary-run memory loading.
