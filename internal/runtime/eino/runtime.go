package eino

import "context"

type Runtime interface {
	Execute(ctx context.Context, prompt string) (Result, error)
	Name() string
}
