package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

type ApprovalCallback func(ctx context.Context, toolName, args string) bool

var HITLApprover ApprovalCallback = defaultStdinApproval

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
