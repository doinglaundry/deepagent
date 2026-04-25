package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"eino-cli/internal/cli/router"
	"eino-cli/internal/config"
	"eino-cli/internal/plugin/gateway"
	"eino-cli/internal/session"
	"eino-cli/internal/tools/execute"
	"eino-cli/internal/tools/policy"
	"eino-cli/internal/tools/registry"
)

func TestToolApprovalFlow(t *testing.T) {
	svc := NewService(nil, registry.New(gateway.New(config.PluginGatewayConfig{Enabled: false})), execute.New(), policy.New())
	sess := session.New("s1", ".", time.Now())
	route := router.Route{RawInput: "/shell printf ok", InputType: router.InputTypeSlashCommand, CommandName: "shell", Args: []string{"printf", "ok"}}

	handled, invocation, result, err := svc.tryToolInvocation(context.Background(), sess, route, time.Now())
	if !handled {
		t.Fatal("expected tool route handled")
	}
	if err != nil {
		t.Fatalf("tryToolInvocation() error = %v", err)
	}
	if !result.NeedsUser {
		t.Fatalf("expected needs user, got %+v", result)
	}
	if invocation.ApprovalStatus != session.ApprovalStatusAwaitingApproval {
		t.Fatalf("unexpected approval status: %q", invocation.ApprovalStatus)
	}

	rejectedInvocation, rejectedResult, err := svc.ContinueToolInvocation(context.Background(), sess.ID, invocation.ID, false)
	if err != nil {
		t.Fatalf("ContinueToolInvocation(reject) error = %v", err)
	}
	if rejectedInvocation.ExecutionStatus != session.ExecutionStatusRejected {
		t.Fatalf("unexpected rejected execution status: %q", rejectedInvocation.ExecutionStatus)
	}
	if rejectedResult.Success {
		t.Fatalf("expected failure result on rejection: %+v", rejectedResult)
	}
}

func TestToolApprovalExecuteAfterApprove(t *testing.T) {
	svc := NewService(nil, registry.New(gateway.New(config.PluginGatewayConfig{Enabled: false})), execute.New(), policy.New())
	sess := session.New("s2", ".", time.Now())
	route := router.Route{RawInput: "/shell printf ok", InputType: router.InputTypeSlashCommand, CommandName: "shell", Args: []string{"printf", "ok"}}

	handled, invocation, _, err := svc.tryToolInvocation(context.Background(), sess, route, time.Now())
	if !handled || err != nil {
		t.Fatalf("tryToolInvocation() handled=%v err=%v", handled, err)
	}

	approvedInvocation, approvedResult, err := svc.ContinueToolInvocation(context.Background(), sess.ID, invocation.ID, true)
	if err != nil {
		t.Fatalf("ContinueToolInvocation(approve) error = %v", err)
	}
	if approvedInvocation.ExecutionStatus != session.ExecutionStatusSucceeded {
		t.Fatalf("unexpected approved execution status: %q", approvedInvocation.ExecutionStatus)
	}
	if !approvedResult.Success {
		t.Fatalf("expected success result: %+v", approvedResult)
	}
	if strings.TrimSpace(approvedResult.Output) != "ok" {
		t.Fatalf("unexpected output: %q", approvedResult.Output)
	}
}

