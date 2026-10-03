package execution

import (
	"context"
	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	yaml "gopkg.in/yaml.v3"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LoadSubAgents reads dir/name/SUBAGENT.yaml in deterministic name order.
func LoadSubAgents(ctx context.Context, dir string) ([]*SubAgent, error) {
	ctxErrErr := ctx.Err()
	if ctxErrErr != nil {
		return nil, ctxErrErr
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var result []*SubAgent
	for _, entry := range entries {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(entry.Name(), "SUBAGENT.yaml")
		file, err := root.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("subagent %s: %w", path, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 64*1024+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) > 64*1024 {
			return nil, fmt.Errorf("subagent %s exceeds 64 KiB", path)
		}
		var spec struct {
			Name             string   `yaml:"name"`
			SystemPrompt     string   `yaml:"system_prompt"`
			MaxSteps         int      `yaml:"max_steps"`
			EnableFilesystem bool     `yaml:"enable_filesystem"`
			EnableWeb        bool     `yaml:"enable_web"`
			ReadOnly         bool     `yaml:"read_only"`
			Tools            []string `yaml:"tools"`
		}
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		decoderDecodeErr := decoder.Decode(&spec)
		if decoderDecodeErr != nil {
			return nil, fmt.Errorf("subagent %s: %w", path, decoderDecodeErr)
		}
		var extra any
		decodeErr := decoder.Decode(&extra)
		if decodeErr != io.EOF {
			return nil, fmt.Errorf("subagent %s must contain one YAML document", path)
		}
		if spec.Name == "" {
			spec.Name = entry.Name()
		}
		if strings.TrimSpace(spec.Name) == "" || strings.TrimSpace(spec.SystemPrompt) == "" || spec.MaxSteps < 0 {
			return nil, fmt.Errorf("subagent %s requires a name, system_prompt and non-negative max_steps", path)
		}
		var mask tools.Mask
		if spec.Tools != nil {
			names := make(map[string]bool, len(spec.Tools))
			for _, name := range spec.Tools {
				if strings.TrimSpace(name) == "" || names[name] {
					return nil, fmt.Errorf("subagent %s has empty or duplicate tool name", path)
				}
				names[name] = true
			}
			mask = func(_ context.Context, info *schema.ToolInfo) bool { return names[info.Name] }
		}
		result = append(result, &SubAgent{Name: spec.Name, SystemPrompt: spec.SystemPrompt, MaxSteps: spec.MaxSteps, EnableFilesystem: spec.EnableFilesystem, EnableWeb: spec.EnableWeb, ReadOnly: spec.ReadOnly, ToolMask: mask})
	}
	return result, nil
}

type SubAgent struct {
	Name, SystemPrompt                    string
	MaxSteps                              int
	EnableFilesystem, EnableWeb, ReadOnly bool
	ToolMask                              tools.Mask
	Tools                                 []tool.BaseTool
}

// validateSubAgents checks the explicit list before task registration.
func validateSubAgents(agents []*SubAgent) error {
	names := map[string]bool{}
	for _, spec := range agents {
		if spec == nil || strings.TrimSpace(spec.Name) == "" {
			return fmt.Errorf("subagent name is required")
		}
		if names[spec.Name] {
			return fmt.Errorf("duplicate subagent name %q", spec.Name)
		}
		names[spec.Name] = true
	}
	return nil
}

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
			if event.Kind == "llm_token" {
				payload, ok := event.Data.(types.LLMTokenChunk)
				message := payload.Message
				if ok {
					chunks = append(chunks, types.CopyMessage(message))
				}
				return nil
			}
			if event.Kind != "llm_end" {
				return nil
			}
			payload, ok := event.Data.(types.LLMEnd)
			message := payload.Message
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
				err := emit(ctx, chunk)
				if err != nil {
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
	// shared. Each new Run invokes the factories before building its Graph.
	cfg.Middlewares = nil
	for _, mw := range r.config.Middlewares {
		_, ok := mw.(middleware.RunFactory)
		if ok {
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
			cfg.Prompts = append(cfg.Prompts, schema.SystemMessage(spec.SystemPrompt))
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
			cfg.ToolDescriptors = make([]tools.ToolDescriptor, 0, len(spec.Tools))
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
	result, err := a.Invoke(ctx, input, func(o *RunOptions) {
		if checkpoint != nil {
			o.CheckpointID = "child"
		}

	})
	if checkpoint != nil {
		_, blocked := compose.ExtractInterruptInfo(err)
		if blocked {
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
