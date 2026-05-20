# AGENTS.md

Project-level coding-style notes for the eino-cli repo, addressed to AI
agents (Cursor, Codex, etc.) and human contributors. Auto-loaded when an
agent opens a task here.

## Agent Working Discipline

Execution rules for LLM agents. Lean toward "careful", not "fast";
trivial tasks at your discretion.

1. **Think before you cut.** Do not assume; do not hide your confusion.
   State assumptions, ask when unsure. When several readings exist,
   list them and let the user pick — do not silently choose one. Push
   back when a simpler approach exists. On any unclear point — stop,
   name the confusion, ask.

2. **Surgical changes.** Only touch what must be touched — every diff
   line traces back to the user's request. Do not "casually optimize"
   neighbors; do not refactor what is not broken; copy the existing
   style even if you dislike it. Clean up imports / variables /
   functions you yourself made unused; flag pre-existing dead code, do
   not delete it.

3. **Goal-driven execution.** Translate vague requests into verifiable
   goals — "add validation" → "test for invalid input passes"; "fix
   bug" → "test reproducing the bug passes"; "refactor X" → tests pass
   before and after. For multi-step tasks, write a short numbered plan
   with per-step verification before starting. Strong criteria let the
   agent self-loop; weak ones ("get it running") cause repeated user
   pings.

4. **Simplicity.** Do not add features the user did not request, do
   not abstract for a single use, do not handle errors for impossible
   cases. Rewrite when 200 lines compress to 50.

## When this does not apply

Public library APIs (forward compatibility via struct options), plug-in
systems (the DI package is the seam), domain-rich types where methods
genuinely model something (`time.Time`, `*sql.Tx`).

The rule is "reduce indirection", not "abolish methods".

## Code Conventions

> **Structs hold data. Functions hold behavior. Pass less data. Push less stack.**

- A struct only holds state that must travel together — fields share a
  lifecycle and read meaningfully side by side. Reach for one when the
  alternative is an 4+ argument function; never define a parallel
  config DTO.
- Behavior lives in plain top-level functions: no receivers, no hidden
  callbacks, no `deps.X.Y(...)`. The body reads top-to-bottom in one
  place.
- Never use function-injection style for dependencies (`getenv func(...)`,
  `getwd func(...)`, clocks, command runners). Call the real dependency in
  production code; if tests need control, isolate the wrapper behind a small
  test-only helper instead of threading functions through normal call paths.
- When N callers all derive the same value from `cfg`, internalize the
  derivation in the callee. When seven struct fields only see two
  readers, delete the other five.
- Outermost interface to innermost executor stays within four layers
  when possible. Collapse pure-forwarding layers.

### Naming

- Variable name = the thing's meaning: `getID()` returns `id`, not
  `result` / `r` / `tmp`. No invented abbreviations; standard Go shorts
  (`ctx`, `err`, `cfg`, `fn`, `req`, `resp`, `i`, `ok`) stay.
- Function names start with a verb: `getX` / `buildX` / `applyX` /
  `parseX`. Avoid prepositions (`pathFor`), adjectives (`validX`), bare
  nouns (`userInfo`). Effective Go's "no Get prefix" rule is **not**
  adopted here — `getX` matches the semantics. Exceptions: standard Go
  `NewX` constructors and trivial property helpers (`utcNowISO`).
- Carry renames through the entire repo in one commit — call sites,
  comments, tests, **and any shipped spec docs**. No archaeological
  strata.

### Comments

- If the code is simple and reads cleanly, skip the comment.
- When a comment is needed, allow exactly one line capturing the core "why".

### Concise Assignment

Each line does one thing. Default-then-override beats split-on-condition:

```go
trimmed := strings.TrimSpace(name)
if trimmed == "" {
    trimmed = defaultName
}
ac.Name = trimmed
```

Use map / switch / lookup tables for either-or mapping; return early on
errors instead of stuffing the main path inside `else`.

### Commits

- Pure rename ≠ behavior change → split into two commits.
- "Remove the middle layer" + "rewire consumers" → one commit each.
- Each commit's diff fits in a single sentence.

## Spec Documents

`specs/<date>-<topic>/design.md` is where design lands. Three sections,
no more — extra headings do not map onto the reviewer's questions
(what / how / how does it fail safely):

```
### Goal           — current state + code refs + expected outcomes
### Implementation — file-grained steps + paste-ready Go code
### Tradeoffs      — design choice (cite this file) + side effects + rollback
```

- **Code references**: `` ```start:end:path `` for existing code (Cursor
  syntax), regular ```go``` fences for new code, `` `path::Symbol` ``
  for inline mentions.
- **Anchor each design choice** with a phrase from this file
  ("Behavior lives in plain top-level functions", "Push less stack").
  Do not write "best practice / clean / maintainable" — those carry no
  constraint.
- **Side effects must be concrete**: file names, test names, numbers.
  Write "`tools_test.go` raw assertion will break" instead of "tests
  need updating"; write "4 int64 struct copies" instead of "performance
  impact negligible". Trivial changes may skip this.
- **Rollback** has two tiers: soft (flag / yaml off-switch → behavior
  reverts) + hard (the N edits to revert, period-separated). Skip
  "soft" when no flag exists.
- **Implementation plans must be executable**: minimize background,
  motivation, and repeated explanation. Keep only necessary judgments
  and constraints. Do not describe implementation only in prose; include
  key code snippets covering new files, structs, functions, call sites,
  and tests. Keep verification steps short and concrete. The document
  should read like implementation notes, not a long design essay.
