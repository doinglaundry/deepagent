# Legacy Thread removal evidence

The runnable Thread implementation is `core/runtime/agentthread`; history,
compaction and usage belong to `core/internal/conversation`. No Go imports of
`core/agentthread` remained at removal. Public Thread methods and input/config
options remain at the canonical path required by the final design.

| Removed test contract | Canonical evidence |
| --- | --- |
| Pending input stays in one Run | `runtime/agentthread/legacy_thread_contract_test.go`, original test ported |
| Interrupt leaves Thread reusable | Same file, original test ported |
| Follow-up resumes on new Thread | Same file, original test ported |
| Reload repairs dangling tool call | Same file, original test ported; additionally asserts no synthetic result persists |
| Model-call and Graph-step budgets | Same file, both original cases ported |
| Durable message-ID redelivery | `internal/conversation/legacy_history_contract_test.go`, original test ported |
| Concurrent input invalidates compaction | Same file; compactor fixture now uses one summary object, matching the canonical compact contract |
| Summary retains complete tool exchange and resumes | `internal/conversation/legacy_summary_contract_test.go`, original test ported |
| Cumulative usage across model calls | `internal/conversation/usage_test.go`, `graph/usage_test.go`, `host/runtime/usage_contract_test.go`; assert 5 then 10, new Run reset, and resumed cumulative usage |

Additional source-level differences restored before removal:

- Thread always repairs dangling calls in the model request; raw history stays unchanged.
- Multiple interrupts produce one typed batch event; edit review remains an approval event; custom payloads retain `InfoType`.
- External interrupts copy timeout and metadata; timeout cancels the run if a safe node boundary is unavailable. No Eino forced-timeout checkpoint is generated while a node still mutates local state.
- Repeated Graph Interrupt calls cannot invoke Eino's one-shot channel closure twice.
- Model EOF after context cancellation reports cancellation rather than empty response.

Evidence: `/tmp/core-thread-parity.log` reproduced missing request repair;
`/tmp/core-thread-interrupt-timeout.log` reproduced concurrent state serialization;
`/tmp/core-thread-interrupt-timeout-after.log` reproduced cancellation classification;
`/tmp/core-thread-interrupt-final.log` passed Graph, Thread and Host race suites.

This is a Thread migration record, not a full rewrite acceptance report. Managed
transport still needs the broader custom/batch interrupt audit, direct public Graph
forced-timeout handling needs the same safety audit, and terminal checkpoint
failure/outcome-unknown crash safety remains a separate open gate.
