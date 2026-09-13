package agentthread

import (
	"context"
	"eino-cli/deepagent/core/graph"
)

type RunStartRequest struct {
	ThreadID  string
	RunID     string `json:"TurnID" yaml:"turnid"`
	Input     *Message
	InputMeta any
	Resume    *ResumeRunOptions
}
type RunConfigProvider func(ctx context.Context, request RunStartRequest) (*RunConfig, error)
type OnRunStartFunc func(ctx context.Context, request RunStartRequest) context.Context

func applyRunStart(ctx context.Context, onStart OnRunStartFunc, request RunStartRequest) (runCtx context.Context) {
	if onStart == nil {
		return ctx
	}
	request.Input = graph.CopyMessage(request.Input)
	request.Resume = copyResumeRunOptions(request.Resume)
	runCtx = onStart(ctx, request)
	if runCtx == nil {
		return ctx
	}
	return runCtx
}
func WithRunStartHook(onStart OnRunStartFunc) (option SubmitInputOption) {
	option = func(opts *submitInputOptions) { opts.OnRunStart = onStart }
	return option
}
