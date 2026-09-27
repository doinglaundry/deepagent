package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type DeepAgent struct {
	runID             string
	resourcesOpen     bool
	resourcesClosing  chan struct{}
	resourcesCloseErr error
	model             model.ToolCallingChatModel
	policy            tools.Policy
	eager             bool
	started           bool
	streamCancel      context.CancelFunc
	streamDone        chan struct{}
	middlewares       []middleware.Middleware
	graphState        *types.GraphState
	cfg               Config
	graph             compose.Runnable[*types.RunState, *schema.Message]
	conversation      Conversation
	registry          *tools.Registry
	executor          *toolExecutor
	emit              func(context.Context, types.RuntimeEvent) error
	drainInput        func(context.Context, string) ([]types.Input, bool, error)
	mu                sync.Mutex
	eventMu           sync.Mutex
	active            bool
	closed            bool
	cancel            context.CancelFunc
	interrupt         func(...compose.GraphInterruptOption)
	done              chan struct{}
	state             *types.RunState
	chunk             types.ModelChunkSink
}

func New(ctx context.Context, opts ...Option) (*DeepAgent, error) {
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
	if !cfg.DisableSubAgent {
		if err := loadSubAgents(ctx, &cfg); err != nil {
			return nil, err
		}
	}
	history := cfg.Conversation
	if history == nil {
		history = conversation.New(cfg.ThreadID, nil, nil, nil)
	}
	a := &DeepAgent{cfg: cfg, emit: cfg.Emit, drainInput: cfg.DrainInput, conversation: history}
	a.runID = cfg.RunID
	if a.runID == "" {
		a.runID = uuid.NewString()
	}
	if err := a.configureRun(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

// configureRun binds one run's middleware, tools, model and state handlers
// before compiling the graph with that run's checkpoint store.
func (a *DeepAgent) configureRun(ctx context.Context) (err error) {
	childConfig := *a.cfg.Clone()
	childConfig.RunID = a.runID

	middlewares, err := a.newRunMiddlewares(ctx, &childConfig)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), middlewares))
		}
	}()

	descriptors, err := a.collectToolDescriptors(ctx, middlewares, childConfig)
	if err != nil {
		return err
	}
	registry, err := a.buildToolRegistry(ctx, descriptors)
	if err != nil {
		return err
	}
	chatModel, err := a.bindModelTools(ctx, registry)
	if err != nil {
		return err
	}

	a.middlewares = middlewares
	a.registry = registry
	a.model = chatModel
	a.policy = a.buildPolicy()
	a.eager = a.canExecuteToolsEagerly(middlewares)
	a.graphState = a.buildRuntimeState(middlewares)

	err = a.buildGraph(ctx)
	if err != nil {
		return err
	}
	a.resourcesOpen = true
	return nil
}

// newRunMiddlewares creates run-local middleware. The caller owns the filesystem.
func (a *DeepAgent) newRunMiddlewares(ctx context.Context, childConfig *Config) (middlewares []middleware.Middleware, err error) {
	configured := make([]middleware.Middleware, 0, len(childConfig.Middlewares)+4)
	if childConfig.SkillLoader != nil {
		configured = append(configured, middleware.NewSkillMiddleware(childConfig.SkillLoader))
	}
	defer func() {
		if err == nil {
			return
		}
		err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), middlewares))
	}()

	filesystemConfig := childConfig.FilesystemConfig
	if filesystemConfig != nil && childConfig.Filesystem != nil {
		configured = append(configured, middleware.NewFilesystem(&middleware.FilesystemConfig{
			Filesystem: childConfig.Filesystem,
			ReadOnly:   filesystemConfig.ReadOnly, DisableExecute: filesystemConfig.DisableExecute,
			DisableApplyPatch: filesystemConfig.DisableApplyPatch, CommandTimeout: filesystemConfig.CommandTimeout,
		}))
	}
	if childConfig.WebConfig != nil {
		webConfig := *childConfig.WebConfig
		webConfig.ToolMask = tools.CombineMasks(webConfig.ToolMask, childConfig.ToolMask)
		configured = append(configured, middleware.NewWeb(&webConfig))
	}
	configured = append(configured, childConfig.Middlewares...)
	if childConfig.EnablePatchToolCalls {
		configured = append(configured, middleware.NewPatchToolCalls())
	}

	middlewares = make([]middleware.Middleware, 0, len(configured))
	for _, source := range configured {
		if source == nil {
			continue
		}
		instance := source
		factory, ok := source.(middleware.RunFactory)
		if ok {
			instance = factory.NewRun()
		}
		if instance == nil {
			return middlewares, fmt.Errorf("middleware factory returned nil")
		}
		middlewares = append(middlewares, instance)
	}
	return middlewares, nil
}

