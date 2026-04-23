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
	"eino-cli/internal/config"
	"eino-cli/internal/orchestrator"
	"eino-cli/internal/session"
	"eino-cli/internal/workspace"
)

type Runner interface {
	Run(ctx context.Context) error
}

type REPL struct {
	Config       config.Config
	Workspace    workspace.Manifest
	Session      session.Session
	Store        *session.Store
	Parser       *router.Parser
	Renderer     render.Renderer
	Orchestrator *orchestrator.Service
}

func New(cfg config.Config, manifest workspace.Manifest, renderer render.Renderer, service *orchestrator.Service) *REPL {
	now := time.Now()
	store := session.NewStore(cfg.SessionsDir)
	currentSession := session.New(fmt.Sprintf("session-%d", now.UnixNano()), manifest.RootPath, now)
	if latest, ok, err := store.LoadLatest(); err == nil && ok && latest.WorkspaceRoot == manifest.RootPath {
		currentSession = latest.Touch(now)
	}
	return &REPL{
		Config:       cfg,
		Workspace:    manifest,
		Session:      currentSession,
		Store:        store,
		Parser:       router.New(),
		Renderer:     renderer,
		Orchestrator: service,
	}
}

func (r *REPL) Run(ctx context.Context) error {
	if err := r.Store.Save(r.Session); err != nil {
		return err
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
		return true, r.Renderer.Render(render.Message{Kind: "command", Content: "支持的命令: /help, /status, /exit, /read <file>, /ls [dir], /shell <command>"})
	case "status":
		return true, r.Renderer.RenderStatus(clistatus.Snapshot{Workspace: r.Workspace.RootPath, Mode: "single-agent", TaskState: "idle"})
	default:
		return false, nil
	}
}
