package repl

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"eino-cli/internal/cli/render"
	"eino-cli/internal/cli/router"
	clistatus "eino-cli/internal/cli/status"
	"eino-cli/internal/cli/taskview"
	"eino-cli/internal/config"
	memorypolicy "eino-cli/internal/memory/policy"
	memoryretrieval "eino-cli/internal/memory/retrieval"
	memorystore "eino-cli/internal/memory/store"
	"eino-cli/internal/orchestrator"
	"eino-cli/internal/runtime/eino"
	"eino-cli/internal/session"
	"eino-cli/internal/session/checkpoint"
	"eino-cli/internal/task/planner"
	"eino-cli/internal/task/tracker"
	"eino-cli/internal/workspace"
)

type Runner interface {
	Run(ctx context.Context) error
}

type REPL struct {
	Config              config.Config
	Workspace           workspace.Manifest
	Session             session.Session
	Store               *session.Store
	CheckpointStore     *checkpoint.Store
	Parser              *router.Parser
	Renderer            render.Renderer
	Orchestrator        *orchestrator.Service
	Planner             *planner.Planner
	Tracker             *tracker.Tracker
	MemoryStore         *memorystore.Store
	MemoryPolicy        *memorypolicy.Policy
	Retriever           *memoryretrieval.Retriever
	KnownCommands       map[string]struct{}
	KnownCommandsPretty []string
}

func New(cfg config.Config, manifest workspace.Manifest, renderer render.Renderer, service *orchestrator.Service, knownCommands []string) *REPL {
	now := time.Now()
	store := session.NewStore(cfg.SessionsDir)
	checkpointStore := checkpoint.NewStore(cfg.CheckpointDir)
	currentSession := session.New(fmt.Sprintf("session-%d", now.UnixNano()), manifest.RootPath, now)
	if latest, ok, err := store.LoadLatest(); err == nil && ok && latest.WorkspaceRoot == manifest.RootPath {
		currentSession = latest.Touch(now)
	}
	plan := planner.New()
	tracked := tracker.New(nil)
	memoryStore := memorystore.NewStore(cfg.MemoryDir)
	memoryPolicy := memorypolicy.New()
	memoryRetriever := memoryretrieval.New(memoryStore)

	known := map[string]struct{}{
		"/help":   {},
		"/status": {},
		"/tasks":  {},
		"/memory": {},
		"/exit":   {},
		"/read":   {},
		"/ls":     {},
		"/shell":  {},
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
		Workspace:           manifest,
		Session:             currentSession,
		Store:               store,
		CheckpointStore:     checkpointStore,
		Parser:              router.New(),
		Renderer:            renderer,
		Orchestrator:        service,
		Planner:             plan,
		Tracker:             tracked,
		MemoryStore:         memoryStore,
		MemoryPolicy:        memoryPolicy,
		Retriever:           memoryRetriever,
		KnownCommands:       known,
		KnownCommandsPretty: knownPretty,
	}
}

