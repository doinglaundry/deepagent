package middleware

import (
	"context"
	"fmt"

	"eino-cli/deepagent/core/backend"
)

// File tools share the thread command service; this wrapper applies command
// classification before that service starts a job.
type classifiedCommands struct {
	backend.CommandService
}

func (c classifiedCommands) check(ctx context.Context, request backend.CommandRequest) error {
	classification := classifyShellScript(ctx, request.Command)
	if classification.Classification != classificationSafe {
		return fmt.Errorf("command denied (%s): %s", classification.Classification, classification.Reason)
	}
	return nil
}
func (c classifiedCommands) Start(ctx context.Context, request backend.CommandRequest) (string, error) {
	if err := c.check(ctx, request); err != nil {
		return "", err
	}
	return c.CommandService.Start(ctx, request)
}
func (c classifiedCommands) Execute(ctx context.Context, request backend.CommandRequest) (*backend.CommandResult, error) {
	if err := c.check(ctx, request); err != nil {
		return nil, err
	}
	return c.CommandService.Execute(ctx, request)
}
