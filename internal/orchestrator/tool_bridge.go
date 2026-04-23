package orchestrator

import (
	"errors"
	"fmt"
	"time"

	"eino-cli/internal/cli/router"
	"eino-cli/internal/runtime/eino"
	"eino-cli/internal/session"
)

func (s *Service) tryToolInvocation(route router.Route, cwd string, now time.Time) (bool, session.ToolInvocation, eino.Result, error) {
	if route.CommandName != "read" && route.CommandName != "ls" && route.CommandName != "shell" {
		return false, session.ToolInvocation{}, eino.Result{}, nil
	}

	tool, err := s.registry.Get(route.CommandName)
	if err != nil {
		return true, session.ToolInvocation{}, eino.Result{}, err
	}

	invocation := session.ToolInvocation{
		ID:              fmt.Sprintf("tool-%d", now.UnixNano()),
		ToolName:        tool.Name,
		Arguments:       route.Args,
		ApprovalStatus:  session.ApprovalStatusNotRequired,
		ExecutionStatus: session.ExecutionStatusRequested,
		CreatedAt:       now,
	}

	if s.policy.RequiresApproval(tool) {
		invocation.ApprovalStatus = session.ApprovalStatusAwaitingApproval
		invocation.ExecutionStatus = session.ExecutionStatusRejected
		invocation.ErrorMessage = "tool requires confirmation in current MVP"
		return true, invocation, eino.FailureResult(eino.ErrorCodeTool, invocation.ErrorMessage), errors.New(invocation.ErrorMessage)
	}

	invocation.ExecutionStatus = session.ExecutionStatusExecuting
	result, err := s.executor.Execute(tool, route.Args, cwd)
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
