# AGENTS.md

Project-level coding-style brief for AI agents (Cursor, Codex, etc.) and
human contributors working on the eino-cli codebase. Loaded automatically
when an agent starts a task in this repo.

## Core philosophy

The repo's overall style is captured in one rule:

> **Structs are for data. Functions are for behavior. Minimize data
> passing. Minimize call-chain depth.**

Every other guideline below is a corollary.

### What this means in practice

1. **Structs hold state that travels together.**
   Not callback bags, not parameter objects, not "DI containers".
   A struct earns its existence when its fields share a lifetime and
   reading them together is meaningful. The moment a struct's only job
   is "carry these three callbacks from A to B", consider deleting it
   and inlining each callback at the call site instead.

2. **Behavior lives in plain top-level functions.**
   Not on receiver methods, not in callback fields, not behind layered
   `deps.X.Y(...)` lookups. A function's body should read
   top-to-bottom in one place.

3. **Minimize data passing.**
   Each parameter must justify itself. If three callers all pass the
   same derivation of `cfg`, internalize the derivation. If a struct
   has seven fields and production wires only two, the other five are
   dead weight — delete them.

4. **Minimize call-chain depth.**
   `factory → BuildRuntime → NewDeepAgentRuntime → MakeLeadAgent` is
   fine when each hop adds value (provider validation, history
   wiring, prompt assembly). It's noise when every hop just forwards
   args. Collapse pure pass-through layers.

## When to delete a struct

Trigger on any of these:

- Every production call site sets the same value for every field →
  internalize the defaults inside the consumer.
- The struct's fields are already on `cfg` or `RuntimeContext` →
  move them onto the existing host instead of duplicating.
- ≥50% of fields are dead in production and reserved for "future
  host integrations" → YAGNI; delete now, restore when the second
  host actually appears.
- The struct only exists to project a subset of another struct
  (DTO / view) → delete; let the consumer read the source directly.

## When to keep a struct

Don't over-correct. Keep the struct when:

- Fields are genuinely heterogeneous and travel together as
  *state* (e.g. `RuntimeContext` carries per-run flags that all
  vary independently and all need to be readable at any call site).
- The alternative is a function with 7+ parameters.
- Two serialization formats need to share one Go type — use struct
  tags (`json:"x" yaml:"x"`) instead of inventing a parallel DTO.

## Naming

- Functions named after the **return type's noun**, not the
  mechanism. `GetModelConfig` (returns `ModelConfig`), not
  `ResolveModelForAgent`.
- Function-typed fields end in `Func` (`HITLApprovalFunc`).
  Readers should know at a glance whether to call or store.
- **Outcome verbs over mechanism verbs**: `populate*` / `assemble*`
  over `set*` / `build*` when the function does meaningful work.
- **Full rename sweeps**: every call site, comment, and test in
  the same commit. Half-renames leave archeological layers.

## Comments

When a comment earns its line, keep it to **one line that answers "why"** —
the *what* is the code's job. Multi-line block comments above functions
or inline are a smell: usually they restate the syntax, or hint that the
function is doing two things and the comment is patching the seam.

```go
// Reset turn counter so /clear restarts numbering at 1.
func (t *Trace) ResetTurn() { t.turn.Store(0) }
```

When a single line genuinely isn't enough, ask first whether the comment
should become a name or a split function instead. Real exceptions:

- Package docs (`// Package x ...`) — they're read together in `go doc`.
- Structural conventions for a large literal (e.g. a prompt template's
  indentation rules) that aren't derivable from the literal itself.
- Non-obvious external constraints (protocol quirk, upstream bug) the
  reader can't recover from the code.

Default to deleting a comment before adding one. Especially: never narrate
freshly-generated code with paragraph-length explanations. If reviewers
need that much context, the explanation belongs in the commit message or
a spec doc, not next to the symbol.

## Validation flow

- Validate once, at the earliest authoritative layer
  (e.g. `config.normalizeConfig`). Downstream trusts the contract.
