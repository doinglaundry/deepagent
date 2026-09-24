# Claude streaming race patch

Source: github.com/cloudwego/eino-ext/components/model/claude v0.1.17.
Only production files and module metadata are retained, with upstream Apache-2.0 headers and license.

ChatModel.Stream captured its named return variable err in the receive goroutine. Initial usage-only events followed by a tool-call delta caused ConcatMessages to write that variable concurrently with the method return. Use goroutine-local err_ for this merge instead. No protocol, model, or graph behavior is otherwise changed.

Regression: go test -race ./deepagent/core/modelhub -run TestClaudeStreamsToolCallsAndThinkingConfiguration -count=10.
Version v0.1.25 was inspected and still contained the same captured assignment. Remove this replace when adopting an upstream release with a verified fix.
