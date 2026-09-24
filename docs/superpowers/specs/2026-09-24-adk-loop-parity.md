# ADK execution-loop retirement

Removed the unused local runtime (`host/runtime/local/deepagent`) and its
`MakeLeadAgent` / `MakeAutoDreamAgent` constructors. No Go caller of its
`NewRuntime` remained. The distributed Manager CLI already uses the canonical
Thread path. Removed `chat_model_agent.go` and `deep_compat.go`, including their
independent model/tool loop and ADK child-agent recursion.

| Old execution contract | Canonical implementation and tests |
| --- | --- |
| Model → tool → model | `graph/agent_test.go: TestRun_ModelToolModel` |
| Clarification | `runtime/agentthread/legacy_thread_contract_test.go: TestBlockedRunResumesFromCheckpointOnNewThread`; now a typed persisted interrupt, as required by final design |
| Approval deny never executes | `graph/agent_test.go: TestRun_ApprovalDenyNeverExecutesTool` |
| ReturnDirect | `graph/agent_test.go: TestRun_ReturnDirectDoesNotCallModelAgain` |
| Native tool middleware | `graph/native_tool_middleware_test.go`; invoke and stream transforms, policy ordering, duplicate invocation protection |
| Argument rewriting before execution | `graph/adk_parity_test.go: TestRun_ArgumentNormalizationPrecedesPolicyAndExecution`; Descriptor.NormalizeArgs replaces ADK ToolsConfig callback |
| Unknown tool | `graph/engine_parity_test.go`; model-visible error under the final Registry/Policy contract. ADK's arbitrary unknown-tool execution callback is not a second dispatch path |
| Streamable tools | `graph/native_tool_middleware_test.go`, `tool_multimodal_stream_test.go`, `events_test.go` |
| Plan/task builtins | canonical `tools` registration; `graph/subagent_test.go`, `runtime/agentthread/tool_events_test.go` |
| Independent child budgets and state | `graph/subagent_test.go`, `subagent_state_test.go`, `subagent_resume_test.go` |
| Memory consolidation agent | `memory/consolidator_test.go: TestConsolidatorRunsScopedGraphAndValidatesWrittenArtifact` |
| Scope lease / extraction dedup | `memory/lease_test.go: TestMemory_ScopeLeaseAndDuplicateExtraction` |
| History | canonical Conversation persistence/reload tests; local Runtime's independent JSON transcript is retired |
| Clarification presentation / model events | Thread/Worker typed event tests and Host resume tests replace the local ADK event-to-string adapter |

The unused local Runtime's UI-only tests were retired with that frontend
implementation, not presented as canonical Web acceptance. Its autodream helper
inferred touched files from attempted tool arguments; canonical consolidation
validates the actual scoped artifact instead. The reusable autodream data helpers
remain in memory/autodream.

Scope remaining: the old ADK directory still contains prompt/model/middleware
assembly helpers and the global TUI approver; plural middleware/tool packages
also remain. This removal eliminates the ADK execution loop, not all legacy
scaffolding or all non-Web entrypoints. Those require further consolidation.

Verification before deletion: Graph, middleware, Memory, ModelHub and Host race
suites passed in `/tmp/core-adk-parity-after.log`.

## Retired unused ADK model error wrapper

`adk/error_handling.go` had no production callers after loop retirement. Its
private tests and implementation are removed after verifying the canonical
retry/circuit-breaker tests. It converted cancellation, provider failure and an
open circuit into a successful Assistant message; that behavior is intentionally
not preserved because final-design sections 42–43 require observable errors and
safe stream-opening retries.

| Removed behavior | Canonical contract evidence |
| --- | --- |
| Transient retry and eventual success | `middleware/TestModelRetryOnlyRetriesBeforeStream` |
| Exhaustion/non-retryable error | `middleware/TestModelRetryExhaustionPreservesError`; returns the original error, no fabricated message |
| Cancellation during backoff | `middleware/TestModelRetryCancellationDuringBackoff`; exits promptly without a further attempt |
| No replay after partial stream | `middleware/TestModelRetryOnlyRetriesBeforeStream` |
| Cancellation not retried | `middleware/TestModelRetryNeverRetriesCancellation` |
| Circuit threshold, restore, per-run isolation | `middleware/TestCircuitBreakerThresholdRestoreAndRunIsolation` |
| Single recovery probe and reset after success | `middleware/TestCircuitBreakerOnlyOneRecoveryProbe` |
| Cancellation not provider failure | `middleware/TestCircuitBreakerCancellationDoesNotCountAsFailure` |

