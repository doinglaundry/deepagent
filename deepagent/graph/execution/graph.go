package execution

import (
	"context"
	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"sync"
)

// Graph owns Eino nodes, model/tool execution and checkpoint state.
// Its invocation gate lets Close cancel and wait for local resource cleanup.
type Graph struct {
	runID             string
	resourcesOpen     bool
	resourcesClosing  chan struct{}
	resourcesCloseErr error
	model             model.ToolCallingChatModel
	eager             bool
	invoked           bool
	middlewares       []middleware.Middleware
	graphState        *types.GraphState
	cfg               Config
	graph             compose.Runnable[*types.RunState, *schema.Message]
	conversation      Conversation
	tools             *tools.ToolSet
	executor          *toolExecutor
	mu                sync.Mutex
	eventMu           sync.Mutex
	invoking          bool
	closed            bool
	cancel            context.CancelCauseFunc
	interrupt         func(...compose.GraphInterruptOption)
	done              chan struct{}
	state             *types.RunState
}

func New(ctx context.Context, opts ...Option) (*Graph, error) {
	cfg := Config{MaxSteps: 1000, Parallelism: 4, Name: "deepagent"}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.Model == nil {
		return nil, errors.New("model is required")
	}
	if cfg.Name == "" {
		cfg.Name = "deepagent"
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 1000
	}
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = 4
	}
	if cfg.MaxModelCalls < 0 {
		return nil, errors.New("max model calls must be >= 0")
	}
	err := validateSubAgents(cfg.SubAgents)
	if err != nil {
		return nil, err
	}
	history := cfg.Conversation
	if history == nil {
		history = conversation.New(cfg.ThreadID, nil, nil, nil)
	}
	a := &Graph{cfg: cfg, conversation: history, runID: cfg.RunID}
	if a.runID == "" {
		a.runID = uuid.NewString()
	}
	err = a.configure(ctx)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// Invoke executes the Eino Graph once. Resume restores its saved local state.
func (a *Graph) Invoke(ctx context.Context, input []*schema.Message, opts ...RunOptionFunc) (result *schema.Message, err error) {
	ctx, err = a.beginInvoke(ctx)
	if err != nil {
		return nil, err
	}
	options := RunOptions{}
	var state *types.RunState
	initialCheckpointSaved := false
	defer func() {
		if state != nil {
			err = a.finishInvoke(ctx, state, options, initialCheckpointSaved, err)
		} else {
			err = errors.Join(err, a.closeResources(ctx))
		}
		a.endInvoke(err)
	}()

	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	a.runID, err = a.resolveRunID(ctx, options)
	if err != nil {
		return nil, err
	}
	err = a.buildGraph(ctx)
	if err != nil {
		return nil, err
	}
	a.executor = newToolExecutor(a.tools, a.cfg.Parallelism, a.cfg.Policy)
	state = a.newRunState(input, options)
	if len(options.ResumeInterruptIDs) > 0 {
		ctx = compose.Resume(ctx, options.ResumeInterruptIDs...)
	}
	if len(options.ResumeData) > 0 {
		ctx = compose.BatchResumeWithData(ctx, options.ResumeData)
	}
	for _, data := range options.ResumeData {
		approval, ok := data.(*tools.ApprovalResult)
		if ok && approval != nil && approval.CancelRun {
			ctx = context.WithValue(ctx, approvalCancelKey{}, true)
			break
		}
	}
	ctx = context.WithValue(ctx, "graph", a)

	err = a.beforeRun(ctx, state)
	if err != nil {
		return nil, err
	}
	result, initialCheckpointSaved, err = a.invokeGraph(ctx, state, options)
	return result, err
}
