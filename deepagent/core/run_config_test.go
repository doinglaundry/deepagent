package deepagents

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestRunConfigurationOnlyChangesWhenStartingRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	thread := newTestThread("thread", &RunConfig{Agent: Config{Model: model}}, make(chan Event, 100), ThreadOptions{})
	first, err := thread.SubmitInput(ctx, schema.UserMessage("first"), WithPlan(true))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	providerCalled := false
	second, err := thread.SubmitInput(ctx, schema.UserMessage("pending"), WithPlan(false), WithRunConfigProvider(func(context.Context, RunStartRequest) (*RunConfig, error) {
		providerCalled = true
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if second.Started || second.RunID != first.RunID || providerCalled || !first.RunHandle.run.config.EnablePlan {
		t.Fatal("pending input changed the active run configuration")
	}
	close(model.release)
	err = first.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	third, err := thread.SubmitInput(ctx, schema.UserMessage("next"), WithPlan(false))
	if err != nil {
		t.Fatal(err)
	}
	if !third.Started || third.RunHandle.run.config.EnablePlan || thread.config.EnablePlan {
		t.Fatal("new run did not select its own mode")
	}
	err = third.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
}
