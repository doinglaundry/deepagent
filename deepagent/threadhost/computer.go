//go:build !windows

package threadhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"eino-cli/deepagent/graph/computer"
	"eino-cli/deepagent/graph/types"
)

func getHTTPOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("valid HTTP(S) URL without credentials is required")
	}
	host := strings.ToLower(parsed.Host)
	if parsed.Scheme == "https" && parsed.Port() == "443" || parsed.Scheme == "http" && parsed.Port() == "80" {
		host = strings.TrimSuffix(host, ":"+parsed.Port())
	}
	return parsed.Scheme + "://" + host, nil
}
func ValidateComputerTargets(origins, apps []string) error {
	if len(origins) == 0 || len(apps) == 0 {
		return errors.New("computer_enabled requires browser_origins and computer_apps")
	}
	for _, origin := range origins {
		_, err := getHTTPOrigin(origin)
		if err != nil {
			return err
		}
		parsed, _ := url.Parse(origin)
		if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(origin, "*") {
			return fmt.Errorf("browser origin %q must contain only scheme and host", origin)
		}
	}
	for _, app := range apps {
		if strings.TrimSpace(app) == "" || strings.ContainsAny(app, "*/ ") {
			return errors.New("computer_apps must contain exact bundle IDs")
		}
	}
	return nil
}

// This scope check runs before read-only and remembered-approval shortcuts.
func validateComputerTarget(runtime RuntimeConfig, call types.ToolCall) error {
	browser := strings.HasPrefix(call.Name, "browser_")
	desktop := strings.HasPrefix(call.Name, "computer_")
	if !browser && !desktop {
		return nil
	}
	var action computer.Action
	err := json.Unmarshal([]byte(call.Arguments), &action)
	if err != nil {
		return err
	}
	if desktop {
		for _, allowed := range runtime.ComputerApps {
			if allowed == action.App {
				return nil
			}
		}
		return fmt.Errorf("app %q is not in computer_apps", action.App)
	}
	origin, err := getHTTPOrigin(action.URL)
	if err != nil {
		return err
	}
	for _, allowed := range runtime.BrowserOrigins {
		allowedOrigin, err := getHTTPOrigin(allowed)
		if err == nil && origin == allowedOrigin {
			return nil
		}
	}
	return fmt.Errorf("origin %q is not in browser_origins", origin)
}

// A blocked logical Thread retains Chrome for an unchanged approval target.
// The claim's Thread object can close; another Worker gets a fresh observation.
func (host *ThreadHost) getThreadBrowser(ctx context.Context, threadID int64) (*computer.Browser, error) {
	host.browserMu.Lock()
	defer host.browserMu.Unlock()
	expiration := host.browserExpirations[threadID]
	if expiration != nil {
		expiration.Stop()
		delete(host.browserExpirations, threadID)
	}
	browser := host.browsers[threadID]
	if browser != nil {
		return browser, nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	profileDir := filepath.Join(cacheDir, "DeepAgent", "browser", strconv.FormatInt(threadID, 10), strconv.Itoa(os.Getpid()))
	browser, err = computer.NewBrowser(ctx, profileDir)
	if err != nil {
		return nil, err
	}
	if host.browsers == nil {
		host.browsers = map[int64]*computer.Browser{}
	}
	host.browsers[threadID] = browser
	return browser, nil
}
func (host *ThreadHost) closeThreadBrowser(ctx context.Context, threadID int64) error {
	host.browserMu.Lock()
	browser := host.browsers[threadID]
	expiration := host.browserExpirations[threadID]
	if expiration != nil {
		expiration.Stop()
		delete(host.browserExpirations, threadID)
	}
	delete(host.browsers, threadID)
	host.browserMu.Unlock()
	if browser == nil {
		return nil
	}
	return browser.Close(ctx)
}
func (host *ThreadHost) closeBrowsers(ctx context.Context) error {
	host.browserMu.Lock()
	for _, timer := range host.browserExpirations {
		timer.Stop()
	}
	host.browserExpirations = nil
	browsers := host.browsers
	host.browsers = nil
	host.browserMu.Unlock()
	var err error
	for _, browser := range browsers {
		err = errors.Join(err, browser.Close(ctx))
	}
	return err
}

// Bound blocked retention so another Worker's takeover cannot leave Chrome forever.
func (host *ThreadHost) retainThreadBrowser(threadID int64, duration time.Duration) {
	host.browserMu.Lock()
	defer host.browserMu.Unlock()
	browser := host.browsers[threadID]
	if browser == nil {
		return
	}
	if host.browserExpirations == nil {
		host.browserExpirations = map[int64]*time.Timer{}
	}
	previous := host.browserExpirations[threadID]
	if previous != nil {
		previous.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(duration, func() {
		host.browserMu.Lock()
		if host.browserExpirations[threadID] != timer {
			host.browserMu.Unlock()
			return
		}
		delete(host.browserExpirations, threadID)
		delete(host.browsers, threadID)
		host.browserMu.Unlock()
		browser.Close(context.Background())
	})
	host.browserExpirations[threadID] = timer
}
