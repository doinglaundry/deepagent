# AGENTS.md

Project-level coding-style notes for the eino-cli repository, addressed
to AI agents (Cursor, Codex, etc.) and human contributors. Agents
auto-load this file when they open a task in the repo.

## Core Principles

> **Structs hold data. Functions hold behavior. Pass less data. Push less stack.**

Four corollaries:

1. **A struct only holds "state that must travel together".** Fields must
   share a lifecycle and be meaningful to read side by side.
2. **Behavior lives in plain top-level functions.** No receivers, no
   hidden callback fields, no `deps.X.Y(...)` indirection. The body
   should read top-to-bottom in one place.
3. **Pass less data.** When three callers all derive the same value from
   `cfg`, internalize the derivation in the callee. When seven struct
   fields only see two readers, delete the other five.
4. **Push less stack.** Business code from outermost interface to
   innermost executor should stay **within four layers** when possible.
   Every layer earns its keep; collapse pure-forwarding layers.

## When Structs Earn Their Place

**Overcorrection warning**: when the alternative is an 8+ argument
function, a struct is the right answer; a single Go type shared between
two serialization formats also stays — solve it with struct tags
(`json:"x" yaml:"x"`), not parallel DTOs.

**Project-specific exception: configuration uses one `config.Config`.**
When a submodule needs only a few fields, pass the whole `cfg` and let
the consumer read what it needs. **Do not** define a parallel config
struct just to copy fields into — that is the most common source of
indirection in this repo.

## Naming

- **Variable name = the thing's meaning.** `getID()` returns `id`, not
  `result` / `r` / `tmp`.
- **No invented abbreviations.** Expand `usrCnt` / `procRslt` /
  `tmpVal`. Standard Go shorts (`ctx` / `err` / `cfg` / `fn` / `req` /
  `resp` / `i` / `ok`) stay — those are industry vocabulary. Readability
  always wins.
- **Function names start with a verb.** `getX` / `buildX` / `applyX` /
  `parseX` / `renderX`. Avoid `pathFor` (preposition) / `validX`
  (adjective) / `userInfo` (bare noun). Effective Go's "no Get prefix"
  recommendation is **not** adopted here — `getX` is preferred when it
  matches the semantics. Exceptions: Go's standard `NewX` (constructor)
  and trivial property helpers (`utcNowISO` /
  `defaultIterationLimit`) may stay nominal.
- **Carry renames through to the bottom.** All call sites, comments,
  and tests change in the same commit. A half-finished rename only
  leaves archaeological strata behind.

## Comments

- **Two lines max, answer "why".** "What" is the code's job.
- If one line is not enough, ask first: should this be renamed or
  extracted into a function?
- Legitimate multi-line cases:
  - Package doc (`// Package x ...`) — read as a whole via `go doc`.
  - Structural conventions for large literals (e.g. prompt template
    indentation rules).
  - Non-obvious external constraints (protocol quirks, upstream bugs)
    that a reader cannot recover from the code alone.
- The default action is delete. **Never** glue a paragraph-level
  explanation onto code you just generated — that text belongs in the
  commit message or a spec doc.

```go
// Reset turn counter so /clear restarts numbering at 1.
func (t *Trace) ResetTurn() { t.turn.Store(0) }
```

## Concise Assignment (avoid if/else)

The standard pattern for in-place normalization:

```go
trimmed := strings.TrimSpace(name)
if trimmed == "" {
    trimmed = defaultName
}
ac.Name = trimmed
```

Each line does one thing. Avoid splitting the assignment across both
branches:

```go
// AVOID
if strings.TrimSpace(ac.Name) == "" {
    ac.Name = name
} else {
    ac.Name = strings.TrimSpace(ac.Name)
}
```

Three typical replacements:

- **Default-value assignment** → write the default first, then `if`-override (example above).
- **Error handling** → return early; do not stuff the main path inside `else`.
- **Either-or mapping** → use map / switch / lookup table instead of nested `if`.

## Commit Granularity

- Pure rename ≠ behavior change → split into two commits.
- "Remove the middle layer" + "rewire the consumers" → one commit each.
- Each commit's diff should fit in a single sentence.

## Spec Documents

`specs/<date>-<topic>/design.md` is where design lands. The conventions
below let a reviewer answer the only three questions worth asking:
**what / how / how does it fail safely**.

### Three-section skeleton

Each independent feature (a yaml field, a middleware, a tool…) gets
exactly three sections:

