package eino

import (
	"context"
	"fmt"
	"strings"
)

type Runtime interface {
	Execute(ctx context.Context, prompt string) (Result, error)
}

type NoopRuntime struct {
	ModelName string
}

func NewNoopRuntime(modelName string) NoopRuntime {
	return NoopRuntime{ModelName: modelName}
}

func (n NoopRuntime) Execute(context.Context, string) (Result, error) {
	return SuccessResult(fmt.Sprintf("stub response from %s", strings.TrimSpace(n.ModelName))), nil
}