Legacy keyword classification and exponential timing are private wrapper policy,
not another reusable model implementation. Canonical retries require an explicit
Retryable predicate, bounded attempts and configured delay. No automatic retry of
tools is introduced. Remaining ADK prompt/model/assembly helpers still require
capability comparison before deletion.

## Model client construction consolidated

Removed `adk/models.go`. The remaining legacy middleware assembly maps its old
config fields directly to `modelhub.New`; it no longer creates provider clients.
Missing summary configuration yields no summary model, preserving the assembly's
optional summarization behavior without a nil-config panic. The legacy chain
fixture now supplies an explicit model identifier, as canonical ModelHub requires.

ModelHub retains configured Kimi model identifiers rather than silently replacing
non-moonshot names with moonshot-v1-8k. It retains its current explicit budget
validation, 120-second default timeout and Claude default token budget. Existing
HTTP tests verify provider streams and Claude thinking/max-token configuration.
Added `TestModelConfigurationNormalizationReachesProvider` to verify trimmed model,
URL and credentials plus case/whitespace-normalized reasoning effort on actual
requests. Provider-specific effort values remain pass-through (normalized), rather
than applying the old helper's low/medium/high-only whitelist. Removed the private
parser test; its supported case/whitespace contracts are now tested at the wire.
The unrelated DefaultAgentIterations assertion remains intact.

## Retire dead ADK middleware assembly and stdin approval

Removed `adk/middleware_chain.go`, its assembly-only test, and `adk/hitl.go`.
Repository searches found no production callers of GetChatModelMiddlewares or
HITLApprover. This removes the second assembly entrypoint and global stdin approval
surface; it does not claim every individual old middleware is already deleted.
The preceding temporary ModelHub mapping in that dead assembly is now gone too.

The retired order assertion depended on ADK-specific concrete types and on
Clarification stripping ToolCalls after Trace. Canonical
`TestMiddleware_OrderAndAfterRunOnce` verifies actual execution order instead.
`TestRun_FollowUpArgumentAliasesPreserveQuestionAndResume` now also verifies that
Trace sees the original question ToolCall and arguments before typed interruption.
`TestRun_TraceAndTranscriptObserveCanonicalEvents` verifies the same event sequence,
complete model/tool history and independent per-run writer cleanup.

The mutable global approver test is intentionally retired with the terminal
frontend. Graph approval/deny, new-agent resume, policy-change protection and
consecutive-approval tests cover the selected persisted Web-facing approval path.
The old optional-summary factory test was a property of dead assembly; Conversation
owns configured compaction and its independent tests, rather than constructing a
second summarization middleware/model implicitly.

Individual ADK implementations remain pending deletion/capability reconciliation,
including legacy plan-mode prompts, silent excess-task truncation and old log
formatting. Removing this uncalled factory does not remove those sources or their
tests, and does not establish full migration parity for them.

## Retire duplicate state, usage, trace, clarification and error middleware

Removed seven ADK files (950 lines): agent_state, token_usage, trace and its test,
clarification and its test, tool_error. They had no external callers after the
assembly removal; their remaining internal references were self-contained tests.

| Retired ownership | Canonical evidence |
| --- | --- |
| Independent model/tool counters | RunState.ModelCalls/Calls and executor ledger; Graph budget/resume tests |
| Independent token accumulator | Conversation.RunUsage; graph usage_test.go including Extra fallback, precedence and checkpoint restore |
| Separate TraceEvent/turn counter/consumer | RuntimeEvent.Sequence + middleware.Trace; observe_test.go verifies same delivered sequence and per-run isolation |
| Trace plan/tokens side channels | Canonical plan_updated/tokens events; plan_test.go and usage_test.go |
| Destructive clarification rewriting | ask_user typed interrupt/resume; follow_up_alias_test.go verifies original ToolCall reaches Trace, aliases/context/options, checkpoint and persisted answer |
| Swallowing all tool errors | TestRun_ToolErrorVisibleButCancellationStopsGraph verifies ordinary errors reach the model while cancellation/deadline errors stop execution |

