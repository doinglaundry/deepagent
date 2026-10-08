package thread

import (
	"context"
	"testing"
	"time"

	"eino-cli/deepagent/graph/execution"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"
	runpkg "eino-cli/deepagent/run"
)

func TestModelEventsShareResponseIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan runpkg.Event, 32)
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: &threadModel{}}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(ctx, messagepkg.NewUserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	waitErr := accepted.RunHandle.Wait(ctx)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	var tokenID, finalID string
	for len(events) > 0 {
		e := <-events
		switch p := e.Payload.(type) {
		case types.LLMTokenChunk:
			tokenID = p.LLMResponseID
		case types.LLMEnd:
			finalID = p.LLMResponseID
		}
	}
	if tokenID == "" || tokenID != finalID {
		t.Fatalf("token=%q final=%q", tokenID, finalID)
	}
}
