package eino

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// defaultHITLApproval is the stdin-based approval prompter used by the
// eino-cli REPL. It is invoked synchronously from inside an agent run, so
// the REPL goroutine is naturally suspended while we read y/N from stdin.
//
// Output goes to stdout via fmt.Fprintf so it interleaves with the chat
// stream the user is already watching. Input is read line-by-line from
// stdin under a package-level mutex — concurrent agent runs would
// otherwise race on the same scanner.
//
// Decision rule: case-insensitive "y" / "yes" approves; everything else
// (including EOF / read error) denies, which is the safer default.
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
