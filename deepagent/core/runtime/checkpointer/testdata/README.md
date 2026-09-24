# Legacy checkpoint fixtures

`legacy_before_model.json` was produced on 2026-09-23 by the repository's old
`core.DeepAgent.Run`, using the pinned Eino v0.9.0-alpha.17 codec:

```go
Config{
    Model: fakeModel,
    CheckpointStore: capturingStore,
    InterruptBeforeNodes: []string{"model"},
}
agent.Run(ctx, []*schema.Message{schema.UserMessage("legacy question")},
    WithCheckpointID("fixture"))
```

The fixture is the actual stored snapshot, including its generated interrupt ID.
Its original middleware sidecar was `{}`. Tests separately exercise nonempty
sidecar import. This fixture covers a user-input boundary before the model;
it does not establish compatibility for tool interruptions or nested graphs.

`legacy_before_tools.json` and `legacy_before_tools_state.json` were generated
with the same old engine, with `MaxModelCalls: 5` and an interrupt before `tools`.
The model emitted `counter({})` with ID `legacy-call`; the tool's implementation
panicked if invoked, so fixture generation verifies it had not run. The sidecar
records one consumed model call. Its resume test supplies the durable user and
assistant history that the Thread normally reloads before resuming.

This is a before-node tool boundary, not an in-tool approval interruption.
