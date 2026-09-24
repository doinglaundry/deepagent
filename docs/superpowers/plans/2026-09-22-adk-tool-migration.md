# ADK Tool Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Compare the legacy ADK tools with the canonical tool chain, migrate only unique capabilities into `deepagent/core/tools` or `deepagent/core/middlewares`, and make them available through the existing Eino Graph assembly.

**Architecture:** Keep `core/tools/filesystem`, `core/middlewares/web`, `plan`, `skill`, and `subagent` as the canonical implementations. Migrate unique ADK capabilities behind the same `tool.BaseTool` and `middleware.Middleware` contracts. Do not delete ADK sources until behavior is covered and the live constructor no longer depends on them.

**Tech Stack:** Go, Cloudwego Eino tools/compose, existing DeepAgent middleware interfaces, Go unit tests.

**Spec:** Conversation-approved ADK capability comparison and migration rule.

## Global Constraints

- Preserve existing canonical tool names and behavior when an ADK tool is a duplicate.
- Do not add a builder, coordinator, or forwarding layer.
- Every migrated tool must be returned by `Middleware.Tools` or supplied through `Config.Tools`.
- Keep legacy ADK files until migration verification is complete.
- Run focused tests and `go test ./...` after migration batches.

## Review Focus

- A migrated tool must expose the same input schema and return useful errors.
- Read-only mode must never expose write, delete, patch, or execute tools.
- Shell jobs must not bypass the existing command classifier and approval policy.
- Search and lint tools must work with the configured workspace/backend.
- Migrated tools must be visible to `collectAllTools` and bind to the model.

### Task 1: Build the capability inventory

**Files:**
- Create: `docs/superpowers/specs/2026-09-22-adk-tool-capability-matrix.md`
- Inspect: `deepagent/core/tools/adk/*.go`
- Inspect: `deepagent/core/tools/filesystem/*.go`
- Inspect: `deepagent/core/middlewares/*.go`

**Interfaces:**
- Produces a table mapping every ADK tool to canonical equivalent, unique behavior, or obsolete behavior.

- [ ] **Step 1: Extract tool names and public constructors from both trees.**
- [ ] **Step 2: Read each unique implementation and record inputs, outputs, backend dependencies, and policy requirements.**
- [ ] **Step 3: Write the capability matrix and mark migration targets.**
- [ ] **Step 4: Verify every ADK file is classified exactly once.**

### Task 2: Migrate workspace search and lint capabilities

**Files:**
- Create: `deepagent/core/tools/semantic_search.go`
- Create: `deepagent/core/tools/read_lints.go`
- Modify: `deepagent/core/middlewares/filesystem/filesystem.go`
- Test: `deepagent/core/tools/semantic_search_test.go`
- Test: `deepagent/core/tools/read_lints_test.go`

**Interfaces:**
- Consumes: existing `backends.WorkspaceBackend` and `tool.BaseTool`.
- Produces: tools named `semantic_search` and `read_lints`, returned by the filesystem middleware when the backend supports them.

- [ ] **Step 1: Write tests for tool metadata, valid input, backend error, and empty result.**
- [ ] **Step 2: Implement thin tools that call workspace/backend interfaces directly.**
- [ ] **Step 3: Register them in filesystem middleware without changing existing file tool names.**
- [ ] **Step 4: Run focused tests.**

### Task 3: Migrate background shell capability

**Files:**
- Create: `deepagent/core/tools/background_shell.go`
- Modify: `deepagent/core/middlewares/filesystem/shell_jobs.go`
- Test: `deepagent/core/tools/background_shell_test.go`

**Interfaces:**
- Consumes: `backends.CommandExecutor`, existing shell classifier, workspace job store.
- Produces: `shell_start`, `shell_status`, and `shell_output` tools with the same approval/classification boundary as `execute`.

- [ ] **Step 1: Write tests proving unsafe commands are rejected before job creation.**
- [ ] **Step 2: Write tests for start, status, output, and missing job errors.**
- [ ] **Step 3: Implement the tools using the existing job store and classifier.**
- [ ] **Step 4: Expose them only when execute is enabled and the backend supports jobs.**
- [ ] **Step 5: Run focused tests.**

### Task 4: Migrate memory and clarification behavior

**Files:**
- Create or modify: `deepagent/core/middlewares/memory/`
- Modify: `deepagent/core/tools/tools.go`
- Modify: `deepagent/core/middlewares/skill/skill.go`
- Test: `deepagent/core/middlewares/memory/*_test.go`

**Interfaces:**
- Consumes: existing `core/memory` stores and Eino interrupt/resume APIs.
- Produces: one canonical user-question tool and one memory update path; no duplicate ADK tool names.

- [ ] **Step 1: Compare ADK clarification and auto-dream behavior with current `ask_user` and memory packages.**
- [ ] **Step 2: Write tests for interrupt, resume, and persisted memory update behavior.**
- [ ] **Step 3: Implement only missing behavior behind canonical interfaces.**
- [ ] **Step 4: Keep `ask_user` as the single public clarification tool.**
- [ ] **Step 5: Run focused tests.**

### Task 5: Wire migrated tools into the graph

**Files:**
- Modify: `deepagent/core/middlewares/filesystem/filesystem.go`
- Modify: `deepagent/core/constructor.go`
- Test: `deepagent/core/constructor_test.go`

**Interfaces:**
- Consumes: migrated tools and existing `Middleware.Tools` contract.
- Produces: `collectAllTools` output containing canonical and migrated unique tools, with masks and HITL gates applied.

- [ ] **Step 1: Add constructor tests that inspect the final tool list.**
- [ ] **Step 2: Verify read-only filtering removes every mutating migrated tool.**
- [ ] **Step 3: Verify tool policy gates wrap execute and background shell tools.**
- [ ] **Step 4: Run constructor and graph tests.**

### Task 6: Audit legacy ADK retention

**Files:**
- Modify: `docs/superpowers/specs/2026-09-22-adk-tool-capability-matrix.md`
- Inspect: `deepagent/core/tools/adk/`, `deepagent/core/middlewares/adk/`

- [ ] **Step 1: Confirm every unique capability has a canonical implementation or an explicit retention reason.**
- [ ] **Step 2: Confirm no live command imports the legacy tool packages.**
- [ ] **Step 3: Leave legacy sources in place until a separate deletion decision is approved.**
- [ ] **Step 4: Run `go test ./...` and `go build ./...`.**
