package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"eino-cli/internal/cli/router"
	"eino-cli/internal/runtime/eino"
	"eino-cli/internal/session"
	"eino-cli/internal/session/checkpoint"
	"eino-cli/internal/task"
	"eino-cli/internal/tools"
	"eino-cli/internal/tools/execute"
	"eino-cli/internal/tools/policy"
	"eino-cli/internal/tools/registry"
)

type AgentRun struct {
	ID          string                   `json:"id"`
	SessionID   string                   `json:"session_id"`
	CommandID   string                   `json:"command_id"`
	ModelName   string                   `json:"model_name"`
	Status      AgentRunStatus           `json:"status"`
	Result      eino.Result              `json:"result"`
	StartedAt   time.Time                `json:"started_at"`
	Invocations []session.ToolInvocation `json:"invocations,omitempty"`
}

type CommandAccepted struct {
	Command session.Command `json:"command"`
	Run     AgentRun        `json:"run"`
}

type Service struct {
	runtime     eino.Runtime
	registry    *registry.Registry
	executor    *execute.Executor
	policy      *policy.Policy
	persistence *Persistence

	approvalMu      sync.Mutex
	pendingApproval map[string]pendingToolExecution
}

type pendingToolExecution struct {
	SessionID   string
	Route       router.Route
	Invocation  session.ToolInvocation
	Tool        tools.Tool
	WorkingDir  string
	RequestedAt time.Time
}

func NewService(runtime eino.Runtime, registry *registry.Registry, executor *execute.Executor, policy *policy.Policy) *Service {
	return &Service{runtime: runtime, registry: registry, executor: executor, policy: policy, pendingApproval: map[string]pendingToolExecution{}}
}

func (s *Service) WithPersistence(persistence *Persistence) *Service {
	s.persistence = persistence
	return s
}

func (s *Service) Submit(ctx context.Context, sess session.Session, route router.Route) (CommandAccepted, error) {
	return s.SubmitStream(ctx, sess, route, nil)
}

func (s *Service) SubmitStream(ctx context.Context, sess session.Session, route router.Route, onChunk eino.StreamChunkHandler) (CommandAccepted, error) {
	now := time.Now()
	command := session.NewCommand(fmt.Sprintf("cmd-%d", now.UnixNano()), sess.ID, route.RawInput, session.CommandInputType(route.InputType), now)
	command.Status = session.CommandStatusRunning

	run := AgentRun{
		ID:        fmt.Sprintf("run-%d", now.UnixNano()),
		SessionID: sess.ID,
		CommandID: command.ID,
		ModelName: s.runtime.Name(),
		Status:    AgentRunStatusStreaming,
		StartedAt: now,
	}

	if route.InputType == router.InputTypeSlashCommand {
		if handled, invocation, result, err := s.tryToolInvocation(ctx, sess, route, now); handled {
			run.Invocations = append(run.Invocations, invocation)
			if result.NeedsUser {
				command.Status = session.CommandStatusCompleted
				command.CompletedAt = now
				command.ErrorCode = string(result.Code)
				command.ErrorMessage = result.Message
				run.Status = AgentRunStatusCompleted
				run.Result = result
				s.persist(sess, route, run)
				return CommandAccepted{Command: command, Run: run}, nil
			}
			if err != nil {
				command.Status = session.CommandStatusFailed
				command.CompletedAt = now
				command.ErrorCode = string(eino.ErrorCodeTool)
				command.ErrorMessage = err.Error()
				run.Status = AgentRunStatusFailed
				run.Result = eino.FailureResult(eino.ErrorCodeTool, err.Error())
				s.persist(sess, route, run)
				return CommandAccepted{Command: command, Run: run}, nil
			}
			// Feed tool result back to the model so it can reason about the output.
			if result.Success && result.Output != "" {
				syntheticPrompt := fmt.Sprintf("[Tool result: %s]\n%s", route.CommandName, result.Output)
				agentResult, agentErr := s.runtime.ExecuteStream(ctx, syntheticPrompt, onChunk)
				if agentErr == nil && agentResult.Success {
					run.Result = agentResult
					command.Output = agentResult.Output
				}
			}
			command.Status = session.CommandStatusCompleted
			command.CompletedAt = now
			if command.Output == "" {
				command.Output = result.Output
			}
			run.Status = AgentRunStatusCompleted
			if !run.Result.Success {
				run.Result = result
			}
			s.persist(sess, route, run)
			return CommandAccepted{Command: command, Run: run}, nil
		}
	}

	result, err := s.runtime.ExecuteStream(ctx, route.RawInput, onChunk)
	if err != nil {
		command.Status = session.CommandStatusFailed
		run.Status = AgentRunStatusFailed
		run.Result = eino.FailureResult(eino.ErrorCodeRuntime, err.Error())
		s.persist(sess, route, run)
		return CommandAccepted{Command: command, Run: run}, nil
	}

	command.Status = session.CommandStatusCompleted
	command.CompletedAt = now
	command.Output = result.Output
	command.ErrorCode = string(result.Code)
	command.ErrorMessage = result.Message

	run.Status = AgentRunStatusCompleted
	run.Result = result
	s.persist(sess, route, run)

	return CommandAccepted{Command: command, Run: run}, nil
}

func (s *Service) Runtime() eino.Runtime {
	return s.runtime
}

func (s *Service) persist(sess session.Session, route router.Route, run AgentRun) {
	if s.persistence == nil {
		return
	}
	updatedSession := sess.Touch(time.Now())
	_ = s.persistence.SaveSession(updatedSession)
	_ = s.persistence.SaveCheckpoint(checkpoint.Snapshot{
		SessionID:        sess.ID,
		WorkspaceRoot:    sess.WorkspaceRoot,
		LastInput:        route.RawInput,
		AwaitingApproval: len(run.Invocations) > 0 && run.Invocations[0].ApprovalStatus == session.ApprovalStatusAwaitingApproval,
		UpdatedAt:        time.Now(),
	})
	status := task.StatusCompleted
	if !run.Result.Success {
		status = task.StatusFailed
	}
	_ = s.persistence.SaveTask(sess, task.Task{ID: run.CommandID, Title: route.RawInput, Status: status})
}
