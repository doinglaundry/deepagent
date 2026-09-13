// Package hook contains lifecycle callbacks for one DeepAgent execution.
package hook

import (
	"context"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

type ModelOutput struct {
	Message  *schema.Message
	Stream   *schema.StreamReader[*schema.Message]
	IsStream bool
}

type BeforeAgentFunc func(context.Context) error
type AfterAgentFunc func(context.Context) error
type BeforeModelFunc func(context.Context, []*schema.Message, []*schema.Message, *types.GraphState) ([]*schema.Message, error)
type AfterModelFunc func(context.Context, ModelOutput, *types.GraphState) (ModelOutput, error)

type Hooks struct {
	BeforeAgent BeforeAgentFunc
	AfterAgent  AfterAgentFunc
	BeforeModel BeforeModelFunc
	AfterModel  AfterModelFunc
}

type HooksChain []Hooks

func (h HooksChain) BeforeAgent(ctx context.Context) error {
	for _, hooks := range h {
		if hooks.BeforeAgent == nil {
			continue
		}
		if err := hooks.BeforeAgent(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (h HooksChain) AfterAgent(ctx context.Context) error {
	for _, hooks := range h {
		if hooks.AfterAgent == nil {
			continue
		}
		if err := hooks.AfterAgent(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (h HooksChain) BeforeModel(ctx context.Context, initial, messages []*schema.Message, state *types.GraphState) ([]*schema.Message, error) {
	current := messages
	for _, hooks := range h {
		if hooks.BeforeModel == nil {
			continue
		}
		var err error
		current, err = hooks.BeforeModel(ctx, initial, current, state)
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

func (h HooksChain) AfterModel(ctx context.Context, output ModelOutput, state *types.GraphState) (ModelOutput, error) {
	current := output
	for _, hooks := range h {
		if hooks.AfterModel == nil {
			continue
		}
		var err error
		current, err = hooks.AfterModel(ctx, current, state)
		if err != nil {
			return ModelOutput{}, err
		}
	}
	return current, nil
}
