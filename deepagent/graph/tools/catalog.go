package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ToolDescriptor carries the tool and its execution capabilities from the constructor.
// ToolSet registers this descriptor; execution uses the original Eino Tool.
type ToolDescriptor struct {
	Tool             tool.BaseTool
	ReadOnly         bool
	RequiresApproval bool
	ParallelSafe     bool
	ReturnDirect     bool

	toolInfo *schema.ToolInfo // Cached by NewToolSet; not supplied by callers.
}

// ToolSet is immutable after construction; filtered sets share their tool definitions.
type ToolSet struct {
	toolsByName map[string]ToolDescriptor
	toolNames   []string
}

func NewToolSet(ctx context.Context, toolDescriptors []ToolDescriptor) (*ToolSet, error) {
	toolSet := &ToolSet{toolsByName: make(map[string]ToolDescriptor)}
	for _, toolDescriptor := range toolDescriptors {
		if toolDescriptor.Tool == nil {
			return nil, fmt.Errorf("nil tool")
		}
		toolInfo, err := toolDescriptor.Tool.Info(ctx)
		if err != nil {
			return nil, err
		}
		if toolInfo == nil || strings.TrimSpace(toolInfo.Name) == "" {
			return nil, fmt.Errorf("tool name is required")
		}
		_, exists := toolSet.toolsByName[toolInfo.Name]
		if exists {
			return nil, fmt.Errorf("duplicate tool %q", toolInfo.Name)
		}
		toolInfo, err = cloneToolInfo(toolInfo)
		if err != nil {
			return nil, err
		}
		toolDescriptor.toolInfo = toolInfo
		toolSet.toolsByName[toolInfo.Name] = toolDescriptor
		toolSet.toolNames = append(toolSet.toolNames, toolInfo.Name)
	}
	return toolSet, nil
}

func (toolSet *ToolSet) GetToolDescriptor(toolName string) (ToolDescriptor, bool) {
	toolDescriptor, exists := toolSet.toolsByName[toolName]
	return toolDescriptor, exists
}

func (toolSet *ToolSet) GetToolInfos(ctx context.Context) ([]*schema.ToolInfo, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	toolInfos := make([]*schema.ToolInfo, 0, len(toolSet.toolNames))
	for _, toolName := range toolSet.toolNames {
		toolInfo, err := cloneToolInfo(toolSet.toolsByName[toolName].toolInfo)
		if err != nil {
			return nil, err
		}
		toolInfos = append(toolInfos, toolInfo)
	}
	return toolInfos, nil
}

func (toolSet *ToolSet) FilterTools(ctx context.Context, readOnly bool, mask Mask) (*ToolSet, error) {
	filteredToolSet := &ToolSet{toolsByName: make(map[string]ToolDescriptor)}
	for _, toolName := range toolSet.toolNames {
		err := ctx.Err()
		if err != nil {
			return nil, err
		}
		toolDescriptor := toolSet.toolsByName[toolName]
		if readOnly && !toolDescriptor.ReadOnly {
			continue
		}
		toolInfo, err := cloneToolInfo(toolSet.toolsByName[toolName].toolInfo)
		if err != nil {
			return nil, err
		}
		if mask != nil && !mask(ctx, toolInfo) {
			continue
		}
		// The mask receives a copy; the immutable registered toolDescriptor can be shared.
		filteredToolSet.toolsByName[toolName] = toolDescriptor
		filteredToolSet.toolNames = append(filteredToolSet.toolNames, toolName)
	}
	return filteredToolSet, nil
}

func cloneToolInfo(toolInfo *schema.ToolInfo) (*schema.ToolInfo, error) {
	encodedToolInfo, err := json.Marshal(toolInfo)
	if err != nil {
		return nil, fmt.Errorf("encode tool schema: %w", err)
	}
	var clonedToolInfo schema.ToolInfo
	err = json.Unmarshal(encodedToolInfo, &clonedToolInfo)
	if err != nil {
		return nil, fmt.Errorf("decode tool schema: %w", err)
	}
	return &clonedToolInfo, nil
}
