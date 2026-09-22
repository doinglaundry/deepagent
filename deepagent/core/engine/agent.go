// Package core executes a model/tools/continue Eino graph. It has no scheduling or transport dependencies.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	middleware "eino-cli/deepagent/core/middlewares/repairjson"
	"eino-cli/deepagent/protocol"
	"encoding/json"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ToolPolicy may reject a call or require a durable user interaction before it runs.
type ToolPolicy func(context.Context, tool.BaseTool, schema.ToolCall) (*protocol.Block, error)
type State struct {
	TotalTokens      int               `json:"total_tokens"`
	PromptTokens     int               `json:"prompt_tokens"`
	CompletionTokens int               `json:"completion_tokens"`
	Pending          []protocol.Input  `json:"pending,omitempty"`
	RunID            string            `json:"run_id"`
	ThreadID         string            `json:"thread_id"`
	Namespace        string            `json:"namespace"`
	CheckpointID     string            `json:"checkpoint_id,omitempty"`
	Messages         []*schema.Message `json:"messages"`
	MessageIDs       []string          `json:"message_ids"`
	Steps            int               `json:"steps"`
	ModelCalls       int               `json:"model_calls"`
	Calls            []schema.ToolCall `json:"calls,omitempty"`
	ToolIndex        int               `json:"tool_index"`
	Block            *protocol.Block   `json:"block,omitempty"`
	Resume           *protocol.Resume  `json:"resume,omitempty"`
}
type Config struct {
	Model                   model.ToolCallingChatModel
	Tools                   []tool.BaseTool
	MaxSteps, MaxModelCalls int
	PlanMode                bool
	ToolPolicy              ToolPolicy
	Emit                    func(protocol.Event)
	BeforeModel             func(context.Context, *State) error
	Continue                func(*State) bool
	Save                    func(context.Context, *State) error
}
type DeepAgent struct {
	c     Config
	model model.ToolCallingChatModel
	tools map[string]tool.BaseTool
	graph compose.Runnable[*State, *State]
}

