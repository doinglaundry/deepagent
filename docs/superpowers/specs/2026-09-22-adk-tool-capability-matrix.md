# ADK Tool Capability Matrix

| Legacy ADK capability | Current canonical implementation | Decision |
|---|---|---|
| `read_file`, `write_file`, `edit_file`, `delete_file`, `ls`, `glob`, `grep`, `rg` | `core/middlewares/filesystem` and `core/tools/filesystem` | Keep canonical implementation |
| `shell`, `await_shell`, shell jobs | `core/middlewares/filesystem/shell_jobs.go` with the new command classifier | Keep canonical implementation |
| `apply_patch` | `core/middlewares/filesystem/filesystem.go` | Keep canonical implementation |
| `web_search` | `core/middlewares/web` | Keep canonical implementation |
| `semantic_search` | No canonical equivalent | Migrated to `core/tools/semantic_search.go` |
| `read_lints` | No canonical equivalent | Migrated to `core/tools/read_lints.go` |
| `clarification` / follow-up | `core/tools/tools.go` and filesystem `ask_user` | Merge into one `ask_user` capability |
| `auto_dream` | `core/memory` is the canonical memory subsystem | Keep as memory-specific behavior; no duplicate public tool yet |
| `read_lints` Go diagnostics | `core/tools/read_lints.go` | Registered by filesystem middleware |
| ADK trace, token, loop, todo, error middleware | `core/middlewares` equivalents or GraphState | Middleware migration, not tool migration |

The live constructor only receives tools returned by `Middleware.Tools` and
`Config.Tools`; legacy ADK packages remain available as source references until
the remaining capability audit is complete.
