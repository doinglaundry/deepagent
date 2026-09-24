# Worker filesystem tool consolidation

Production: `worker/distributed/bootstrap.go` → canonical `backend` / `tools` / filesystem middleware. The unreferenced `core/tools/filesystem` package was removed after verifying its eight tool capabilities against the current tools, Backend/Workspace, and command service. Behavioral differences below remain work for the canonical path; removal does not imply exact compatibility.

## Verified contracts

- Root traversal and escaping symlinks: `backend.TestWorkspace_PathAndSymlinkBoundaries`.
- Plain read content, exact single-match edit, rejection without mutation for ambiguous edits, and 1 MiB reads: `tools.TestFilesystemPreservesWorkerReadAndExactEditContracts`.
- Literal search, empty-query rejection, 100-result limit: `tools.TestSearchFilesPreservesLiteralWorkerQueries`, `TestSearchFilesRejectsEmptyQueryAndBoundsResults`.
- Actual Worker registry uniqueness and plan-mode filtering: `distributed.TestFactoryUsesCanonicalToolsAndPlanPrompt`.
- Worker filesystem config explicitly sets `MaxFileSizeMB: 1`; write/edit/delete approval is set by Worker policy, command approval comes from descriptors.

## Unresolved behavior differences

| Capability | Legacy Worker | Canonical path |
| --- | --- | --- |
| Default read | Entire file up to 1 MiB | Default 2000-line window |
| Directory list | Reject over 1000 entries | Compare bounded listing behavior before deletion |
| Search traversal | At most 10000 visited entries; includes ordinary hidden directories | Skips selected directories; no equivalent visited-entry budget |
| Search oversized files | Searches first 1 MiB | Skips files larger than configured limit |
| Paths | Reject every absolute path | Accept in-root absolute paths; other absolute paths are virtual-root relative |
| Write | Requires existing parent; new files 0600 | Creates parent directories; new files 0644 |
| Execute output | Streaming stdout/stderr, first 1 MiB cap | Streaming through CommandService, first 1 MiB retained even for slow consumers; success exit-code suffix still differs |
| Execute timeout | Default 60s, maximum 300s | Worker now explicitly 60s; execute maximum 300s; timeout_seconds alias retained |

These are observed differences, not accepted compatibility exceptions. Preserve the single canonical backend/tool ownership while resolving the required contracts. The old package is absent from production wiring and is no longer an executable fallback.

Validation: `/tmp/core-filesystem-compat.log`, `/tmp/core-filesystem-compat-race.log` passed tools/backend/distributed/host-runtime suites. No real database or browser acceptance performed.

## Command streaming migration

`executeTool.StreamableRun` uses the existing CommandService.Start/Wait/Cancel job ledger. InvokableRun consumes that exact stream; no direct process implementation or second job registry. Shell and await_shell retain their existing foreground/background behavior.

`TestExecuteStreamsAndConsumerCloseJoinsJob` verifies incremental output before command completion and silent command cancellation/join on consumer close. `TestExecuteStreamAndInvokeShareResultAndErrors` covers common results and nonzero exits. `TestExecuteTimeoutAndParentCancellation` distinguishes a tool-local timeout from caller cancellation. Parent cancellation propagates; tool-local timeout is an ordinary model-visible tool failure.

Scoped race suites, full repository tests and independent build are recorded in `/tmp/core-execute-stream*.log`. Remaining output-format differences above still prevent declaring complete legacy equivalence.

Prefix retention now uses `CommandRequest.KeepOutputPrefix` on the existing job buffer. Execute enables it; shell keeps its rolling tail. Snapshot offsets still count all produced bytes, preventing repeated output after truncation. `TestCommandsPrefixRetentionAndIncrementalOffsets` verifies completed-job prefix retrieval and subsequent offsets; `TestExecuteSlowConsumerRetainsFirstMiB` verifies the execute path with more than 1 MiB of real process output. Race evidence: `/tmp/core-command-prefix-race.log`.
