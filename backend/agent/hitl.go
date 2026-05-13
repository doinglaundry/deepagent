package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ApprovalCallback is what middlewares.HITL calls to ask "may this tool
// actually run". The signature must stay stdlib-only (no eino/charm
// types) so frontends — TUI, batch CLI, future RPC — can plug in their
// own asker without dragging deps back into the agent package.
type ApprovalCallback func(ctx context.Context, toolName, args string) bool

// HITLApprover is the package-level injection point read by
// GetChatModelMiddlewares when assembling the HITL middleware. The TUI
// overrides it at startup with a tea.Msg-routed approver so prompts
// render inside the chat surface instead of stomping on the alt-screen.
// Anyone who never overrides it gets defaultStdinApproval, which only
// makes sense in a non-TUI (CLI / batch / test) process.
//
// Set ONCE during process bootstrap, before the first agent runs.
// We intentionally do not lock — concurrent writes are a programming
// error, not a runtime concern.
var HITLApprover ApprovalCallback = defaultStdinApproval

// defaultStdinApproval is the fallback approver: prints a y/N prompt to
// stdout, reads one line from stdin, denies on EOF or anything that
// isn't a literal y/yes. Closed over a mutex + scanner so concurrent
// gated tool calls serialise instead of interleaving prompts.
var defaultStdinApproval ApprovalCallback = func() ApprovalCallback {
	var (
		mu      sync.Mutex
		scanner *bufio.Scanner
	)
	return func(ctx context.Context, toolName, args string) bool {
		mu.Lock()
		defer mu.Unlock()

		fmt.Fprintf(os.Stdout, "\n[approval] tool %q wants to run with args:\n  %s\nApprove? [y/N]: ", toolName, args)
		if scanner == nil {
			scanner = bufio.NewScanner(os.Stdin)
		}
		if !scanner.Scan() {
			fmt.Fprintln(os.Stdout, "(no input — denying)")
			return false
		}
		decision := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return decision == "y" || decision == "yes"
	}
}()
