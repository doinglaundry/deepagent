package graph

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type sharedCounterState struct{ value int }

func (s *sharedCounterState) MarshalRuntimeState() string { return strconv.Itoa(s.value) }
func (s *sharedCounterState) UnmarshalRuntimeState(raw string) error {
	value, err := strconv.Atoi(raw)
	if err == nil {
		s.value = value
	}
	return err
}

type childStateTool struct{}

func (childStateTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "bump"}, nil
}
func (childStateTool) InvokableRun(ctx context.Context, _ string, _ ...einotool.Option) (string, error) {
	state := types.StateFromContext(ctx)
	shared, ok := state.GetStateful("shared").(*sharedCounterState)
	if !ok {
		return "", fmt.Errorf("missing shared state")
	}
	if state.GetStateful("private") != nil {
		return "", fmt.Errorf("private parent state leaked")
	}
	shared.value++
	return "updated", nil
}

type childStateObserver struct {
	middleware.BaseMiddleware
	t        *testing.T
	observed *bool
}

func (*childStateObserver) Name() string                                     { return "child_state_observer" }
func (*childStateObserver) BeforeRun(context.Context, *types.RunState) error { return nil }
func (o *childStateObserver) AfterRun(_ context.Context, s *types.RunState, _ error) error {
	if s.Depth == 1 {
		*o.observed = true
		if _, exists := s.Extensions["middleware:shared"]; exists {
			o.t.Error("child persisted second shared state copy")
		}
	}
	return nil
}

type defaultSharedStateMiddleware struct {
	middleware.BaseMiddleware
	state *sharedCounterState
}

func (*defaultSharedStateMiddleware) Name() string { return "shared" }
func (*defaultSharedStateMiddleware) NewRun() middleware.Middleware {
	return &defaultSharedStateMiddleware{state: &sharedCounterState{value: 99}}
}
func (m *defaultSharedStateMiddleware) BuildStateHandler() types.RunTimeStateful { return m.state }

func TestChildAgent_ExplicitSharedStateHasOneCheckpointOwner(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%v", resume), func(t *testing.T) { testSharedChildState(t, resume) })
	}
}
func testSharedChildState(t *testing.T, resume bool) {
	ctx := context.Background()
	shared, private := &sharedCounterState{}, &sharedCounterState{value: 7}
	observed := false
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "task", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"update state"}`}}})},
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "bump", Function: schema.FunctionCall{Name: "bump", Arguments: "{}"}}})},
		{schema.AssistantMessage("child done", nil)},
		{schema.AssistantMessage("parent done", nil)},
	}}
	cfg := Config{Model: m, RunID: "parent-run", CheckpointStore: &checkpointMemory{}, CustomGraphState: map[string]types.RunTimeStateful{"shared": shared, "private": private}, SubAgentSharedCustomStateNames: []string{"shared"}, ToolDescriptors: []tools.Descriptor{{Tool: childStateTool{}}}, Middlewares: []middleware.Middleware{&defaultSharedStateMiddleware{}, &childStateObserver{t: t, observed: &observed}}}
	if resume {
		m.responses[1][0].ToolCalls = append(m.responses[1][0].ToolCalls, schema.ToolCall{ID: "approval", Function: schema.FunctionCall{Name: "approval", Arguments: "{}"}})
		cfg.ToolDescriptors = append(cfg.ToolDescriptors, tools.Descriptor{Tool: &namedCountingTool{name: "approval"}, RequiresApproval: true})
	}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	if resume {
		info, ok := compose.ExtractInterruptInfo(err)
		if !ok {
			t.Fatal(err)
		}
		if shared.value != 1 {
			t.Fatal("child did not update parent state before checkpoint")
		}
		shared = &sharedCounterState{}
		cfg.CustomGraphState = map[string]types.RunTimeStateful{"shared": shared, "private": private}
		cfg.Conversation = a.conversation
		a, err = New(ctx, WithConfig(&cfg))
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	}
	if err != nil {
		t.Fatal(err)
	}
	if shared.value != 1 || private.value != 7 || !observed {
		t.Fatalf("shared=%d private=%d observed=%v", shared.value, private.value, observed)
	}
	if string(a.state.Extensions["middleware:shared"]) != `"1"` {
		t.Fatalf("parent lost shared state: %s", a.state.Extensions["middleware:shared"])
	}
}
