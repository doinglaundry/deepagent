package repl

import (
	"bufio"
	"context"
	"fmt"
	"os"
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
	Config          config.Config
	Workspace       workspace.Manifest
	Session         session.Session
	Store           *session.Store
	CheckpointStore *checkpoint.Store
	Parser          *router.Parser
	Renderer        render.Renderer
	Orchestrator    *orchestrator.Service
	Planner         *planner.Planner
	Tracker         *tracker.Tracker
	MemoryStore     *memorystore.Store
	MemoryPolicy    *memorypolicy.Policy
	Retriever       *memoryretrieval.Retriever
}

func New(cfg config.Config, manifest workspace.Manifest, renderer render.Renderer, service *orchestrator.Service) *REPL {
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
	return &REPL{
		Config:          cfg,
		Workspace:       manifest,
		Session:         currentSession,
		Store:           store,
		CheckpointStore: checkpointStore,
		Parser:          router.New(),
		Renderer:        renderer,
		Orchestrator:    service,
		Planner:         plan,
		Tracker:         tracked,
		MemoryStore:     memoryStore,
		MemoryPolicy:    memoryPolicy,
		Retriever:       memoryRetriever,
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
			}
		}

		if !accepted.Run.Result.Success {
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
		return true, r.Renderer.Render(render.Message{Kind: "command", Content: "支持的命令: /help, /status, /tasks, /memory, /exit, /read <file>, /ls [dir], /shell <command>"})
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
	default:
		return false, nil
	}
}