```
### Goal              — one-sentence current state + code references + expected outcomes
### Implementation    — file-grained numbered steps + paste-ready Go code
### Tradeoffs         — design choice + side effects / risks + rollback
```

Do not invent "current state / desired behavior / change list / side
effects / risks / rollback toggle" four/six/seven-section variants —
extra headings do not map onto the reviewer's questions.

### Code references

- Existing code: use the tool's clickable syntax (in Cursor:
  `` ```start:end:path ``). Paths are repo-root relative (`backend/...`).
- New / modified code: regular ``` ```go ``` fences, paste-ready;
  signatures and import paths must match real types.
- Inline symbol mentions: `` `path::Symbol` ``, e.g.
  `backend/agent/middlewares/trace.go::Trace`.
- Important diffs: split old vs new with `// === new ===` /
  `// === existing ===` markers inside the code fence.

### Anchor each design choice

Every **design choice** carries a short reason, **preferably citing
AGENTS.md itself**:

- "Overcorrection warning — only consider a struct at 8+ fields"
- "Behavior lives in plain top-level functions"
- "Either-or mapping → switch / lookup table"
- "Push less stack"

Do not write "best practice / clean / maintainable" — those carry no
real constraint.

### Side effects must be concrete

Medium+ changes (>20 lines) require a "side effects / risks" section
with specific numbers, file names, test names:

- ❌ "performance impact negligible"  
  ✅ "4 int64 struct copies", "overflows in 58 million years"
- ❌ "no impact on other modules"  
  ✅ "`esc_footer_test.go`'s existing assertions (`/help` / `ctrl-c`)
  stay green — those live in the right-hint segment, the token segment
  only attaches on the left"
- ❌ "tests need updating"  
  ✅ "`tools_test.go` raw-string assertion will break" (mark expected
  break points explicitly)

Trivial changes may skip this section.

### Two-tier rollback

```
**Rollback**:

- Soft: feature flag / yaml off-switch → behavior reverts to current
- Hard: list each of the N edits to revert (period-separated)
```

When no flag exists, write only the hard rollback.

### "Carry renames through" applies to docs too

The Naming section already requires every call site, comment, and test
to change in one commit. **Spec docs that have already shipped (design.md
/ step-N.md) count as call sites** — update them too in the rename
commit; do not leave a half-renamed paper trail.

## Agent Working Discipline

Execution rules for LLM agents. **This rule set leans toward "careful",
not "fast"; trivial tasks are at your discretion.**

### 1. Think before you cut

- Do not assume. Do not hide your confusion. Put trade-offs on the
  surface.
- Before implementing: state your assumptions, ask when unsure; when
  several reasonable readings exist, list them and let the user pick —
  do not silently choose one; when a simpler approach exists, say so
  and push back if needed; on any unclear point — stop, name the
  confusion, ask.

### 2. Surgical changes

- Only touch **what must be touched**. Every diff line traces back to
  the user's request.
- Do not "casually optimize" neighboring code, comments, or
  formatting; do not refactor what is not broken; copy the existing
  style even if you dislike it.
- Clean up imports / variables / functions you yourself made unused;
  do not casually delete pre-existing dead code — flag it for the user
  to decide.

### 3. Goal-driven execution

Translate the task into a verifiable goal:

| Vague request    | Verifiable goal                                                       |
|------------------|-----------------------------------------------------------------------|
| "Add validation" | Write a test for invalid input first, then make it pass               |
| "Fix this bug"   | Write a test that reproduces the bug first, then make it pass         |
| "Refactor X"     | Make sure tests pass before and after                                 |

For multi-step tasks, write a short plan first:

```
1. [step] → verify: [check]
2. [step] → verify: [check]
3. [step] → verify: [check]
```

Strong success criteria → the agent can self-loop to completion. Weak
ones ("get it running") → repeated user pings.

### 4. Simplicity

`Core Principles` and `When Structs Earn Their Place` already cover the
ground: do not add features the user did not ask for, do not abstract
for a single use, do not handle errors for impossible cases, rewrite
when 200 lines compress to 50. If a senior engineer would call it
"over-engineered" at a glance, it is.

## When this does not apply

Public library APIs (forward compatibility via struct options), plug-in
systems (the DI package is the seam), domain-rich types (methods are
genuinely modeling something — `time.Time` / `*sql.Tx`).

The rule is "reduce indirection", not "abolish methods".
