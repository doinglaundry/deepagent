package deepagents

func newTestThread(threadID string, cfg *RunConfig, events chan Event, options ThreadOptions, opts ...ThreadOption) *Thread {
	thread, err := NewThread(ThreadConfig{ThreadID: threadID, RunConfig: cfg, Events: events, Options: options, ThreadOptions: opts})
	if err != nil {
		panic(err)
	}
	return thread
}