func (r *REPL) Run(ctx context.Context) error {
	if err := r.Store.Save(r.Session); err != nil {
		return err
	}
	if snapshot, ok, err := checkpoint.RecoverLatest(r.Config.CheckpointDir); err == nil && ok && snapshot.SessionID == r.Session.ID {
		message, err := resumeMessage(r.Session, snapshot, r.Retriever)
		if err != nil {
			return err
		}
		if err := r.Renderer.Render(message); err != nil {
			return err
		}
	}

	if err := r.Renderer.RenderStatus(clistatus.Snapshot{Workspace: r.Workspace.RootPath, Mode: "single-agent", TaskState: "idle"}); err != nil {
		return err
	}

	scanner := bufio.NewScanner(os.Stdin)
	for {
		if _, err := fmt.Fprint(os.Stdout, "> "); err != nil {
			return err
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			return nil
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "/exit" {
			return nil
		}

		r.Session = r.Session.Touch(time.Now())
		if err := r.Store.Save(r.Session); err != nil {
			return err
		}

		route := r.Parser.Parse(input)
		if route.InputType == router.InputTypeSlashCommand && !r.isKnownSlashCommand(route.CommandName) {
			unknown := strings.TrimSpace(route.RawInput)
			if unknown == "" {
				unknown = "/"
			}
			if err := r.Renderer.RenderError(render.ErrorView{Code: eino.ErrorCodeTool, Message: fmt.Sprintf("unknown command: %s", unknown)}); err != nil {
				return err
			}
			continue
		}
		if route.InputType == router.InputTypeNaturalLanguage {
			planned := r.Planner.Plan(route.RawInput)
			r.Tracker = tracker.New(planned)
			if len(planned) > 0 {
				r.Tracker.SetStatus(planned[0].ID, "in_progress")
			}
			memory := memorystore.Memory{
				Key:       fmt.Sprintf("memory-%d", time.Now().UnixNano()),
				Content:   route.RawInput,
				Scope:     r.Workspace.RootPath,
				UpdatedAt: time.Now(),
			}
			if r.MemoryPolicy.Allow(memory) {
				if err := r.MemoryStore.Save(memory); err != nil {
					return err
				}
			}
		}
		if handled, err := r.handleBuiltin(route); handled || err != nil {
			if err != nil {
				return err
			}
			continue
		}

		accepted, err := r.Orchestrator.Submit(ctx, r.Session, route)
		if err != nil {
			if renderErr := r.Renderer.RenderError(render.ErrorView{Code: "runtime_error", Message: err.Error()}); renderErr != nil {
				return renderErr
			}
			continue
		}

		snapshot := checkpoint.Snapshot{
			SessionID:        r.Session.ID,
			WorkspaceRoot:    r.Workspace.RootPath,
			LastInput:        route.RawInput,
			AwaitingApproval: len(accepted.Run.Invocations) > 0 && accepted.Run.Invocations[0].ApprovalStatus == session.ApprovalStatusAwaitingApproval,
			UpdatedAt:        time.Now(),
		}
		if err := r.CheckpointStore.Save(snapshot); err != nil {
			return err
		}

		if len(accepted.Run.Invocations) > 0 {
			invocation := accepted.Run.Invocations[0]
			if invocation.ApprovalStatus == session.ApprovalStatusAwaitingApproval {
				if err := r.Renderer.Render(approvalMessage(invocation)); err != nil {
					return err
				}
				if _, err := fmt.Fprint(os.Stdout, "approval> "); err != nil {
					return err
				}
				if !scanner.Scan() {
					if err := scanner.Err(); err != nil {
						return err
					}
					return nil
				}
				decision := strings.ToLower(strings.TrimSpace(scanner.Text()))
				approved := decision == "y" || decision == "yes"
				resolvedInvocation, toolResult, err := r.Orchestrator.ContinueToolInvocation(ctx, r.Session.ID, invocation.ID, approved)
				if err != nil {
					if renderErr := r.Renderer.RenderError(render.ErrorView{Code: eino.ErrorCodeTool, Message: err.Error()}); renderErr != nil {
						return renderErr
					}
					continue
				}
				if resolvedInvocation.ExecutionStatus == session.ExecutionStatusRejected {
					if err := r.Renderer.Render(render.Message{Kind: "approval", Content: "已拒绝执行"}); err != nil {
						return err
					}
				}
				if !toolResult.Success {
					if err := r.Renderer.RenderError(render.ErrorView{Code: toolResult.Code, Message: toolResult.Message}); err != nil {
						return err
					}
					continue
				}
				if err := r.Renderer.Render(render.Message{Kind: "tool", Content: toolResult.Output}); err != nil {
					return err
				}
				// Feed shell result back to model for analysis.
				if toolResult.Output != "" {
					feedPrompt := fmt.Sprintf("[Shell result]\n%s", toolResult.Output)
					feedOnChunk := func(chunk string) { fmt.Fprint(os.Stdout, chunk) }
					feedRoute := router.Route{
						RawInput:  feedPrompt,
						InputType: router.InputTypeNaturalLanguage,
						Target:    router.TargetAgent,
					}
					feedAccepted, feedErr := r.Orchestrator.SubmitStream(ctx, r.Session, feedRoute, feedOnChunk)
					if feedErr == nil && feedAccepted.Run.Result.Success {
						fmt.Fprintln(os.Stdout)
					}
				}
				continue
			}
		}

		if !accepted.Run.Result.Success {
			if accepted.Run.Result.NeedsUser {
				continue
			}
			if err := r.Renderer.RenderError(render.ErrorView{Code: accepted.Run.Result.Code, Message: accepted.Run.Result.Message}); err != nil {
				return err
			}
			continue
		}

		if err := r.Renderer.Render(render.Message{Kind: string(route.InputType), Content: accepted.Run.Result.Output}); err != nil {
			return err
		}
	}
}

func (r *REPL) handleBuiltin(route router.Route) (bool, error) {
	if route.InputType != router.InputTypeSlashCommand {
		return false, nil
	}

	switch route.CommandName {
	case "help":
		return true, r.Renderer.Render(render.Message{Kind: "command", Content: "支持的命令: " + strings.Join(r.KnownCommandsPretty, ", ")})
	case "status":
		return true, r.Renderer.RenderStatus(clistatus.Snapshot{Workspace: r.Workspace.RootPath, Mode: "single-agent", TaskState: "idle"})
	case "tasks":
		return true, r.Renderer.Render(render.Message{Kind: "tasks", Content: taskview.FromTasks(r.Tracker.Tasks()).String()})
	case "memory":
		memories, err := r.Retriever.Find("")
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
	now := time.Now()
	r.Session = session.New(fmt.Sprintf("session-%d", now.UnixNano()), r.Workspace.RootPath, now)
	if err := r.Store.Save(r.Session); err != nil {
		return true, err
	}
	r.Tracker = tracker.New(nil)
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