func New(ctx context.Context, c Config) (*DeepAgent, error) {
	if c.Model == nil {
		return nil, errors.New("model required")
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = 100
	}
	if c.MaxModelCalls <= 0 {
		c.MaxModelCalls = 30
	}
	a := &DeepAgent{c: c, tools: map[string]tool.BaseTool{}}
	var infos []*schema.ToolInfo
	for _, t := range c.Tools {
		if c.PlanMode {
			ro, ok := t.(interface{ ReadOnly() bool })
			if !ok || !ro.ReadOnly() {
				continue
			}
		}
		inf, e := t.Info(ctx)
		if e != nil {
			return nil, e
		}
		if inf == nil || inf.Name == "" {
			return nil, errors.New("tool name required")
		}
		if _, ok := a.tools[inf.Name]; ok {
			return nil, fmt.Errorf("duplicate tool %s", inf.Name)
		}
		a.tools[inf.Name] = t
		infos = append(infos, inf)
	}
	var err error
	a.model, err = c.Model.WithTools(infos)
	if err != nil {
		return nil, err
	}
	g := compose.NewGraph[*State, *State]()
	for name, fn := range map[string]func(context.Context, *State) (*State, error){"model": a.modelNode, "tools": a.toolsNode, "continue": a.continueNode} {
		if err = g.AddLambdaNode(name, compose.InvokableLambda(fn)); err != nil {
			return nil, err
		}
	}
	if err = g.AddBranch(compose.START, compose.NewGraphBranch(func(_ context.Context, s *State) (string, error) {
		if s.ToolIndex < len(s.Calls) {
			return "tools", nil
		}
		return "model", nil
	}, map[string]bool{"model": true, "tools": true})); err != nil {
		return nil, err
	}
	if err = g.AddBranch("model", compose.NewGraphBranch(func(_ context.Context, s *State) (string, error) {
		if len(s.Calls) > 0 {
			return "tools", nil
		}
		if a.c.Continue != nil && a.c.Continue(s) {
			return "continue", nil
		}
		return compose.END, nil
	}, map[string]bool{"tools": true, "continue": true, compose.END: true})); err != nil {
		return nil, err
	}
	if err = g.AddBranch("tools", compose.NewGraphBranch(func(_ context.Context, s *State) (string, error) {
		if s.Block != nil {
			return compose.END, nil
		}
		return "model", nil
	}, map[string]bool{"model": true, compose.END: true})); err != nil {
		return nil, err
	}
	if err = g.AddEdge("continue", "model"); err != nil {
		return nil, err
	}
	a.graph, err = g.Compile(ctx, compose.WithMaxRunSteps(c.MaxSteps+3))
	return a, err
}
func (a *DeepAgent) Run(ctx context.Context, s *State) (*State, error) { return a.graph.Invoke(ctx, s) }
func (a *DeepAgent) step(s *State) error {
	if s.Steps >= a.c.MaxSteps {
		return fmt.Errorf("MaxSteps budget exhausted (%d)", a.c.MaxSteps)
	}
	s.Steps++
	return nil
}
func (a *DeepAgent) emit(e protocol.Event) {
	if a.c.Emit != nil {
		a.c.Emit(e)
	}
}
func (a *DeepAgent) save(ctx context.Context, s *State) error {
	if a.c.Save != nil {
		return a.c.Save(ctx, s)
	}
	return nil
}
func (a *DeepAgent) modelNode(ctx context.Context, s *State) (*State, error) {
	if e := a.step(s); e != nil {
		return s, e
	}
	if s.ModelCalls >= a.c.MaxModelCalls {
		return s, fmt.Errorf("MaxModelCalls budget exhausted (%d)", a.c.MaxModelCalls)
	}
	if a.c.BeforeModel != nil {
		if e := a.c.BeforeModel(ctx, s); e != nil {
			return s, e
		}
	}
	s.ModelCalls++
	r, e := a.model.Stream(ctx, s.Messages)
	if e != nil {
		return s, e
	}
	var closeOnce sync.Once
	closeReader := func() { closeOnce.Do(r.Close) }
	defer closeReader()
	response := protocol.NewID("response")
	var chunks []*schema.Message
	// Closing the reader wakes Recv even when a model implementation ignores cancellation.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			closeReader()
		case <-stop:
		}
	}()
	for {
		m, err := r.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return s, err
		}
		if err = ctx.Err(); err != nil {
			return s, err
		}
		if m == nil {
			continue
		}
		chunks = append(chunks, m)
		if m.Content != "" {
			a.emit(protocol.Event{Kind: protocol.EventTextDelta, ResponseID: response, Text: m.Content})
		}
	}
	if e = ctx.Err(); e != nil {
		return s, e
	}
	if len(chunks) == 0 {
		return s, errors.New("model returned empty stream")
	}
	m, e := schema.ConcatMessages(chunks)
	if e != nil {
		return s, e
	}
	m.Role = schema.Assistant
	s.Messages = append(s.Messages, m)
	s.Calls = m.ToolCalls
	s.ToolIndex = 0
	if m.Content != "" {
		a.emit(protocol.Event{Kind: protocol.EventText, ResponseID: response, Text: m.Content})
	}
	if m.ResponseMeta != nil && m.ResponseMeta.Usage != nil {
		u := m.ResponseMeta.Usage
		s.TotalTokens += u.TotalTokens
		s.PromptTokens += u.PromptTokens
		s.CompletionTokens += u.CompletionTokens
		data, _ := json.Marshal(map[string]int{"total_tokens": s.TotalTokens, "prompt_tokens": s.PromptTokens, "completion_tokens": s.CompletionTokens})
		a.emit(protocol.Event{Kind: protocol.EventTokens, Text: fmt.Sprint(s.TotalTokens), Data: data})
	}
	return s, a.save(ctx, s)
}
func (a *DeepAgent) continueNode(_ context.Context, s *State) (*State, error) { return s, a.step(s) }
func (a *DeepAgent) toolsNode(ctx context.Context, s *State) (*State, error) {
	if e := a.step(s); e != nil {
		return s, e
	}
	for s.ToolIndex < len(s.Calls) {
		if e := ctx.Err(); e != nil {
			return s, e
		}
		call := s.Calls[s.ToolIndex]
		t, ok := a.tools[call.Function.Name]
		if !ok {
			return s, fmt.Errorf("tool %q unavailable under current capability policy", call.Function.Name)
		}
		answer := ""
		denied := false
		resumed := s.Resume != nil
		if resumed {
			answer = s.Resume.Answer
			denied = !s.Resume.Approved
			s.Resume = nil
			s.Block = nil
		} else {
			var block *protocol.Block
			var err error
			if a.c.ToolPolicy != nil {
				block, err = a.c.ToolPolicy(ctx, t, call)
				if err != nil {
					return s, err
				}
			} else if req, ok := t.(interface{ RequiresApproval() bool }); ok && req.RequiresApproval() {
				block = &protocol.Block{Kind: "approval", Question: "Allow " + call.Function.Name + "?"}
			}
			if call.Function.Name == "ask_user" {
				var q struct {
					Question string
					Options  []string
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &q)
				if q.Question == "" {
					q.Question = call.Function.Arguments
				}
				block = &protocol.Block{Kind: "clarification", Question: q.Question, Options: q.Options}
			}
			if block != nil {
				block.RunID = s.RunID
				block.CheckpointID = protocol.NewID("checkpoint")
				block.InterruptID = protocol.NewID("interrupt")
				block.ToolName = call.Function.Name
				block.Arguments = call.Function.Arguments
				s.Block = block
				s.CheckpointID = block.CheckpointID
				return s, a.save(ctx, s)
			}
		}
		a.emit(protocol.Event{Kind: protocol.EventToolStarted, ToolCallID: call.ID, ToolName: call.Function.Name, Arguments: call.Function.Arguments})
		var out string
		var err error
		if resumed && call.Function.Name == "ask_user" {
			out = answer
		} else if denied {
			out = "User denied this tool call."
		} else {
			out, err = a.invoke(ctx, t, call)
		}
		if ctx.Err() != nil {
			return s, ctx.Err()
		}
		event := protocol.Event{Kind: protocol.EventToolCompleted, ToolCallID: call.ID, ToolName: call.Function.Name, Text: out}
		if err != nil {
			event.Error = err.Error()
			out = "Tool error: " + err.Error()
		}
		a.emit(event)
		if call.Function.Name == "update_plan" && err == nil {
			data := json.RawMessage(out)
			if !json.Valid(data) {
				data, _ = json.Marshal(map[string]any{"todos": []map[string]string{{"content": out, "status": "in_progress"}}})
			}
			a.emit(protocol.Event{Kind: protocol.EventPlan, Text: out, ToolCallID: call.ID, Data: data})
		}
		s.Messages = append(s.Messages, &schema.Message{Role: schema.Tool, Content: out, ToolCallID: call.ID, Name: call.Function.Name})
		s.ToolIndex++
		if err = a.save(ctx, s); err != nil {
			return s, err
		}
	}
	s.Calls = nil
	s.ToolIndex = 0
	return s, nil
}
func (a *DeepAgent) invoke(ctx context.Context, t tool.BaseTool, c schema.ToolCall) (string, error) {
	args, err := middleware.RepairJSON(c.Function.Arguments)
	if err != nil {
		return "", err
	}
	c.Function.Arguments = args
	if st, ok := t.(tool.StreamableTool); ok {
		r, e := st.StreamableRun(ctx, c.Function.Arguments)
		if e != nil {
			return "", e
		}
		var closeOnce sync.Once
		closeReader := func() { closeOnce.Do(r.Close) }
		defer closeReader()
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-ctx.Done():
				closeReader()
			case <-stop:
			}
		}()
		var b strings.Builder
		for {
			v, e := r.Recv()
			if errors.Is(e, io.EOF) {
				return b.String(), nil
			}
			if e != nil {
				return b.String(), e
			}
			b.WriteString(v)
			a.emit(protocol.Event{Kind: protocol.EventToolOutput, ToolCallID: c.ID, ToolName: c.Function.Name, Text: v})
		}
	}
	if it, ok := t.(tool.InvokableTool); ok {
		return it.InvokableRun(ctx, c.Function.Arguments)
	}
	return "", errors.New("tool does not implement an executable Eino tool interface")
}
