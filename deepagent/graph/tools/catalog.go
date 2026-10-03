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

	info *schema.ToolInfo // Cached by NewToolSet; not supplied by callers.
}

// ToolSet is immutable after construction; filtered sets share their tool definitions.
type ToolSet struct {
	toolsByName map[string]ToolDescriptor
	toolNames   []string
}

func NewToolSet(ctx context.Context, toolDescriptors []ToolDescriptor) (*ToolSet, error) {
	s := &ToolSet{toolsByName: make(map[string]ToolDescriptor)}
	for _, toolDescriptor := range toolDescriptors {
		if toolDescriptor.Tool == nil {
			return nil, fmt.Errorf("nil tool")
		}
		info, err := toolDescriptor.Tool.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			return nil, fmt.Errorf("tool name is required")
		}
		_, exists := s.toolsByName[info.Name]
		if exists {
			return nil, fmt.Errorf("duplicate tool %q", info.Name)
		}
		info, err = cloneToolInfo(info)
		if err != nil {
			return nil, err
		}
		toolDescriptor.info = info
		s.toolsByName[info.Name] = toolDescriptor
		s.toolNames = append(s.toolNames, info.Name)
	}
	return s, nil
}

func (s *ToolSet) Lookup(name string) (ToolDescriptor, bool) {
	toolDescriptor, ok := s.toolsByName[name]
	return toolDescriptor, ok
}

func (s *ToolSet) ModelTools(ctx context.Context) ([]*schema.ToolInfo, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	out := make([]*schema.ToolInfo, 0, len(s.toolNames))
	for _, name := range s.toolNames {
		i, err := cloneToolInfo(s.toolsByName[name].info)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}

func (s *ToolSet) Filter(ctx context.Context, readOnly bool, mask Mask) (*ToolSet, error) {
	out := &ToolSet{toolsByName: make(map[string]ToolDescriptor)}
	for _, name := range s.toolNames {
		err := ctx.Err()
		if err != nil {
			return nil, err
		}
		toolDescriptor := s.toolsByName[name]
		if readOnly && !toolDescriptor.ReadOnly {
			continue
		}
		info, err := cloneToolInfo(s.toolsByName[name].info)
		if err != nil {
			return nil, err
		}
		if mask != nil && !mask(ctx, info) {
			continue
		}
		// The mask receives a copy; the immutable registered toolDescriptor can be shared.
		out.toolsByName[name] = toolDescriptor
		out.toolNames = append(out.toolNames, name)
	}
	return out, nil
}

func cloneToolInfo(info *schema.ToolInfo) (*schema.ToolInfo, error) {
	raw, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("encode tool schema: %w", err)
	}
	var copy schema.ToolInfo
	err = json.Unmarshal(raw, &copy)
	if err != nil {
		return nil, fmt.Errorf("decode tool schema: %w", err)
	}
	return &copy, nil
}
