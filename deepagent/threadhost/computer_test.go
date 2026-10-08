//go:build !windows

package threadhost

import (
	"context"
	"eino-cli/deepagent/graph/computer"
	"eino-cli/deepagent/graph/types"
	"testing"
	"time"
)

func TestComputerTarget_ExactOriginAndApp(t *testing.T) {
	runtime := RuntimeConfig{BrowserOrigins: []string{"https://example.com"}, ComputerApps: []string{"com.apple.TextEdit"}}
	cases := []struct {
		name, args string
		allowed    bool
	}{
		{"browser_open", `{"url":"https://example.com/path"}`, true},
		{"browser_observe", `{"url":"https://example.com.evil.org/path"}`, false},
		{"browser_click", `{"url":"https://example.com:443/path"}`, true},
		{"browser_click", `{"url":"https://user@example.com/"}`, false},
		{"browser_click", `{"url":"file:///tmp/file"}`, false},
		{"computer_observe", `{"app":"com.apple.TextEdit"}`, true},
		{"computer_click", `{"app":"com.apple.Terminal"}`, false},
		{"browser_observe", `{}`, false},
	}
	for _, tc := range cases {
		err := validateComputerTarget(runtime, types.ToolCall{Name: tc.name, Arguments: tc.args})
		if (err == nil) != tc.allowed {
			t.Fatalf("%s %s: %v", tc.name, tc.args, err)
		}
	}
	err := ValidateComputerTargets([]string{"https://example.com/path"}, runtime.ComputerApps)
	if err == nil {
		t.Fatal("path accepted as origin")
	}
}

func TestComputer_BlockedBrowserExpiresAndReclaimCancelsExpiry(t *testing.T) {
	t.Setenv("DEEPAGENT_BROWSER_HEADLESS", "1")
	browser, err := computer.NewBrowser(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := &ThreadHost{browsers: map[int64]*computer.Browser{1: browser}}
	defer host.closeBrowsers(context.Background())
	host.retainThreadBrowser(1, 50*time.Millisecond)
	reclaimed, err := host.getThreadBrowser(context.Background(), 1)
	if err != nil || reclaimed != browser {
		t.Fatalf("reclaim: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	host.browserMu.Lock()
	preserved := host.browsers[1] == browser
	host.browserMu.Unlock()
	if !preserved {
		t.Fatal("expiry closed a reclaimed browser")
	}
	host.retainThreadBrowser(1, 10*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.browserMu.Lock()
		remaining := len(host.browsers)
		host.browserMu.Unlock()
		if remaining == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("abandoned blocked browser did not expire")
}
