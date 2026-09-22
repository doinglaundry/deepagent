package engine

import (
	"context"
	"eino-cli/deepagent/protocol"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"strings"
)

// NewSubagentTool creates an in-process research agent, distinct from distributed
// task tools. It receives only read-only capabilities and cannot recursively delegate.
func NewSubagentTool(c Config) tool.BaseTool { return &subagentTool{c: c} }

type subagentTool struct{ c Config }

func (*subagentTool) ReadOnly() bool { return true }
func (*subagentTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "internal_subagent", Desc: "Delegate a bounded read-only research question to an independent in-process agent. Use task tools for distributed persistent work.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"task": {Type: schema.String, Required: true}})}, nil
}
func (s *subagentTool) StreamableRun(ctx context.Context, arg string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	var in struct{ Task string }
	if e := json.Unmarshal([]byte(arg), &in); e != nil {
		return nil, e
	}
	if strings.TrimSpace(in.Task) == "" {
		return nil, errors.New("task required")
	}
	c := s.c
	c.PlanMode = true
	c.MaxSteps = 24
	c.MaxModelCalls = 8
	c.BeforeModel = nil
	c.Continue = nil
	c.Save = nil
	c.ToolPolicy = nil
	var ts []tool.BaseTool
	for _, t := range c.Tools {
		info, e := t.Info(ctx)
		if e != nil {
			return nil, e
		}
		if info.Name == "internal_subagent" || info.Name == "ask_user" {
			continue
		}
		if req, ok := t.(interface{ RequiresApproval() bool }); ok && req.RequiresApproval() {
			continue
		}
		ts = append(ts, t)
	}
	c.Tools = ts
	r, w := schema.Pipe[string](16)
	c.Emit = func(e protocol.Event) {
		if e.Kind == protocol.EventTextDelta {
			w.Send(e.Text, nil)
		}
	}
	go func() {
		defer w.Close()
		a, e := New(ctx, c)
		if e == nil {
			state := &State{RunID: protocol.NewID("internal"), Messages: []*schema.Message{schema.SystemMessage("Research the assigned question using read-only tools. Return factual findings, sources, and uncertainties. Do not attempt mutations or delegation."), schema.UserMessage(in.Task)}}
			_, e = a.Run(ctx, state)
			if e == nil && state.Block != nil {
				e = errors.New("internal subagent cannot request interactive approval")
			}
		}
		if e != nil {
			w.Send("", fmt.Errorf("internal subagent: %w", e))
		}
	}()
	return r, nil
}
