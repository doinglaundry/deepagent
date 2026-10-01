package deepagents

func newTestThread(threadID string, cfg *RunConfig, events chan Event, options ThreadOptions) *Thread {
	thread, err := NewThread(ThreadConfig{ThreadID: threadID, RunConfig: cfg, Events: events, Options: options})
	if err != nil {
		panic(err)
	}
	return thread
}
