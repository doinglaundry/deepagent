# Distributed DeepAgent Implementation Plan

> Execute independent modules with superpowers:subagent-driven-development; integrate and review in this task.

**Goal:** Replace the CLI-local runtime with a shared MySQL/Redis Manager and independent Worker executing Eino graphs.
**Architecture:** CLI submits and subscribes using Manager APIs. Workers compete for expiring permits and execute Core Threads. Core owns graph execution, pending inputs, resumable checkpoints and model history. Retain Local/AIO implementations as optional legacy adapters, not the default execution path.
**Tech Stack:** Go 1.25+, Eino, Bubble Tea, GORM/MySQL, go-redis.
**Spec:** ../specs/2026-09-12-deepagent-architecture.md

## Global constraints
- Single production path: CLI -> Manager -> Worker -> Core. No direct CLI model execution.
- Namespace scopes every persistent operation; SessionID groups threads, ThreadID identifies a conversation, RunID an execution.
- Token fencing applies to worker state mutations and event publishing. Delivery acknowledgement is not execution completion.
- Persist durable events before publishing. Host reconciles real-time events and durable history by event IDs.
- Approval restores the same Run/checkpoint; cancellation does not close a thread; detach does not cancel.
- Default filesystem backend uses thread working directory. AIO remains available separately.
- Shared local paths require shared volumes for multi-machine deployment.
- No external model calls in unit tests; run integration tests against temporary isolated infrastructure where available.

