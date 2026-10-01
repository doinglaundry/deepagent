package deepagents

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestModelEventsShareResponseIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan Event, 32)
	thread := newTestThread("thread", &RunConfig{Agent: Config{Model: &threadModel{}}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(ctx, schema.UserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	{
		err := accepted.RunHandle.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	var tokenID, finalID string
	for len(events) > 0 {
		e := <-events
		switch p := e.Payload.(type) {
		case LLMTokenChunk:
			tokenID = p.LLMResponseID
		case LLMEnd:
			finalID = p.LLMResponseID
		}
	}
	if tokenID == "" || tokenID != finalID {
		t.Fatalf("token=%q final=%q", tokenID, finalID)
	}
}