- Strict failures over soft degradation for *critical* invariants
  (missing default model, missing agent). Soft fallback for truly
  optional things (empty memory dir → no memory, not an error).
- Trust your callee. If `ValidateAgentName` rejects empty strings,
  callers don't pre-trim and re-check. Pre-checks hide where the
  real check happens.

## Error handling

- Differentiate by **what failed**, not where you noticed.
  `"default model %q not found"` beats `"config error"`.
- Every `if err != nil` branch either adds context with `%w` or
  returns the bare error because the caller already has the
  context. Don't double-wrap.

## Micro-patterns

### Copy → judge → replace

For in-place normalization (trim whitespace, supply default):

```go
trimmed := strings.TrimSpace(name)
if trimmed == "" {
    trimmed = defaultName
}
ac.Name = trimmed
```

Each line has one responsibility. Avoid the inverted form:

```go
// AVOID — assignment split across both branches
if strings.TrimSpace(ac.Name) == "" {
    ac.Name = name
} else {
    ac.Name = strings.TrimSpace(ac.Name)
}
```

### Custom UnmarshalYAML alias trick

When YAML shape ≠ Go shape (list ↔ map, legacy aliases), reach for
the alias trick on the Go target type instead of inventing a parallel
DTO:

```go
func (c *Config) UnmarshalYAML(node *yaml.Node) error {
    type alias Config
    aux := struct {
        alias  `yaml:",inline"`
        Models []ModelEntry `yaml:"models"` // YAML shape
    }{alias: alias(*c)}
    if err := node.Decode(&aux); err != nil {
        return err
    }
    *c = Config(aux.alias)
    c.Models = normalizeModels(aux.Models) // → Go shape
    return nil
}
```

One type, two formats, no DTO sprawl.

## Commit granularity

- **Pure renames travel separately from behavioral changes.**
  Reviewers verify a rename is mechanical without scanning logic
  edits. `git bisect` can pin regressions to the behavior commit.
- When a refactor naturally splits into "remove indirection layer"
  + "rewire consumers", give each its own commit.

## Audit checklist (hidden duplication)

When two structs / functions / config layers look similar:

1. Diff their **field / parameter sets**.
2. Diff their **lifetimes** (constructed / destroyed together?).
3. Diff their **consumers** (same callers? same call frequency?).

If all three diffs are small or empty → isomorphic. Merge.

## Pre-flight checklist for new structs / parameters

Before adding a struct, DI bag, or parameter, ask:

- [ ] Does this hold **data**, or just **callbacks / forwarded args**?
- [ ] Will every production call site pass the same value for ≥50%
      of the fields?
- [ ] Could the consumer derive each field from `cfg` /
      `RuntimeContext` directly?
- [ ] Is there an existing struct where these fields would
      naturally live?

If you can't answer **no** to all four, you're probably adding
indirection that the next refactor will remove.

## Anti-pattern catalog

| Smell | Fix |
|---|---|
| `XxxDeps` / `XxxOptions` struct with mostly-zero fields in production | Delete struct; pass the live fields as args, internalize the rest. |
| Two structs with identical field sets in different packages | Merge with shared tags; one type for both formats. |
| Function calls validate the same input the callee re-validates | Trust the callee; remove the outer check. |
| 4+ pass-through layers between caller and the function that does the work | Collapse pure forwarders; keep only layers that add value. |
| Struct field of type `func(...)` set once at construction and never reassigned | The function is a constant; inline it at the call site. |
| Multi-line block comment narrating what freshly-written code does | Compress to one line answering "why", or delete and let the names carry the meaning. |

## When *not* to apply

This style fits Go service code where call sites are countable and
behavior is the product. It's a poor fit for:

- **Public library APIs** where struct-based options preserve
  forward compatibility (`grpc.DialOption`, `http.Server` fields).
- **Plugin systems** where DI bags are the seam external code hooks
  into.
- **Domain-rich types** where methods on receivers genuinely model
  the object (`time.Time`, `*sql.Tx`).

The rule is "minimize indirection", not "eliminate methods".
