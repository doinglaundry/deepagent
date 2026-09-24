# core/engine 删除对照

生产入口已使用 `runtime/agentthread → graph`。删除前 Go 引用检查仅发现 engine 自己的 agentthread；保留旧 checkpoint 数据解码器，不保留旧执行器。

| 删除的旧测试契约 | 新路径的证据 |
|---|---|
| GraphToolIterationAndIncrementalOutput | graph/events_test.go: TestRun_ModelAndToolStreamsPreserveOrder；graph/agent_test.go: TestRun_StreamAndRunUseSameExecutionPath |
| PendingInputAtFinalBoundary | runtime/agentthread/thread_test.go: TestThread_SubmitAndAppendUseSameRun、TestThread_InputAcceptedAtFinishBoundary |
| ApprovalResumesSameRunOnNewThread，含错误关联、完成后拒绝重放 | graph/agent_test.go: TestRun_ResumeDoesNotRepeatCompletedTool；runtime/agentthread/thread_test.go: TestRun_InterruptAndResumeOnNewThread；host/runtime/end_to_end_test.go: TestApprovalResumesOnNewWorkerWithoutRepeatingModelDecision |
| BudgetsCountModelAndGraphSeparately | graph/resume_budget_test.go: TestRun_ResumeContinuesGraphAndModelBudgets |
| CancellationKeepsHistoryAndThreadUsable | runtime/agentthread/terminal_events_test.go: TestThread_CancellationKeepsHistoryAndThreadUsable |
| PlanModeCannotExecuteUnknownCapabilityTool | graph/engine_parity_test.go: TestRun_ReadOnlyRegistryCannotExecuteUnclassifiedTool |
| MultimediaInputAndConsumptionEvent | graph/input_events_test.go；thread/follow_up_test.go: TestThreadAdapter_InputConsumedPreservesIndividualIdentityAndMedia；worker/thread/transport_output_test.go: TestManagedInputConsumedIdentifiesOnlyCurrentMessage；host/runtime/usage_contract_test.go |
| ReloadRefreshesBootstrapPromptAndPreservesCompaction | host/runtime/reload_contract_test.go: TestManagedReloadRefreshesPromptAndRepairsCancelledToolRequest |
| CancelledCheckpointHistoryRepairsPairsForNewRun | 同上；修复只作用于模型请求，历史不伪造工具结果 |
| TokenEventsAccumulateAcrossModelCalls | graph/usage_test.go；host/runtime/usage_contract_test.go |
| RedeliveryDoesNotDuplicatePersistedUserInput | worker/distributed/rollout_test.go: TestRolloutRedeliveryUsesTransportIdentityAcrossWorkers |
| ClarificationReadableOptionsAndResumeOnNewThread | runtime/agentthread/thread_test.go；thread/follow_up_test.go；worker/thread/transport_output_test.go: TestManagedOutputFollowUpPreservesOptions |
| StaleCompactionCannotOverwriteNewHistory | internal/conversation/conversation_test.go: TestContext_StaleCompactionCannotOverwriteNewInput |
| AutomaticThresholdCompactionRetainsRecentInput | graph/engine_parity_test.go: TestRun_AutomaticThresholdCompactionRetainsRecentInput |

只读模式仍禁止未声明只读的工具执行；按最终方案，普通未知工具错误转为模型可见结果，不再使用旧 engine 的立即失败行为。

旧格式恢复继续由 `runtime/checkpointer/engine.go`、`engine_input.go` 和 JSON fixture 验证，生产 Host 的 allow/deny 文件副作用测试保留。

这张表只证明该重复 engine 的契约迁移。其余旧 Core/ADK 路径、失败/取消后的 checkpoint 失效、持久化失败与崩溃后的副作用对账、Pending 元数据、其他协议适配、Web-only 完整验收仍在总目标内。
