package repl

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"eino-cli/internal/cli/render"
	"eino-cli/internal/cli/router"
	clistatus "eino-cli/internal/cli/status"
	"eino-cli/internal/cli/taskview"
	"eino-cli/internal/config"
	memorystore "eino-cli/internal/memory/store"
	"eino-cli/internal/runtime/eino"
	"eino-cli/internal/session"
	"eino-cli/internal/session/turn"
	"eino-cli/internal/task/planner"
	"eino-cli/internal/task/tracker"
	"eino-cli/internal/tools"
	"eino-cli/internal/tools/registry"
)

type Runner interface {
	Run(ctx context.Context) error
}

type pendingToolExecution struct {
	SessionID   string
	Route       router.Route
	Invocation  session.ToolInvocation
	Tool        tools.Tool
	WorkingDir  string
	RequestedAt time.Time
}

type REPL struct {
	Config              config.Config
	Session             session.Session
	Store               *session.Store
	TurnStore           *turnstore.Store
	Parser              *router.Parser
	Renderer            render.Renderer
	Runtime             eino.Runtime
	Registry            *registry.Registry
	Planner             *planner.Planner
	Tracker             *tracker.Tracker
	MemoryStore         *memorystore.Store
	KnownCommands       map[string]struct{}
	KnownCommandsPretty []string
	nextTurnIndex       int
	scanner             *bufio.Scanner

	approvalMu      sync.Mutex
	pendingApproval map[string]pendingToolExecution
}

func New(cfg config.Config, renderer render.Renderer, runtime eino.Runtime, registry *registry.Registry, executor *execute.Executor, knownCommands []string) *REPL {
	store := session.NewStore(cfg.SessionsDir)
	turnStore := turnstore.NewStore(cfg.SessionsDir)
	currentSession := session.New(fmt.Sprintf("session-%d", time.Now().UnixNano()), cfg.RootDir)
	if latest, ok, err := store.LoadLatest(); err == nil && ok && latest.WorkspaceRoot == cfg.RootDir {
		currentSession = latest
	}
	plan := planner.New()
	tracked := tracker.New(nil)
	memoryStore := memorystore.NewStore(cfg.MemoryDir)

	known := map[string]struct{}{
		"/help":   {},
		"/status": {},
		"/tasks":  {},
		"/memory": {},
		"/exit":   {},
		"/read":   {},
		"/ls":     {},
		"/shell":  {},
		"/fetch":  {},
		"/search": {},
	}
	for _, cmd := range knownCommands {
		trimmed := strings.TrimSpace(cmd)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "/") {
			trimmed = "/" + trimmed
		}
		known[trimmed] = struct{}{}
	}
	knownPretty := make([]string, 0, len(known))
	for cmd := range known {
		knownPretty = append(knownPretty, cmd)
	}
	sort.Strings(knownPretty)

	return &REPL{
		Config:              cfg,
		Session:             currentSession,
		Store:               store,
		TurnStore:           turnStore,
		Parser:              router.New(),
		Renderer:            renderer,
		Runtime:             runtime,
		Registry:            registry.New(),
		Planner:             plan,
		Tracker:             tracked,
		MemoryStore:         memoryStore,
		KnownCommands:       known,
		KnownCommandsPretty: knownPretty,
		pendingApproval:     map[string]pendingToolExecution{},
	}
}

func (r *REPL) Run(ctx context.Context) error {
	if err := r.startup(); err != nil {
		return err
	}
	r.scanner = bufio.NewScanner(os.Stdin)
	for {
		input, done, err := r.readInput()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if input == "" {
			continue
		}
		route, skip, err := r.prepareRoute(input)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if err := r.execute(ctx, route); err != nil {
			return err
		}
	}
}

// startup 初始化 nextTurnIndex、渲染崩溃恢复消息、渲染初始状态栏。
func (r *REPL) startup() error {
	err := r.Store.Save(r.Session)
	if err != nil {
		return err
	}
	nextIdx, err := r.TurnStore.NextIndex(r.Session.ID)
	if err != nil {
		return err
	}
	r.nextTurnIndex = nextIdx

	if incompleteTurn, ok, err := r.TurnStore.RecoverLatestIncomplete(r.Session.ID); err == nil && ok {
		message, err := resumeMessage(r.Session, incompleteTurn, r.MemoryStore)
		if err != nil {
			return err
		}
		err = r.Renderer.Render(message)
		if err != nil {
			return err
		}
	}
	return nil
}

