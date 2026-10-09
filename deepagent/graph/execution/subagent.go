package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"eino-cli/deepagent/graph/tools"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	yaml "gopkg.in/yaml.v3"
)

// LoadSubAgents reads dir/name/SUBAGENT.yaml in deterministic name order.
func LoadSubAgents(ctx context.Context, dir string) ([]*SubAgent, error) {
	contextErr := ctx.Err()
	if contextErr != nil {
		return nil, contextErr
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directoryEntries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var subAgents []*SubAgent
	for _, directoryEntry := range directoryEntries {
		iterationContextErr := ctx.Err()
		if iterationContextErr != nil {
			return nil, iterationContextErr
		}
		if !directoryEntry.IsDir() {
			continue
		}
		configPath := filepath.Join(directoryEntry.Name(), "SUBAGENT.yaml")
		file, err := root.Open(configPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("subagent %s: %w", configPath, err)
		}
		configBytes, readErr := io.ReadAll(io.LimitReader(file, 64*1024+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(configBytes) > 64*1024 {
			return nil, fmt.Errorf("subagent %s exceeds 64 KiB", configPath)
		}
		var subAgentConfig struct {
			Name             string   `yaml:"name"`
			SystemPrompt     string   `yaml:"system_prompt"`
			MaxSteps         int      `yaml:"max_steps"`
			EnableFilesystem bool     `yaml:"enable_filesystem"`
			EnableWeb        bool     `yaml:"enable_web"`
			ReadOnly         bool     `yaml:"read_only"`
			Tools            []string `yaml:"tools"`
		}
		decoder := yaml.NewDecoder(strings.NewReader(string(configBytes)))
		decoder.KnownFields(true)
		configDecodeErr := decoder.Decode(&subAgentConfig)
		if configDecodeErr != nil {
			return nil, fmt.Errorf("subagent %s: %w", configPath, configDecodeErr)
		}
		var extraDocument any
		decodeErr := decoder.Decode(&extraDocument)
		if decodeErr != io.EOF {
			return nil, fmt.Errorf("subagent %s must contain one YAML document", configPath)
		}
		if subAgentConfig.Name == "" {
			subAgentConfig.Name = directoryEntry.Name()
		}
		if strings.TrimSpace(subAgentConfig.Name) == "" || strings.TrimSpace(subAgentConfig.SystemPrompt) == "" || subAgentConfig.MaxSteps < 0 {
			return nil, fmt.Errorf("subagent %s requires a name, system_prompt and non-negative max_steps", configPath)
		}
		var mask agentmodel.Mask
		if subAgentConfig.Tools != nil {
			allowedToolNames := make(map[string]bool, len(subAgentConfig.Tools))
			for _, toolName := range subAgentConfig.Tools {
				if strings.TrimSpace(toolName) == "" || allowedToolNames[toolName] {
					return nil, fmt.Errorf("subagent %s has empty or duplicate tool name", configPath)
				}
				allowedToolNames[toolName] = true
			}
			mask = func(_ context.Context, toolInfo *schema.ToolInfo) bool { return allowedToolNames[toolInfo.Name] }
		}
		subAgents = append(subAgents, &SubAgent{Name: subAgentConfig.Name, SystemPrompt: subAgentConfig.SystemPrompt, MaxSteps: subAgentConfig.MaxSteps, EnableFilesystem: subAgentConfig.EnableFilesystem, EnableWeb: subAgentConfig.EnableWeb, ReadOnly: subAgentConfig.ReadOnly, ToolMask: mask})
	}
	return subAgents, nil
}

type SubAgent struct {
	Name, SystemPrompt                    string
	MaxSteps                              int
	EnableFilesystem, EnableWeb, ReadOnly bool
	ToolMask                              agentmodel.Mask
	Tools                                 []agentmodel.ToolDescriptor
}

// validateSubAgents checks the explicit list before task registration.
func validateSubAgents(subAgents []*SubAgent) error {
	subAgentNames := map[string]bool{}
	for _, subAgent := range subAgents {
		if subAgent == nil || strings.TrimSpace(subAgent.Name) == "" {
			return fmt.Errorf("subagent name is required")
		}
		if subAgentNames[subAgent.Name] {
			return fmt.Errorf("duplicate subagent name %q", subAgent.Name)
		}
		subAgentNames[subAgent.Name] = true
	}
	return nil
}

type childRunner struct{ config Config }

func NewChildRunner(config Config) agentmodel.ChildRunner {
	return &childRunner{config: *config.Clone()}
}

func (childRunner *childRunner) Run(ctx context.Context, childRequest agentmodel.ChildRequest, emit agentmodel.ModelChunkSink) (*agentmodel.Message, error) {
	if childRunner.config.Depth >= 4 {
		return nil, fmt.Errorf("maximum child depth reached")
	}
	config := *childRunner.config.Clone()
	subAgents := config.SubAgents
	config.ThreadID = ""
	config.RunID = ""
	config.Depth++
	config.Name = childRequest.Name
	config.Conversation = nil
	config.CheckpointStore = nil
	parentToolExecutor, _ := ctx.Value(toolExecutorKey{}).(*toolExecutor)
	callID, _ := ctx.Value(toolCallIDKey{}).(string)
	var checkpointStore *childCheckpointStore
	isResuming := false
	if parentToolExecutor != nil && callID != "" {
		checkpointStore = parentToolExecutor.getChildCheckpointStore(callID)
		config.CheckpointStore = checkpointStore
		_, isResuming, _ = checkpointStore.Get(ctx, "child")
		ctx = compose.AppendAddressSegment(ctx, compose.AddressSegmentTool, callID)
	}
	config.DrainInput = nil
	config.Emit = nil
	var messageChunks []*agentmodel.Message
	hasEmittedOutput := false
	if emit != nil {
		config.Emit = func(ctx context.Context, event agentmodel.RuntimeEvent) error {
			if event.Kind == "llm_requesting" {
				messageChunks = nil
			}
			if event.Kind == "llm_token" {
				payload, ok := event.Data.(agentmodel.LLMTokenChunk)
				message := payload.Message
				if ok {
					messageChunks = append(messageChunks, agentmodel.CopyMessage(message))
				}
				return nil
			}
			if event.Kind != "llm_end" {
				return nil
			}
			payload, ok := event.Data.(agentmodel.LLMEnd)
			message := payload.Message
			if !ok || len(message.ToolCalls) != 0 {
				return nil
			}
			var contentBuilder strings.Builder
			for _, chunk := range messageChunks {
				contentBuilder.WriteString(chunk.Content)
			}
			if contentBuilder.String() != message.Content {
				messageChunks = []*agentmodel.Message{message}
			}
			for _, chunk := range messageChunks {
				err := emit(ctx, chunk)
				if err != nil {
					return err
				}
			}
			hasEmittedOutput = true
			return nil
		}
	}
	config.Callbacks = nil
	config.SubAgents = nil
	config.MaxModelCalls = childRequest.MaxModelCalls
	if config.MaxModelCalls <= 0 {
		config.MaxModelCalls = 8
	}
	config.MaxSteps = 64
	// Stateful middleware needs its explicit RunFactory; static prompts can be
	// shared. Each new Run invokes the factories before building its Graph.
	config.Middlewares = nil
	for _, currentMiddleware := range childRunner.config.Middlewares {
		_, ok := currentMiddleware.(agentmodel.RunFactory)
		if ok {
			config.Middlewares = append(config.Middlewares, currentMiddleware)
			continue
		}
		if currentMiddleware.GetStateHandler() != nil {
			return nil, fmt.Errorf("child middleware %q requires NewRun", currentMiddleware.GetName())
		}
		config.Middlewares = append(config.Middlewares, currentMiddleware)
	}
	if childRequest.Name == "" {
		childRequest.Name = "general-purpose"
		config.Name = childRequest.Name
	}
	hasSubAgent := false
	for _, subAgent := range subAgents {
		if subAgent == nil || subAgent.Name != childRequest.Name {
			continue
		}
		hasSubAgent = true
		if subAgent.SystemPrompt != "" {
			config.Prompts = append(config.Prompts, agentmodel.NewSystemMessage(subAgent.SystemPrompt))
		}
		config.ReadOnlyToolsOnly = config.ReadOnlyToolsOnly || subAgent.ReadOnly
		if !subAgent.EnableFilesystem {
			config.FilesystemConfig = nil
		}
		if !subAgent.EnableWeb {
			config.WebConfig = nil
		}
		config.ToolMask = tools.CombineMasks(config.ToolMask, subAgent.ToolMask)
		if len(subAgent.Tools) > 0 {
			config.ToolDescriptors = append([]agentmodel.ToolDescriptor(nil), subAgent.Tools...)
		}
		if subAgent.MaxSteps > 0 {
			config.MaxSteps = subAgent.MaxSteps
		}
	}
	if !hasSubAgent {
		return nil, fmt.Errorf("unknown subagent %q", childRequest.Name)
	}
	oldMask := config.ToolMask
	config.ToolMask = tools.CombineMasks(oldMask, func(_ context.Context, toolInfo *schema.ToolInfo) bool {
		return toolInfo.Name != "task" && toolInfo.Name != "ask_user"
	})
	childGraph, err := New(ctx, WithConfig(&config))
	if err != nil {
		return nil, err
	}
	defer childGraph.Close(context.Background())
	var inputMessages []*agentmodel.Message
	if !isResuming {
		inputMessages = append(inputMessages, agentmodel.NewUserMessage(childRequest.Prompt))
	}
	response, err := childGraph.Invoke(ctx, inputMessages, func(runOptions *RunOptions) {
		if checkpointStore != nil {
			runOptions.CheckpointID = "child"
		}

	})
	if checkpointStore != nil {
		_, blocked := compose.ExtractInterruptInfo(err)
		if blocked {
			_, exists, saveErr := checkpointStore.Get(context.WithoutCancel(ctx), "child")
			if saveErr != nil {
				return nil, saveErr
			}
			if !exists {
				return nil, errors.New("child interrupted without checkpoint")
			}
			return nil, compose.CompositeInterrupt(ctx, nil, nil, err)
		}
		if err == nil {
			_ = checkpointStore.Set(context.WithoutCancel(ctx), "child", nil)
		}
	}
	if err == nil && emit != nil && !hasEmittedOutput {
		err = emit(ctx, response)
	}
	return response, err
}
