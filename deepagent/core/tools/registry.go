package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Descriptor describes capabilities; execution remains the Eino tool's responsibility.
type Descriptor struct {
	Tool             einotool.BaseTool
	ReadOnly         bool
	RequiresApproval bool
	ParallelSafe     bool
	ReturnDirect     bool
}

// Describe preserves capability metadata exposed by an Eino tool.
func Describe(tool einotool.BaseTool) Descriptor {
	d := Descriptor{Tool: tool}
	readOnly, ok := tool.(interface{ ReadOnly() bool })
	if ok {
		d.ReadOnly = readOnly.ReadOnly()
	}
	approval, ok := tool.(interface{ RequiresApproval() bool })
	if ok {
		d.RequiresApproval = approval.RequiresApproval()
	}
	parallel, ok := tool.(interface{ ParallelSafe() bool })
	if ok {
		d.ParallelSafe = parallel.ParallelSafe()
	}
	returnDirect, ok := tool.(interface{ ReturnDirect() bool })
	if ok {
		d.ReturnDirect = returnDirect.ReturnDirect()
	}
	return d
}

// Registry is configured before a run, then read concurrently by model and tools.
// Registry keeps tool schemas alongside their executable objects.
type Registry struct {
	entries map[string]Descriptor
	infos   map[string]*schema.ToolInfo
	order   []string
}

func NewRegistry(ctx context.Context, entries []Descriptor) (*Registry, error) {
	r := &Registry{entries: make(map[string]Descriptor), infos: make(map[string]*schema.ToolInfo)}
	for _, d := range entries {
		if d.Tool == nil {
			return nil, fmt.Errorf("nil tool")
		}
		info, err := d.Tool.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			return nil, fmt.Errorf("tool name is required")
		}
		if _, exists := r.entries[info.Name]; exists {
			return nil, fmt.Errorf("duplicate tool %q", info.Name)
		}
		info, err = cloneToolInfo(info)
		if err != nil {
			return nil, err
		}
		r.entries[info.Name] = d
		r.infos[info.Name] = info
		r.order = append(r.order, info.Name)
	}
	return r, nil
}

func (r *Registry) Lookup(name string) (Descriptor, bool) { d, ok := r.entries[name]; return d, ok }

func (r *Registry) ModelTools(ctx context.Context) ([]*schema.ToolInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]*schema.ToolInfo, 0, len(r.order))
	for _, name := range r.order {
		i, err := cloneToolInfo(r.infos[name])
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}

func (r *Registry) Filter(ctx context.Context, readOnly bool, mask Mask) (*Registry, error) {
	out := &Registry{entries: make(map[string]Descriptor), infos: make(map[string]*schema.ToolInfo)}
	for _, name := range r.order {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d := r.entries[name]
		if readOnly && !d.ReadOnly {
			continue
		}
		info, err := cloneToolInfo(r.infos[name])
		if err != nil {
			return nil, err
		}
		if mask != nil && !mask(ctx, info) {
			continue
		}
		// A mask can inspect its copy but cannot mutate the retained schema.
		info, err = cloneToolInfo(r.infos[name])
		if err != nil {
			return nil, err
		}
		out.entries[name] = d
		out.infos[name] = info
		out.order = append(out.order, name)
	}
	return out, nil
}

func cloneToolInfo(info *schema.ToolInfo) (*schema.ToolInfo, error) {
	raw, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("encode tool schema: %w", err)
	}
	var copy schema.ToolInfo
	if err := json.Unmarshal(raw, &copy); err != nil {
		return nil, fmt.Errorf("decode tool schema: %w", err)
	}
	return &copy, nil
}