// readInput 打印提示符、读取一行输入、更新 session 活跃时间。
// done=true 表示应退出循环（EOF 或 /exit）；input="" 表示空行，调用方应 continue。
func (r *REPL) readInput() (input string, done bool, err error) {
	_, err = fmt.Fprint(os.Stdout, "> ")
	if err != nil {
		return "", false, err
	}
	if !r.scanner.Scan() {
		return "", true, r.scanner.Err()
	}
	input = strings.TrimSpace(r.scanner.Text())
	if input == "/exit" {
		return "", true, nil
	}
	if input == "" {
		return "", false, nil
	}
	// 更新 session 的最后活跃时间并持久化
	err = r.Store.Save(r.Session)
	if err != nil {
		return "", false, err
	}
	return input, false, nil
}

// prepareRoute 解析输入、处理未知斜杠命令、规划任务并写入 memory、分发内置命令。
// skip=true 表示本轮已处理完毕，调用方应 continue，不需再调用 execute。
func (r *REPL) prepareRoute(input string) (route router.Route, skip bool, err error) {
	// 解析输入，判断是自然语言还是斜杠命令
	route = r.Parser.Parse(input)
	// 未知斜杠命令：渲染错误后跳过
	if route.InputType == router.InputTypeSlashCommand && !r.isKnownSlashCommand(route.CommandName) {
		unknown := strings.TrimSpace(route.RawInput)
		if unknown == "" {
			unknown = "/"
		}
		err = r.Renderer.RenderError(render.ErrorView{Code: eino.ErrorCodeTool, Message: fmt.Sprintf("unknown command: %s", unknown)})
		return route, true, err
	}
	// 自然语言：规划任务并记录到 memory
	if route.InputType == router.InputTypeNaturalLanguage {
		planned := r.Planner.Plan(route.RawInput)
		r.Tracker = tracker.New(planned)
		if len(planned) > 0 {
			r.Tracker.SetStatus(planned[0].ID, "in_progress")
		}
		memory := memorystore.Memory{
			Key:       fmt.Sprintf("memory-%d", time.Now().UnixNano()),
			Content:   route.RawInput,
			SessionID: r.Session.ID,
			TurnIndex: r.nextTurnIndex,
		}
		// 仅在 policy 允许时持久化 memory
			trimmed := strings.TrimSpace(memory.Content)
			minContentLen := 8
			if len(trimmed) >= minContentLen && utf8.RuneCountInString(trimmed) >= minContentLen {
				err = r.MemoryStore.Save(memory)
				if err != nil {
					return route, false, err
				}
			}
	}
	// 内置命令（/help、/tasks 等）由 handleBuiltin 处理
	handled, err := r.handleBuiltin(route)
	if err != nil {
		return route, true, err
	}
	return route, handled, nil
}

