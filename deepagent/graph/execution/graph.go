package execution

import (
	"context"
	"errors"
	"sync"

	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
)

// Graph owns Eino nodes, model/tool execution and checkpoint state.
// Its invocation gate lets Close cancel and wait for local resource cleanup.
type Graph struct {
	runID             string
	resourcesOpen     bool
	resourcesClosing  chan struct{}
	resourcesCloseErr error
	chatModel         model.ToolCallingChatModel
	enableEagerTools  bool
	invoked           bool
	middlewares       []middleware.Middleware
	graphState        *types.GraphState
	config            Config
	runnable          compose.Runnable[*types.RunState, *messagepkg.Message]
	conversation      Conversation
	toolSet           *tools.ToolSet
	toolExecutor      *toolExecutor
	mu                sync.Mutex
	eventMu           sync.Mutex
	invoking          bool
	closed            bool
	cancel            context.CancelCauseFunc
	interrupt         func(...compose.GraphInterruptOption)
	done              chan struct{}
	runState          *types.RunState
}

func New(ctx context.Context, opts ...Option) (*Graph, error) {
	config := Config{MaxSteps: 1000, Parallelism: 4, Name: "deepagent"}
	for _, option := range opts {
		if option != nil {
			option(&config)
		}
	}
	if config.Model == nil {
		return nil, errors.New("model is required")
	}
	if config.Name == "" {
		config.Name = "deepagent"
	}
	if config.MaxSteps <= 0 {
		config.MaxSteps = 1000
	}
	if config.Parallelism <= 0 {
		config.Parallelism = 4
	}
	if config.MaxModelCalls < 0 {
		return nil, errors.New("max model calls must be >= 0")
	}
	err := validateSubAgents(config.SubAgents)
	if err != nil {
		return nil, err
	}
	threadConversation := config.Conversation
	if threadConversation == nil {
		threadConversation = conversation.New(config.ThreadID, nil, nil, nil)
	}
	graph := &Graph{config: config, conversation: threadConversation, runID: config.RunID}
	if graph.runID == "" {
		graph.runID = uuid.NewString()
	}
	err = graph.configure(ctx)
	if err != nil {
		return nil, err
	}
	return graph, nil
}

// Invoke executes the Eino Graph once. Resume restores its saved local state.
func (graph *Graph) Invoke(ctx context.Context, input []*messagepkg.Message, opts ...RunOptionFunc) (result *messagepkg.Message, err error) {
	ctx, err = graph.beginInvoke(ctx)
	if err != nil {
		return nil, err
	}
	runOptions := RunOptions{}
	var runState *types.RunState
	initialCheckpointSaved := false
	defer func() {
		if runState != nil {
			err = graph.finishInvoke(ctx, runState, runOptions, initialCheckpointSaved, err)
		} else {
			err = errors.Join(err, graph.closeResources(ctx))
		}
		graph.endInvoke(err)
	}()

	for _, opt := range opts {
		if opt != nil {
			opt(&runOptions)
		}
	}

	graph.runID, err = graph.resolveRunID(ctx, runOptions)
	if err != nil {
		return nil, err
	}
	err = graph.buildGraph(ctx)
	if err != nil {
		return nil, err
	}
	graph.toolExecutor = newToolExecutor(graph.toolSet, graph.config.Parallelism, graph.config.Policy)
	runState = graph.newRunState(input, runOptions)
	if len(runOptions.ResumeInterruptIDs) > 0 {
		ctx = compose.Resume(ctx, runOptions.ResumeInterruptIDs...)
	}
	if len(runOptions.ResumeData) > 0 {
		ctx = compose.BatchResumeWithData(ctx, runOptions.ResumeData)
	}
	for _, data := range runOptions.ResumeData {
		approval, ok := data.(*tools.ApprovalResult)
		if ok && approval != nil && approval.CancelRun {
			ctx = context.WithValue(ctx, approvalCancelKey{}, true)
			break
		}
	}
	ctx = context.WithValue(ctx, "graph", graph)

	err = graph.prepareRun(ctx, runState)
	if err != nil {
		return nil, err
	}
	result, initialCheckpointSaved, err = graph.invokeGraph(ctx, runState, runOptions)
	return result, err
}
