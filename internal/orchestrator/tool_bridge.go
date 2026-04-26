package orchestrator

import (
	"context"
	"fmt"
	"time"

	"eino-cli/internal/cli/router"
	"eino-cli/internal/runtime/eino"
	"eino-cli/internal/session"
	"eino-cli/internal/tools"
)

func (s *Service) tryToolInvocation(ctx context.Context, sess session.Session, route router.Route, now time.Time) (bool, session.ToolInvocation, eino.Result, error) {
	if route.CommandName == "" {
		return false, session.ToolInvocation{}, eino.Result{}, nil
	}

	tool, err := s.registry.Get(route.CommandName)
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

	if s.policy.RequiresApproval(tool) {
		invocation.ApprovalStatus = session.ApprovalStatusAwaitingApproval
		s.queuePendingApproval(invocation.ID, pendingToolExecution{
			SessionID:   sess.ID,
			Route:       route,
			Invocation:  invocation,
			Tool:        tool,
			WorkingDir:  sess.WorkspaceRoot,
			RequestedAt: now,
		})
		return true, invocation, eino.Result{Success: false, Code: eino.ErrorCodeTool, Message: "tool approval required", NeedsUser: true}, nil
	}

	invocation.ExecutionStatus = session.ExecutionStatusExecuting
	result, err := s.executeTool(ctx, tool, route.Args, sess.WorkspaceRoot)
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

func (s *Service) ContinueToolInvocation(ctx context.Context, sessionID, invocationID string, approved bool) (session.ToolInvocation, eino.Result, error) {
	pending, ok := s.resolvePendingApproval(sessionID, invocationID)
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

	result, err := s.executeTool(ctx, pending.Tool, invocation.Arguments, pending.WorkingDir)
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

func (s *Service) executeTool(_ context.Context, tool tools.Tool, args []string, cwd string) (tools.Result, error) {
	return s.executor.Execute(tool, args, cwd)
}

func (s *Service) queuePendingApproval(invocationID string, pending pendingToolExecution) {
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	s.pendingApproval[invocationID] = pending
}

func (s *Service) resolvePendingApproval(sessionID, invocationID string) (pendingToolExecution, bool) {
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	pending, ok := s.pendingApproval[invocationID]
	if !ok {
		return pendingToolExecution{}, false
	}
	if pending.SessionID != sessionID {
		return pendingToolExecution{}, false
	}
	delete(s.pendingApproval, invocationID)
	return pending, true
}