// collectToolDescriptors keeps the child configuration free of tools created
// from the parent's mutable middleware instances.
func (a *DeepAgent) collectToolDescriptors(ctx context.Context, middlewares []middleware.Middleware, childConfig Config) ([]tools.Descriptor, error) {
	descriptors := append([]tools.Descriptor(nil), childConfig.ToolDescriptors...)
	for _, mw := range middlewares {
		extra, err := mw.Tools(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range extra {
			descriptors = append(descriptors, tools.Describe(item))
		}
	}
	if childConfig.HITLConfig != nil && childConfig.HITLConfig.NeedFollowUpTool {
		found, err := hasToolNamed(ctx, descriptors, "ask_user")
		if err != nil {
			return nil, err
		}
		if !found {
			descriptors = append(descriptors, tools.Descriptor{Tool: tools.GetFollowUpTool(), ReadOnly: true})
		}
	}
	if !childConfig.DisableSubAgent {
		found, err := hasToolNamed(ctx, descriptors, "task")
		if err != nil {
			return nil, err
		}
		if !found {
			names := []string{"general-purpose"}
			for _, spec := range childConfig.SubAgents {
				if spec.Name != "general-purpose" {
					names = append(names, spec.Name)
				}
			}
			runner := NewChildRunner(childConfig)
			task := tools.NewTaskTool(runner, names...)
			if childConfig.EnableSubAgentTaskStreaming {
				task = tools.NewStreamingTaskTool(runner, names...)
			}
			descriptors = append(descriptors, tools.Descriptor{
				Tool: task, ParallelSafe: true, ReadOnly: childConfig.ReadOnlyToolsOnly,
			})
		}
	}
	return descriptors, nil
}

func hasToolNamed(ctx context.Context, descriptors []tools.Descriptor, name string) (bool, error) {
	for _, descriptor := range descriptors {
		if descriptor.Tool == nil {
			continue
		}
		info, err := descriptor.Tool.Info(ctx)
		if err != nil {
			return false, err
		}
		if info != nil && info.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (a *DeepAgent) buildToolRegistry(ctx context.Context, descriptors []tools.Descriptor) (*tools.Registry, error) {
	registry, err := tools.NewRegistry(ctx, descriptors)
	if err != nil {
		return nil, err
	}
	if a.cfg.ToolInfoRewriter != nil {
		err = registry.RewriteInfo(ctx, a.cfg.ToolInfoRewriter)
		if err != nil {
			return nil, err
		}
	}
	return registry.Filter(ctx, a.cfg.ReadOnlyToolsOnly, a.cfg.ToolMask)
}

func (a *DeepAgent) bindModelTools(ctx context.Context, registry *tools.Registry) (model.ToolCallingChatModel, error) {
	infos, err := registry.ModelTools(ctx)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return a.cfg.Model, nil
	}
	return a.cfg.Model.WithTools(infos)
}

func (a *DeepAgent) buildPolicy() tools.Policy {
	if a.cfg.HITLConfig == nil || len(a.cfg.HITLConfig.ToolPolicyGates) == 0 {
		return a.cfg.Policy
	}
	return policyWithGates(a.cfg.Policy, a.cfg.HITLConfig.ToolPolicyGates)
}

func (a *DeepAgent) canExecuteToolsEagerly(middlewares []middleware.Middleware) bool {
	if !a.cfg.EnableStreamToolCall || a.cfg.ToolNodePreHandler != nil {
		return false
	}
	for _, mw := range middlewares {
		guard, ok := mw.(interface{ RequiresCompleteModelResponse() bool })
		if ok && guard.RequiresCompleteModelResponse() {
			return false
		}
	}
	return true
}

func (a *DeepAgent) buildRuntimeState(middlewares []middleware.Middleware) *types.GraphState {
	state := types.NewGraphState(nil)
	for _, mw := range middlewares {
		handler := mw.BuildStateHandler()
		if handler != nil {
			state.RegisterStateful(mw.Name(), handler)
		}
	}
	// Custom handlers override middleware defaults. Child-shared handlers are
	// owned by the parent and must not be persisted in the child checkpoint.
	for name, handler := range a.cfg.CustomGraphState {
		if handler == nil {
			continue
		}
		if a.cfg.Depth > 0 && slices.Contains(a.cfg.SubAgentSharedCustomStateNames, name) {
			state.RegisterRuntimeOnlyStateful(name, handler)
			continue
		}
		state.RegisterStateful(name, handler)
	}
	return state
}

func closeMiddlewareResources(ctx context.Context, middlewares []middleware.Middleware) error {
	var err error
	for i := len(middlewares) - 1; i >= 0; i-- {
		if closer, ok := middlewares[i].(middleware.ResourceCloser); ok {
			err = errors.Join(err, closer.Close(ctx))
		}
	}
	return err
}
func (a *DeepAgent) closeResources(ctx context.Context) error {
	a.mu.Lock()
	if closing := a.resourcesClosing; closing != nil {
		a.mu.Unlock()
		select {
		case <-closing:
			a.mu.Lock()
			err := a.resourcesCloseErr
			a.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !a.resourcesOpen {
		a.mu.Unlock()
		return nil
	}
	a.resourcesOpen = false
	closing := make(chan struct{})
	a.resourcesClosing = closing
	middlewares := a.middlewares
	a.mu.Unlock()
	err := closeMiddlewareResources(context.WithoutCancel(ctx), middlewares)
	a.mu.Lock()
	a.resourcesCloseErr = err
	a.resourcesClosing = nil
	close(closing)
	a.mu.Unlock()
	return err
}
func (a *DeepAgent) Run(ctx context.Context, input []*schema.Message, opts ...RunOptionFunc) (*schema.Message, error) {
	return a.execute(ctx, input, nil, opts...)
}
func (a *DeepAgent) Stream(ctx context.Context, input []*schema.Message, opts ...RunOptionFunc) (*schema.StreamReader[*schema.Message], error) {
	raw, writer := schema.Pipe[*schema.Message](0)
	raw.SetAutomaticClose()
	streamCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	if a.closed || a.active || a.streamDone != nil {
		a.mu.Unlock()
		cancel()
		raw.Close()
		writer.Close()
		return nil, errors.New("agent is closed or already running")
	}
	streamDone := make(chan struct{})
	a.streamDone = streamDone
	a.streamCancel = cancel
	a.mu.Unlock()
	stopClose := context.AfterFunc(streamCtx, raw.Close)
	chunks := make(chan *schema.Message)
	done := make(chan error, 1)
	options := append([]RunOptionFunc(nil), opts...)
	options = append(options, func(o *RunOptions) {
		o.streamDone = streamDone
		o.chunk = func(ctx context.Context, message *schema.Message) error {
			select {
			case chunks <- message:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	go func() { _, err := a.execute(streamCtx, input, nil, options...); done <- err }()
	go func() {
		defer func() {
			a.mu.Lock()
			writer.Close()
			a.streamCancel = nil
			a.streamDone = nil
			close(streamDone)
			a.mu.Unlock()
		}()
		defer cancel()
		defer stopClose()
		// This Eino version exposes consumer closure only through Send. A private
		// nil probe detects Close even while the provider has produced no tokens.
		// Conversion strips probes; consumers see only actual model/tool messages.
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case chunk := <-chunks:
				if writer.Send(chunk, nil) {
					cancel()
					<-done
					return
				}
			case err := <-done:
				if err != nil {
					writer.Send(nil, err)
				}
				return
			case <-ticker.C:
				if writer.Send(nil, nil) {
					cancel()
					<-done
					return
				}
			case <-streamCtx.Done():
				<-done
				return
			}
		}
	}()
	return schema.StreamReaderWithConvert(raw, func(message *schema.Message) (*schema.Message, error) {
		if message == nil {
			return nil, schema.ErrNoValue
		}
		return message, nil
	}), nil
}
func (a *DeepAgent) execute(ctx context.Context, input []*schema.Message, resume *ResumeOptions, opts ...RunOptionFunc) (result *schema.Message, err error) {
	options := RunOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	if resume != nil {
		options.CheckpointID = resume.CheckpointID
		options.ResumeInterruptIDs = resume.InterruptIDs
		options.ResumeData = resume.Data
	}
	a.mu.Lock()
	if a.closed || a.active || (a.streamDone != nil && options.streamDone != a.streamDone) {
		a.mu.Unlock()
		return nil, errors.New("agent is closed or already running")
	}
	runID := a.runID
	if a.cfg.RunID == "" {
		if a.started {
			runID = uuid.NewString()
		}
		if options.CheckpointID != "" && a.cfg.CheckpointStore != nil && !options.ForceNewRun {
			raw, exists, readErr := a.cfg.CheckpointStore.Get(ctx, options.CheckpointID)
			if readErr != nil {
				a.mu.Unlock()
				return nil, readErr
			}
			if exists {
				var envelope checkpointer.Envelope
				if decodeErr := json.Unmarshal(raw, &envelope); decodeErr != nil {
					a.mu.Unlock()
					return nil, decodeErr
				}
				if envelope.Version == 1 {
					if envelope.ThreadID != a.cfg.ThreadID || envelope.RunID == "" {
						a.mu.Unlock()
						return nil, fmt.Errorf("checkpoint identity mismatch")
					}
					runID = envelope.RunID
				}
			}
		}
	}
	rebuild := a.started || runID != a.runID
	a.runID = runID
	if rebuild {
		if a.resourcesOpen {
			a.resourcesOpen = false
			if err := closeMiddlewareResources(context.WithoutCancel(ctx), a.middlewares); err != nil {
				a.mu.Unlock()
				return nil, err
			}
		}
		if err := a.configureRun(ctx); err != nil {
			a.mu.Unlock()
			return nil, err
		}
	}
	a.started = true
	a.active = true
	a.state = nil
	a.done = make(chan struct{})
	ctx, a.cancel = context.WithCancel(ctx)
	ctx, a.interrupt = compose.WithGraphInterrupt(ctx)
	a.chunk = options.chunk
	a.executor = newToolExecutor(runID, a.registry, a.cfg.Parallelism, a.policy)
	a.executor.middlewares = a.middlewares
	var state *types.RunState
	initialCheckpointSaved := false
	if a.cfg.CheckpointStore != nil && options.CheckpointID != "" && (options.WriteToCheckpointID == "" || options.WriteToCheckpointID == options.CheckpointID) {
		store := checkpointer.New(a.cfg.CheckpointStore, a.cfg.ThreadID, runID, "core-graph-v1")
		var fenceMu sync.Mutex
		a.executor.beforeInvoke = func(ctx context.Context, call types.ToolCall) error {
			fenceMu.Lock()
			defer fenceMu.Unlock()
			// Fresh local state is the input pointer returned by our Eino state
			// generator. A restored state is decoded from the checkpoint instead.
			restored := types.RunStateFromContext(ctx) != state
			if err := store.FenceTool(ctx, options.CheckpointID, call, restored); err != nil {
				return fmt.Errorf("persist tool execution fence: %w", err)
			}
			return nil
		}
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		executor := a.executor
		a.mu.Unlock()
		cleanupErr := executor.cancel(context.Background())
		err = errors.Join(err, cleanupErr, a.closeResources(ctx))
		current := a.state
		if current == nil {
			current = state
		}
		// A terminal failure must not discard accepted inputs embedded in a
		// checkpoint. Interrupts retain them in the Eino snapshot.
		if _, interrupted := compose.ExtractInterruptInfo(err); err != nil && !interrupted && current != nil {
			pending := hasPendingInputs(current)
			if pending {
				saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				err = errors.Join(err, a.persistInputs(saveCtx, current))
				cancel()
			}
		}
		markRunError(ctx, current, err)
		_, interrupted := compose.ExtractInterruptInfo(err)
		// Only finalize a state actually entered by the Graph. A rejected resume
		// or a BeforeRun failure must not overwrite the saved state with a new one.
		if !interrupted && a.state != nil && current != nil && a.cfg.CheckpointStore != nil && (!options.ForceNewRun || initialCheckpointSaved) {
			checkpointID := options.CheckpointID
			if options.WriteToCheckpointID != "" {
				checkpointID = options.WriteToCheckpointID
			}
			if checkpointID != "" {
				saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				required := current != state && checkpointID == options.CheckpointID
				saveErr := checkpointer.New(a.cfg.CheckpointStore, a.cfg.ThreadID, runID, "core-graph-v1").Finalize(saveCtx, checkpointID, current, required)
				cancel()
				if saveErr != nil {
					err = errors.Join(err, fmt.Errorf("persist terminal checkpoint: %w", saveErr))
				}
			}
		}
		if err == nil && current != nil {
			err = a.event(ctx, current, "turn_end", "", nil)
		}
		markRunError(ctx, current, err)
		a.mu.Lock()
		a.cancel()
		a.active = false
		a.interrupt = nil
		a.chunk = nil
		close(a.done)
		a.mu.Unlock()
	}()
	state = &types.RunState{Version: 1, ThreadID: a.cfg.ThreadID, RunID: runID, AgentName: a.cfg.Name, Depth: a.cfg.Depth, Phase: types.PhasePreparing}
	for i, message := range input {
		if message != nil {
			entry := types.Input{Message: message}
			if i < len(options.InputMeta) {
				entry.Meta = options.InputMeta[i]
			}
			state.Consumed = append(state.Consumed, entry)
		}
	}
	invokeOpts := append([]compose.Option(nil), options.composeOpts...)
	if len(a.cfg.Callbacks) > 0 {
		invokeOpts = append(invokeOpts, compose.WithCallbacks(a.cfg.Callbacks...))
	}
	if options.CheckpointID != "" {
		invokeOpts = append(invokeOpts, compose.WithCheckPointID(options.CheckpointID))
	}
	if options.WriteToCheckpointID != "" {
		invokeOpts = append(invokeOpts, compose.WithWriteToCheckPointID(options.WriteToCheckpointID))
	}
	if options.ForceNewRun {
		invokeOpts = append(invokeOpts, compose.WithForceNewRun())
	}
	if len(options.ResumeInterruptIDs) > 0 {
		ctx = compose.Resume(ctx, options.ResumeInterruptIDs...)
	}
	if len(options.ResumeData) > 0 {
		ctx = compose.BatchResumeWithData(ctx, options.ResumeData)
	}
	ctx = context.WithValue(ctx, "deep_agent", a)
	ctx = types.NewStateContext(ctx, a.graphState)
	defer func() {
		err = errors.Join(err, a.cfg.Hooks.AfterAgent(ctx))
		for i := len(a.middlewares) - 1; i >= 0; i-- {
			if mw, ok := a.middlewares[i].(middleware.RunMiddleware); ok {
				current := a.state
				if current == nil {
					current = state
				}
				err = errors.Join(err, mw.AfterRun(ctx, current, err))
			}
		}
	}()
	for _, mw := range a.middlewares {
		if err := mw.BeforeAgent(ctx); err != nil {
			return nil, err
		}
		if lifecycle, ok := mw.(middleware.RunMiddleware); ok {
			if err := lifecycle.BeforeRun(ctx, state); err != nil {
				return nil, err
			}
		}
	}
	if err := a.cfg.Hooks.BeforeAgent(ctx); err != nil {
		return nil, err
	}
	if a.cfg.CheckpointStore != nil && options.CheckpointID != "" && (options.WriteToCheckpointID == "" || options.WriteToCheckpointID == options.CheckpointID) {
		ctx = context.WithValue(ctx, initialCheckpointKey{}, state)
	}
	result, err = a.graph.Invoke(types.WithRunState(ctx, state), state, invokeOpts...)
	if info, interrupted := compose.ExtractInterruptInfo(err); interrupted && len(info.InterruptContexts) == 1 {
		if _, initial := info.InterruptContexts[0].Info.(*initialCheckpoint); initial {
			initialCheckpointSaved = true
			// This is a persistence boundary, not a user-visible blocked Run.
			// Model/tool work and lifecycle hooks have not been repeated.
			resumeCtx := compose.Resume(ctx, info.InterruptContexts[0].ID)
			// In the pinned Eino version the last option supplies ForceNewRun.
			// A checkpoint-ID option resets it, so this invocation loads the new
			// cursor instead of starting another forced run.
			resumeOpts := append(append([]compose.Option(nil), invokeOpts...), compose.WithCheckPointID(options.CheckpointID))
			result, err = a.graph.Invoke(types.WithRunState(resumeCtx, state), state, resumeOpts...)
		}
	}
	if info, interrupted := compose.ExtractInterruptInfo(err); interrupted {
		id := options.CheckpointID
		if options.WriteToCheckpointID != "" {
			id = options.WriteToCheckpointID
		}
		if saveErr := a.savePending(ctx, id, info); saveErr != nil {
			return nil, saveErr
		}
	}
	return result, err
}
func (a *DeepAgent) Close(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	if a.streamCancel != nil {
		a.streamCancel()
	}
	var done chan struct{}
	if a.active {
		a.cancel()
		done = a.done
	}
	streamDone := a.streamDone
	a.mu.Unlock()
	for _, completion := range []chan struct{}{done, streamDone} {
		if completion == nil {
			continue
		}
		select {
		case <-completion:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return a.closeResources(ctx)
}
func (a *DeepAgent) Interrupt(opts ...compose.GraphInterruptOption) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active || a.interrupt == nil {
		return false
	}
	a.interrupt(opts...)
	// Eino's interrupt function closes a channel and is one-shot.
	a.interrupt = nil
	return true
}
func (a *DeepAgent) Name() string { return a.cfg.Name }
func (a *DeepAgent) Depth() int   { return a.cfg.Depth }
func (a *DeepAgent) event(ctx context.Context, state *types.RunState, kind, callID string, data any) error {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	state.EventSeq++
	event := types.RuntimeEvent{Sequence: state.EventSeq, Kind: kind, CallID: callID, Data: data}
	for _, mw := range a.middlewares {
		if observer, ok := mw.(middleware.EventObserver); ok {
			if err := observer.Observe(ctx, event); err != nil {
				return err
			}
		}
	}
	if a.emit == nil {
		return nil
	}
	return a.emit(ctx, event)
}

func (a *DeepAgent) GraphState() *types.GraphState { return a.graphState }

// GetGraphRunnable preserves the public Eino message-shaped interface. The
// adapter only converts inputs/options; all four methods use this agent's graph.
func (a *DeepAgent) GetGraphRunnable() compose.Runnable[[]*schema.Message, *schema.Message] {
	return messageRunnable{agent: a}
}

type messageRunnable struct{ agent *DeepAgent }

func (r messageRunnable) Invoke(ctx context.Context, input []*schema.Message, opts ...compose.Option) (*schema.Message, error) {
	return r.agent.Run(ctx, input, func(o *RunOptions) { o.composeOpts = append(o.composeOpts, opts...) })
}
func (r messageRunnable) Stream(ctx context.Context, input []*schema.Message, opts ...compose.Option) (*schema.StreamReader[*schema.Message], error) {
	return r.agent.Stream(ctx, input, func(o *RunOptions) { o.composeOpts = append(o.composeOpts, opts...) })
}
func (r messageRunnable) Collect(ctx context.Context, input *schema.StreamReader[[]*schema.Message], opts ...compose.Option) (*schema.Message, error) {
	messages, err := collectInputs(ctx, input)
	if err != nil {
		return nil, err
	}
	return r.Invoke(ctx, messages, opts...)
}
func (r messageRunnable) Transform(ctx context.Context, input *schema.StreamReader[[]*schema.Message], opts ...compose.Option) (*schema.StreamReader[*schema.Message], error) {
	messages, err := collectInputs(ctx, input)
	if err != nil {
		return nil, err
	}
	return r.Stream(ctx, messages, opts...)
}
func collectInputs(ctx context.Context, input *schema.StreamReader[[]*schema.Message]) ([]*schema.Message, error) {
	if input == nil {
		return nil, errors.New("input stream is nil")
	}
	defer input.Close()
	var messages []*schema.Message
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, err := input.Recv()
		if err == io.EOF {
			return messages, nil
		}
		if err != nil {
			return nil, err
		}
		messages = append(messages, part...)
	}
}