Exact ADK phase constants, mutable Trace turn resets and FindTrace reflection are
retired with their uncalled API, not replicated as another runtime abstraction.
Malformed question arguments remain model-visible tool errors rather than a
fabricated successful final reply. Tool-observability stderr formatting and
MessagesLog are not deleted in this change; their private log contracts require
separate reconciliation. Other ADK middleware sources/tests also remain.

## Retire duplicate loop-detection window

Removed ADK loop_detection.go and its two unit tests after actual canonical Graph
coverage passed. Canonical LoopGuard hashes full ordered tool batches (name and
arguments, excluding changing call IDs), retains the bounded window and warning
state in its RunState extension, and gets a new instance for each Run.

- `TestLoopGuardStopsRepeatedToolsAndIsRunLocal`: repeated calls stop at the limit,
  eager invocation cannot bypass it, and another Run starts with a fresh window.
- `TestLoopGuardRestoresWindowFromCheckpoint`: interrupt for approval, construct a
  new Agent and resume; the repeated next model call still reaches the hard limit
  and does not execute another tool.
- `TestLoopGuardDistinctCallsExpireFromWindow`: differing arguments remain distinct
  and calls outside the configured window no longer count toward the limit.

The hard-limit behavior remains removal of tool calls from a copied model response;
no second loop or registry is introduced. This deletion does not change default
middleware enablement or claim that all remaining legacy middleware is retired.

## Consolidate todo reminders and remove the unused plan forwarding package

Removed ADK TodoReminder (implementation and private tests) and the unused
middlewares/plan forwarding package. Its three current tests were moved intact
apart from package/constructor names to core/middleware/plan_test.go: validated
updates, restored RunState reminder/idempotency/history preservation and invalid
plan status rejection. No external caller imported the forwarding package.

Canonical Plan reads only RunState.Plan; graph/plan_test.go additionally verifies
real Eino checkpoint restore after compacted context and event ordering/errors.
During comparison, found and reproduced that the canonical reminder incorrectly
accepted ToolCalls on non-Assistant messages as evidence of a visible plan.
TestPlanReminderOnlyTrustsAssistantToolCalls now covers all four roles; only an
Assistant plan call suppresses the reminder, matching the old role boundary.

The canonical tool name and instruction remain update_plan, with legacy todos
argument normalization at the tool boundary. No ADK SessionKeyTodos store remains
in this reminder path. Plan-mode instruction middleware is a separate behavior
and remains under review; it was not silently removed with TodoReminder.

## Retire ADK Memory and Summarization hooks

Removed the uncalled middlewares/adk/memory.go and summarization.go. The former
spawned unjoined extraction goroutines after each model response and tracked an
injected flag; the latter installed a second ADK compactor with a hard-coded
DefaultAgentKey extraction callback. Neither has a caller after assembly removal.

The chosen final lifecycle is different by design: memory.NewPrompt reads the
current scoped snapshot before each model call; Worker RunCompleted observes and
consolidates the Thread history, and the durable scanner retries persisted sources
after restart. Conversation alone owns compaction. Memory extraction and
consolidation both invoke canonical Graph, without another model/tool loop.

Evidence inspected and run before removal:
- TestMemoryPrompt_ReadsCurrentScopeBeforeEachModel and ReadFailurePreventsModel;
- TestMemory_ScopeLeaseAndDuplicateExtraction and scope-separation service test;
- TestConsolidatorRunsScopedGraphAndValidatesWrittenArtifact;
- TestMemorySweepProcessesPersistedHistoryAfterRestart;
- Conversation compaction/race suite and actual Graph threshold-compaction test.

Legacy memory/agent, memory/store and autodream artifact helpers remain for a
separate data-format/capability audit. Removing these dead hooks does not establish
migration of all existing legacy memory files or completion of Memory cleanup.

