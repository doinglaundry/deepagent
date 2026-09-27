package graph

import (
	"context"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/compose"
	"strings"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

type childRunner struct{ config Config }

func NewChildRunner(cfg Config) tools.ChildRunner { return &childRunner{config: *cfg.Clone()} }
func (r *childRunner) Run(ctx context.Context, request tools.ChildRequest, emit types.ModelChunkSink) (*schema.Message, error) {
	if r.config.Depth >= 4 {
		return nil, fmt.Errorf("maximum child depth reached")
	}
	cfg := *r.config.Clone()
	agents := cfg.SubAgents
	cfg.ThreadID = ""
	cfg.RunID = ""
	cfg.Depth++
	cfg.Name = request.Name
	cfg.Conversation = nil
	cfg.CheckpointStore = nil
	executor, _ := ctx.Value(toolExecutorKey{}).(*toolExecutor)
	callID, _ := ctx.Value(toolCallIDKey{}).(string)
	var checkpoint *childCheckpointStore
	resuming := false
	if executor != nil && callID != "" {
		checkpoint = executor.childCheckpoint(callID)
		cfg.CheckpointStore = checkpoint
		_, resuming, _ = checkpoint.Get(ctx, "child")
		ctx = compose.AppendAddressSegment(ctx, compose.AddressSegmentTool, callID)
	}
	cfg.DrainInput = nil
	cfg.Emit = nil
	var chunks []*schema.Message
	emitted := false
	if emit != nil {
		cfg.Emit = func(ctx context.Context, event types.RuntimeEvent) error {
			if event.Kind == "llm_requesting" {
				chunks = nil
			}
			if event.Kind != "llm_end" {
				return nil
			}
			message, ok := event.Data.(*schema.Message)
			if !ok || len(message.ToolCalls) != 0 {
				return nil
			}
			var text strings.Builder
			for _, chunk := range chunks {
				text.WriteString(chunk.Content)
			}
			if text.String() != message.Content {
				chunks = []*schema.Message{message}
			}
			for _, chunk := range chunks {
				if err := emit(ctx, chunk); err != nil {
					return err
				}
			}
			emitted = true
			return nil
		}
	}
	cfg.Callbacks = nil
	cfg.SubAgents = nil
	cfg.MaxModelCalls = request.MaxModelCalls
	if cfg.MaxModelCalls <= 0 {
		cfg.MaxModelCalls = 8
	}
	cfg.MaxSteps = 64
	// Stateful middleware needs its explicit RunFactory; static prompts can be
	// shared. Each new agent invokes the factories before building its graph.
	cfg.Middlewares = nil
	for _, mw := range r.config.Middlewares {
		if _, ok := mw.(middleware.RunFactory); ok {
			cfg.Middlewares = append(cfg.Middlewares, mw)
			continue
		}
		if mw.BuildStateHandler() != nil {
			return nil, fmt.Errorf("child middleware %q requires NewRun", mw.Name())
		}
		cfg.Middlewares = append(cfg.Middlewares, mw)
	}
	if request.Name == "" {
		request.Name = "general-purpose"
		cfg.Name = request.Name
	}
	found := false
	for _, spec := range agents {
		if spec == nil || spec.Name != request.Name {
			continue
		}
		found = true
		if spec.SystemPrompt != "" {
			cfg.Prompts = []*schema.Message{schema.SystemMessage(spec.SystemPrompt)}
		}
		cfg.ReadOnlyToolsOnly = cfg.ReadOnlyToolsOnly || spec.ReadOnly
		if !spec.EnableFilesystem {
			cfg.FilesystemConfig = nil
		}
		if !spec.EnableWeb {
			cfg.WebConfig = nil
		}
		cfg.ToolMask = tools.CombineMasks(cfg.ToolMask, spec.ToolMask)
		if len(spec.Tools) > 0 {
			cfg.ToolDescriptors = make([]tools.Descriptor, 0, len(spec.Tools))
			for _, item := range spec.Tools {
				cfg.ToolDescriptors = append(cfg.ToolDescriptors, tools.Describe(item))
			}
		}
		if spec.MaxSteps > 0 {
			cfg.MaxSteps = spec.MaxSteps
		}
	}
	if !found {
		return nil, fmt.Errorf("unknown subagent %q", request.Name)
	}
	oldMask := cfg.ToolMask
	cfg.ToolMask = tools.CombineMasks(oldMask, func(_ context.Context, info *schema.ToolInfo) bool {
		return info.Name != "task" && info.Name != "ask_user"
	})
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		return nil, err
	}
	defer a.Close(context.Background())
	var input []*schema.Message
	if !resuming {
		input = append(input, schema.UserMessage(request.Prompt))
	}
	result, err := a.execute(ctx, input, nil, func(o *RunOptions) {
		if checkpoint != nil {
			o.CheckpointID = "child"
		}
		if emit != nil {
			o.chunk = func(_ context.Context, message *schema.Message) error {
				chunks = append(chunks, CopyMessage(message))
				return nil
			}
		}
	})
	if checkpoint != nil {
		if _, blocked := compose.ExtractInterruptInfo(err); blocked {
			_, exists, saveErr := checkpoint.Get(context.WithoutCancel(ctx), "child")
			if saveErr != nil {
				return nil, saveErr
			}
			if !exists {
				return nil, errors.New("child interrupted without checkpoint")
			}
			return nil, compose.CompositeInterrupt(ctx, nil, nil, err)
		}
		if err == nil {
			_ = checkpoint.Set(context.WithoutCancel(ctx), "child", nil)
		}
	}
	if err == nil && emit != nil && !emitted {
		err = emit(ctx, result)
	}
	return result, err
}
