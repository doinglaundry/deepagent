package thread

import (
	"context"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/graph/execution"
	messagepkg "eino-cli/deepagent/message"
	runpkg "eino-cli/deepagent/run"

	"github.com/cloudwego/eino/schema"
)

func TestRunConfigurationOnlyChangesWhenStartingRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: model}}, make(chan runpkg.Event, 100), ThreadOptions{})
	first, err := thread.SubmitInput(ctx, messagepkg.NewUserMessage("first"), WithPlan(true))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	providerCalled := false
	second, err := thread.SubmitInput(ctx, messagepkg.NewUserMessage("pending"), WithPlan(false), WithRunConfigProvider(func(context.Context, RunStartRequest) (*runpkg.Config, error) {
		providerCalled = true
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if second.Started || second.RunID != first.RunID || providerCalled {
		t.Fatal("pending input changed the active run configuration")
	}
	close(model.release)
	err = first.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	third, err := thread.SubmitInput(ctx, messagepkg.NewUserMessage("next"), WithPlan(false))
	if err != nil {
		t.Fatal(err)
	}
	if !third.Started || third.RunID == first.RunID || thread.config.EnablePlan {
		t.Fatal("new run did not select its own mode")
	}
	err = third.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Assert the model's actual requests, rather than reaching into Run's config.
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.inputs) != 3 {
		t.Fatalf("model calls=%d", len(model.inputs))
	}
	for i, request := range model.inputs {
		hasPlan := false
		for _, message := range request {
			if message.Role == schema.System && strings.Contains(message.Content, "<plan_mode>") {
				hasPlan = true
			}
		}
		if hasPlan != (i < 2) {
			t.Fatalf("request %d plan=%v", i, hasPlan)
		}
	}
}