## Task 1: Protocol and shared interfaces (root)
Files: protocol/types.go, manager/api/api.go, docs/superpowers/plans/*.
- [x] Define Input, Event, Block, Thread, Permit and namespace-scoped Manager interface.
- [x] Validate malformed input and resume correlation in tests.
- [x] Freeze interfaces before module implementation.

## Task 2: Manager and storage
Files: manager/*.go, manager/internal/*, manager/*_test.go.
Consumes: protocol types and manager/api.Manager.
Produces: manager.New(ctx, Config) (*Manager,error), Close(), complete api.Manager implementation.
- [x] Test state transitions, fenced claims, permit expiration, cancel cutoff, blocked resume, event persistence and namespace isolation.
- [x] Implement MySQL scheduling/history, Redis delivery/pubsub and reconciliation.
- [x] Verify concurrency and crash recovery tests; review own changes.

## Task 3: Core graph, Thread, backends and context
Files: core/*, core/agentthread/*, core/backends/*, core/checkpointer/*, core/compact/*, core/memory/*.
Consumes: Eino model, tools and protocol Input/Event.
Produces: agentthread.New(Config), SubmitInput, ResumeRun, Cancel, Events, Close; graph engine with model/tools/continue nodes.
- [x] Test multi-model tool runs, incremental output, pending input at finish boundary, checkpoint approval resume, cancellation and separate budgets.
- [x] Implement Core graph and safe filesystem/command tools; context history/checkpoint abstractions with shared storage support.
- [x] Implement version-checked context compaction and configurable memory extraction/consolidation.
- [x] Verify without real model requests and review own changes.

## Task 4: Worker execution and distributed assembly
Files: worker/managed/*, worker/thread/*, worker/distributed/*, worker/tasktool/*, cmd/deepagent_worker/*.
Consumes: api.Manager and agentthread public contracts.
Produces: worker.Run(ctx), distributed configuration/bootstrap and task tools.
- [x] Test competing workers, renewal loss, input confirmation, output draining, cancellation, blocked release and graceful shutdown.
- [x] Wire production model configuration, MCP tools, filesystem, shared history/checkpoints and child threads.
- [x] Verify all worker packages; review own changes.

## Task 5: Host Runtime and CLI migration (root)
Files: host/runtime/*, cmd/deepagent/*, backend/cli/tui/*, main.go, README.md, scripts/install-sgadk.sh.
Consumes: api.Manager; produces RunStream, event subscriptions, resume/cancel/history operations.
- [x] Test subscribe-before-submit, deduplication, durable catch-up, request/run binding and retry binding.
- [x] Route CLI exclusively through Manager; add one-shot prompt and shared thread flags.
- [x] Adapt TUI streaming/approval/history; remove legacy local runtime from the production path.
- [x] Document deployment, migration, storage and external-side-effect limitations.

## Task 6: Integration and whole-change review
- [x] Build CLI and Worker and run go test ./... plus race tests for new concurrent modules.
- [x] Exercise isolated MySQL/Redis integration for two workers, restart and resume where local infrastructure permits.
- [x] Audit all nineteen supplied spec sections against implementation; fix uncovered requirements.
- [x] Review full diff and report precise verification and remaining limitations.

## Decisions / progress
- User supplied and authorized target architecture; retain it verbatim as the design authority rather than request redundant approval.
- Work on codex/distributed-deepagent in the existing clean checkout; no shared branch push or merge.
- Original repository baseline test was blocked by cache permissions. Use /private/tmp Go caches and download dependencies.


## Architecture coverage audit

| Supplied sections | Implemented entry points / evidence |
| --- | --- |
| 1–3: system, identities, ownership | `protocol`, `manager/api`; CLI/Manager/Worker/Core packages; production commands |
| 4–5: CLI and Host | `host/cli`, `host/runtime`, TUI distributed adapter; subscribe-before-submit, SQL catch-up, request/retry correlation tests |
| 6: Manager | fenced MySQL scheduling, normalized input/history tables, Redis cache/pubsub; real-store namespace and takeover tests |
| 7–9: Worker layers | managed claim/renew/drain/release loop, distributed assembly, thin Core protocol adapter; competition, rejection, shutdown tests |
| 10–11: Thread and graph | shared versioned history, pending-input boundary, drained events, real Eino model/tools/continue graph, independent budgets |
| 12: middleware and tools | rooted file tools, command approval, plans/questions, tool-pair/JSON repair, on-demand skills, Web/MCP, internal read-only subagent |
| 13: context and checkpoints | automatic/manual compaction with CAS, shared history; configurable MySQL/Redis/file checkpoint stores and correlated resume |
| 14: distributed tasks | Manager-backed child creation, named lookup, send/wait/close; timeout and parent/session isolation tests |
| 15: memory | periodic source scanner, independent renewable MySQL jobs, extraction then internal tool-using consolidation, persisted artifacts/baselines |
| 16: protocol/storage | durable versus realtime event classification, correlation metadata, multimodal input mapping, Redis failure SQL recovery |
| 17: four end-to-end flows | normal two-Worker conversation, restart approval resume, pending/active cancellation, independent-process SIGKILL takeover |
| 18–19: boundaries/read order | README and Core contract documentation; shared-volume requirement, side-effect repetition, retained Local/AIO code |

Implementation choices: package subdivision follows this repository rather than reproducing the reference paths. Core checkpoints serialize the exact graph/tool boundary in an application-owned state structure. Default MySQL checkpoint writes are atomically fenced; file/Redis options cannot share a transaction with MySQL scheduling. Approval is scoped to the current tool call; no implicit remembered authorization cache was introduced. MCP's supported transport is Streamable HTTP, with an explicit bridge needed for stdio. These choices are documented in README/Core contracts.

Final review also covered delivery acknowledgement versus completion, failed resume metadata persisted before acknowledgement, pre-delivery control dispositions, cancellation cutoffs, asynchronous event ordering, late realtime chunks, and skill catalog activation.


## Final verification

- `go mod tidy` completed with dependencies downloaded to temporary Go caches.
- `go test ./...` passed across the original and new packages.
- `go vet ./...` passed after the final code changes.
- `go test -race ./backend/cli/tui` passed, including late-output reconciliation and failed-cancel subscription behavior.
- `scripts/test-distributed.sh` passed with `-race -count=1`, fresh CLI/Worker binaries, isolated MySQL 8 on localhost:13316 and Redis on localhost:16386. It covered the Manager/Core/Worker/Host suites, normalized SQL persistence, fenced takeover, approval resume on another process, and SIGKILL recovery while a model request was active.
- All model requests in verification used local simulated providers; real provider credentials and hosted model APIs were not exercised.
- `git diff --check` passed. No commit, merge or push was performed.
