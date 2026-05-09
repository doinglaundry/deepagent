package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// defaultHITLApproval is the stdin y/N approver attached when rt.HITLTools
// is non-empty. EOF / non-y answers deny (safer default).
var defaultHITLApproval = func() func(ctx context.Context, toolName, args string) bool {
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