## Retire duplicate HITL and deferred-tool middleware

Removed ADK hitl.go, hitl_test.go and deferred_tools.go after confirming no callers
outside the retired middleware's own tests. Canonical policy and registry tests
passed before deletion. Registry.Filter removes both the callable descriptor and
model schema without mutating the source registry; it replaces the old BeforeAgent
list mutation. No separate deferred registry or execution policy remains here.

The old HITL callback's nil=>approve and destructive ToolCall removal are
intentionally retired. Descriptor.RequiresApproval / Policy asks through Eino
Interrupt; deny produces the matching model-visible tool result, and approve
resumes the exact call. TestRun_ApprovalDenyNeverExecutesTool,
TestRun_LegacyPolicyGateDeniesWithoutExecution, global-deny precedence,
TestCheckpoint_PendingApprovalSurvivesPolicyChange and consecutive-approval tests
cover this selected contract. RegistryFilterKeepsExecutionAndOrder verifies masked
tools cannot execute and filtering preserves the original registry.

Existing schema errors fail construction rather than retaining a tool whose Info
cannot be read as the old deferred middleware did. Remaining ADK log, sandbox,
plan-mode and subagent-limit modules are not deleted by this change.

## Move planning guidance into canonical Plan prompt capability

Canonical Plan now implements BuildPrompt using update_plan and the existing
step-lifecycle guidance (start, completion, actual parallelism, revision, trivial
request exemption). Prompt context is rebuilt per model request; it does not append
to durable history or alter the caller's system message. The actual checkpoint/
compaction Graph test checks exactly one guidance block, exactly one restored plan
reminder and no guidance in Conversation history.

Removed uncalled ADK plan_reminder.go/test and todo.go. Their callback-based switch
and first-System-message mutation belong to the retired assembly. Canonical callers
enable planning by attaching Plan (ThreadHost EnablePlan already does this). The
separate middlewares/planmode package currently holds typed user-input payloads and
is still referenced by Thread, so it is not removed.

The former distributed bootstrap gap is now closed: it uses canonical backend/tools
and enables Plan once. TestFactoryUsesCanonicalToolsAndPlanPrompt exercises actual
Factory assembly, unique tool registration and planning guidance. This is local
integration evidence; it does not replace real database/browser acceptance.

## Retire ADK SubagentLimit

Read the complete implementation and both tests; no production callers remain.
The old middleware removed excess task calls from the assistant message. That
behavior is intentionally retired under the final design's call ledger and
ordered-result contract: a concurrency bound limits simultaneous work, without
discarding model-issued calls.

Canonical ToolExecutor.executeBatch owns the concurrency limit. The new
TestChildAgent_ConcurrencyLimitQueuesEveryTask drives five task calls through the
real TaskTool/executor path with two concurrent slots, holds each wave, verifies
the peak is exactly two, and checks all five ordered results. Existing same-Graph
child/parallel-approval tests cover the ChildRunner execution path. Unannotated
task tools remain sequential; ParallelSafe is still an explicit descriptor choice.
No second limiter or middleware was added. Removed subagent_limit.go and its two
old truncation tests after the canonical race tests passed.

## Move tool metadata logging onto the existing ToolMiddleware interface

Read the complete ADK ToolCallObservability implementation and both tests; no
production callers remain. Canonical middleware.ToolCallObservability wraps the
existing ToolHandler and preserves tool.exit/tool.error, name, duration, input
size and successful output size. Arguments and result contents are not logged;
returned errors remain available as error metadata, matching the previous contract.
Model-visible ToolResult.IsError also selects tool.error without logging its body.
Results and errors pass through unchanged. Logger injection avoids changing the
process-global logger in tests. No ADK endpoint or second tool pipeline remains.

TestToolObservabilityPreservesResultsAndLogsMetadataOnly covers success, errors,
cancellation and model-visible tool failure. Canonical middleware/Graph race suites
passed before deleting the old implementation and tests. MessagesLog and sandbox
are separate remaining migration tasks; this deletion does not claim their parity.