// execute 创建 Turn、执行请求、处理工具审批、渲染最终结果。
// 不完整的 Turn（CompletedAt == nil）留在磁盘作为崩溃恢复的锚点。
func (r *REPL) execute(ctx context.Context, route router.Route) error {
	now := time.Now()
	turn := session.NewTurn(r.nextTurnIndex, r.Session.ID, route.RawInput, now)
	r.nextTurnIndex++
	if err := r.TurnStore.Save(turn); err != nil {
		return err
	}

	streamed := false
	onChunk := func(chunk string) {
		fmt.Fprint(os.Stdout, chunk)
		streamed = true
	}

	var result eino.Result
	var invocation session.ToolInvocation

	// 处理斜杠命令
	if route.InputType == router.InputTypeSlashCommand {
		handled, inv, toolResult, err := r.tryToolInvocation(ctx, r.Session, route, now)
		if handled {
			invocation = inv
			if toolResult.NeedsUser {
				// 需要用户输入的情况，暂时不处理
				return r.RenderError(toolResult.Code, toolResult.Message)
			}
			if err != nil {
				if renderErr := r.RenderError(toolResult.Code, toolResult.Message); renderErr != nil {
					return renderErr
				}
				return nil
			}
			// 将工具结果反馈给模型
			if toolResult.Success && toolResult.Output != "" {
				syntheticPrompt := fmt.Sprintf("[Tool result: %s]\n%s", route.CommandName, toolResult.Output)
				var agentErr error
				result, agentErr = r.Runtime.ExecuteStream(ctx, syntheticPrompt, onChunk)
				if agentErr != nil {
					if renderErr := r.RenderError("runtime_error", agentErr.Error()); renderErr != nil {
						return renderErr
					}
					return nil
				}
			} else {
				result = toolResult
			}
		}
	}

	// 在 LLM runtime 上执行
	if result.Output == "" && result.Code == "" {
		var err error
		result, err = r.Runtime.ExecuteStream(ctx, route.RawInput, onChunk)
		if err != nil {
			if renderErr := r.Renderer.RenderError(render.ErrorView{Code: "runtime_error", Message: err.Error()}); renderErr != nil {
				return renderErr
			}
			return nil
		}
	}

	// 保存 turn 结果
	turnResult := session.TurnResult{Success: result.Success, Output: result.Output}
	turn = turn.Complete(turnResult, time.Now())
	if err := r.TurnStore.Save(turn); err != nil {
		return err
	}

	// 渲染输出
	if streamed {
		fmt.Fprintln(os.Stdout)
	} else {
		err := r.Renderer.Render(render.Message{Kind: string(route.InputType), Content: result.Output})
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *REPL) RenderError(code, message string) error {
	return r.Renderer.RenderError(render.ErrorView{Code: eino.ErrorCode(code), Message: message})
}

// handleApproval 处理工具调用的审批子流程：展示命令、等待用户确认、执行并渲染结果。
func (r *REPL) handleApproval(ctx context.Context, invocation session.ToolInvocation) error {
	// 渲染审批提示，告知用户待执行的命令名
	err := r.Renderer.Render(render.Message{Kind: "approval", Content: fmt.Sprintf("命令 %q 需要确认，请输入 y/yes 批准，其他输入视为拒绝", invocation.ToolName)})
	if err != nil {
		return err
	}
	// 打印审批专用提示符并读取用户决策
	if _, err = fmt.Fprint(os.Stdout, "approval> "); err != nil {
		return err
	}
	if !r.scanner.Scan() {
		return r.scanner.Err()
	}
	decision := strings.ToLower(strings.TrimSpace(r.scanner.Text()))
	approved := decision == "y" || decision == "yes"

	// 将审批结果传回 orchestrator，继续执行或拒绝工具调用
	resolvedInvocation, toolResult, err := r.ContinueToolInvocation(ctx, r.Session.ID, invocation.ID, approved)
	if err != nil {
		if renderErr := r.Renderer.RenderError(render.ErrorView{Code: eino.ErrorCodeTool, Message: err.Error()}); renderErr != nil {
			return renderErr
		}
		return nil
	}

	// 用户拒绝执行，渲染拒绝提示
	if resolvedInvocation.ExecutionStatus == session.ExecutionStatusRejected {
		err = r.Renderer.Render(render.Message{Kind: "approval", Content: "已拒绝执行"})
		if err != nil {
			return err
		}
	}

	// 工具执行失败，渲染错误信息
	if !toolResult.Success {
		err = r.Renderer.RenderError(render.ErrorView{Code: toolResult.Code, Message: toolResult.Message})
		if err != nil {
			return err
		}
		return nil
	}

	// 渲染工具执行的输出结果
	err = r.Renderer.Render(render.Message{Kind: "tool", Content: toolResult.Output})
	if err != nil {
		return err
	}

	// 将 shell 输出二次提交给模型进行分析
	if toolResult.Output != "" {
		feedPrompt := fmt.Sprintf("[Shell result]\n%s", toolResult.Output)
		feedOnChunk := func(chunk string) { fmt.Fprint(os.Stdout, chunk) }
		feedResult, feedErr := r.Runtime.ExecuteStream(ctx, feedPrompt, feedOnChunk)
		if feedErr == nil && feedResult.Success {
			fmt.Fprintln(os.Stdout)
		}
	}
	return nil
}

func (r *REPL) handleBuiltin(route router.Route) (bool, error) {
	if route.InputType != router.InputTypeSlashCommand {
		return false, nil
	}

	switch route.CommandName {
	case "help":
		return true, r.Renderer.Render(render.Message{Kind: "command", Content: "支持的命令: " + strings.Join(r.KnownCommandsPretty, ", ")})
	case "status":
		snap := clistatus.Snapshot{Workspace: r.Config.RootDir, Mode: "single-agent", TaskState: "idle"}
		return true, r.Renderer.Render(render.Message{Kind: "status", Content: snap.String()})
	case "tasks":
		return true, r.Renderer.Render(render.Message{Kind: "tasks", Content: taskview.FromTasks(r.Tracker.Tasks()).String()})
	case "memory":
		memories, err := r.MemoryStore.Find("")
		if err != nil {
			return true, err
		}
		if len(memories) == 0 {
			return true, r.Renderer.Render(render.Message{Kind: "memory", Content: "memory: none"})
		}
		lines := make([]string, 0, len(memories)+1)
		lines = append(lines, "memory:")
		for _, memory := range memories {
			lines = append(lines, fmt.Sprintf("- %s", memory.Content))
		}
		return true, r.Renderer.Render(render.Message{Kind: "memory", Content: strings.Join(lines, "\n")})
	case "bootstrap":
		return r.startNewSession("bootstrap completed: new session initialized")
	case "new":
		return r.startNewSession("started a new session")
	case "models":
		cfg := r.Config
		modelNames := make([]string, 0, len(cfg.Models))
		for key := range cfg.Models {
			modelNames = append(modelNames, key)
		}
		sort.Strings(modelNames)
		lines := make([]string, 0, len(modelNames)+1)
		lines = append(lines, fmt.Sprintf("default model: %s", cfg.DefaultModel))
		for _, name := range modelNames {
			mc := cfg.Models[name]
			lines = append(lines, fmt.Sprintf("- %s (%s/%s)", name, mc.Provider, mc.Model))
		}
		return true, r.Renderer.Render(render.Message{Kind: "command", Content: strings.Join(lines, "\n")})
	default:
		return false, nil
	}
}

func (r *REPL) startNewSession(msg string) (bool, error) {
	r.Session = session.New(fmt.Sprintf("session-%d", time.Now().UnixNano()), r.Config.RootDir)
	err := r.Store.Save(r.Session)
	if err != nil {
		return true, err
	}
	r.nextTurnIndex = 0
	r.Tracker = tracker.New(nil)
	r.Runtime.ClearHistory()
	return true, r.Renderer.Render(render.Message{Kind: "command", Content: msg})
}

func (r *REPL) isKnownSlashCommand(commandName string) bool {
	name := strings.TrimSpace(commandName)
	if name == "" {
		return false
	}
	_, ok := r.KnownCommands["/"+name]
	return ok
}

func (r *REPL) tryToolInvocation(ctx context.Context, sess session.Session, route router.Route, now time.Time) (bool, session.ToolInvocation, eino.Result, error) {
	if route.CommandName == "" {
		return false, session.ToolInvocation{}, eino.Result{}, nil
	}

	tool, err := r.Registry.Get(route.CommandName)
	if err != nil {
		return false, session.ToolInvocation{}, eino.Result{}, nil
	}

	invocation := session.ToolInvocation{
		ID:              fmt.Sprintf("tool-%d", now.UnixNano()),
		ToolName:        tool.Name,
		Arguments:       route.Args,
		ApprovalStatus:  session.ApprovalStatusNotRequired,
		ExecutionStatus: session.ExecutionStatusRequested,
		CreatedAt:       now,
	}

	invocation.ExecutionStatus = session.ExecutionStatusExecuting
	result, err := tool.Execute(route.Args, sess.WorkspaceRoot)
	if err != nil {
		invocation.ExecutionStatus = session.ExecutionStatusFailed
		invocation.ErrorMessage = err.Error()
		invocation.Output = result.Output
		return true, invocation, eino.FailureResult(eino.ErrorCodeTool, err.Error()), err
	}

	invocation.ExecutionStatus = session.ExecutionStatusSucceeded
	invocation.Output = result.Output
	return true, invocation, eino.SuccessResult(result.Output), nil
}

func (r *REPL) ContinueToolInvocation(ctx context.Context, sessionID, invocationID string, approved bool) (session.ToolInvocation, eino.Result, error) {
	pending, ok := r.resolvePendingApproval(sessionID, invocationID)
	if !ok {
		return session.ToolInvocation{}, eino.Result{}, fmt.Errorf("pending tool invocation %q not found", invocationID)
	}

	invocation := pending.Invocation
	if !approved {
		invocation.ApprovalStatus = session.ApprovalStatusRejected
		invocation.ExecutionStatus = session.ExecutionStatusRejected
		invocation.ErrorMessage = "tool execution rejected by user"
		return invocation, eino.FailureResult(eino.ErrorCodeTool, invocation.ErrorMessage), nil
	}

	invocation.ApprovalStatus = session.ApprovalStatusApproved
	invocation.ExecutionStatus = session.ExecutionStatusExecuting

	result, err := pending.Tool.Execute(invocation.Arguments, pending.WorkingDir)
	if err != nil {
		invocation.ExecutionStatus = session.ExecutionStatusFailed
		invocation.ErrorMessage = err.Error()
		invocation.Output = result.Output
		return invocation, eino.FailureResult(eino.ErrorCodeTool, err.Error()), err
	}

	invocation.ExecutionStatus = session.ExecutionStatusSucceeded
	invocation.Output = result.Output
	return invocation, eino.SuccessResult(result.Output), nil
}

func (r *REPL) queuePendingApproval(invocationID string, pending pendingToolExecution) {
	r.approvalMu.Lock()
	defer r.approvalMu.Unlock()
	r.pendingApproval[invocationID] = pending
}

func (r *REPL) resolvePendingApproval(sessionID, invocationID string) (pendingToolExecution, bool) {
	r.approvalMu.Lock()
	defer r.approvalMu.Unlock()
	pending, ok := r.pendingApproval[invocationID]
	if !ok {
		return pendingToolExecution{}, false
	}
	if pending.SessionID != sessionID {
		return pendingToolExecution{}, false
	}
	delete(r.pendingApproval, invocationID)
	return pending, true
}
