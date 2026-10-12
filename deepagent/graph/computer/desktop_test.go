package computer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	agentmodel "eino-cli/deepagent/model"
)

func TestDesktop_TimeoutKillsHelperAndNextRequestRestarts(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "helper")
	err := os.WriteFile(helper, []byte("#!/bin/sh\nwhile IFS= read -r line; do\ncase \"$line\" in *hang*) sleep 2;; *) printf '%s\\n' '{\"observation_id\":\"fresh\",\"text\":\"ok\"}';; esac\ndone\n"), 0700)
	if err != nil {
		t.Fatal(err)
	}
	desktop := &Desktop{helperPath: helper}
	defer desktop.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = desktop.PerformAction(ctx, "run-1", "hang", agentmodel.ComputerAction{})
	if err == nil {
		t.Fatal("hung helper was not canceled")
	}
	observation, err := desktop.PerformAction(context.Background(), "run-2", "observe", agentmodel.ComputerAction{})
	if err != nil || observation.ID != "fresh" {
		t.Fatalf("restart: %+v %v", observation, err)
	}
	_, err = desktop.PerformAction(context.Background(), "run-2", "release", agentmodel.ComputerAction{})
	if err != nil {
		t.Fatal(err)
	}
	err = desktop.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = desktop.PerformAction(context.Background(), "run-3", "observe", agentmodel.ComputerAction{})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed desktop: %v", err)
	}
}

func TestDesktop_UnknownActionOutcomeIsNotAnOrdinaryToolError(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "helper")
	err := os.WriteFile(helper, []byte("#!/bin/sh\nread line\nprintf '%s\\n' '{\"error\":\"capture failed after typing\",\"outcome_unknown\":true}'\n"), 0700)
	if err != nil {
		t.Fatal(err)
	}
	desktop := &Desktop{helperPath: helper}
	defer desktop.Close(context.Background())
	_, err = desktop.PerformAction(context.Background(), "run", "type_text", agentmodel.ComputerAction{Text: "once"})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("unknown action can be retried: %v", err)
	}
}

func TestDesktop_InvalidActionReplyHasUnknownOutcome(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "helper")
	err := os.WriteFile(helper, []byte("#!/bin/sh\nread line\nprintf 'invalid-json\\n'\n"), 0700)
	if err != nil {
		t.Fatal(err)
	}
	desktop := &Desktop{helperPath: helper}
	defer desktop.Close(context.Background())
	_, err = desktop.PerformAction(context.Background(), "run", "click", agentmodel.ComputerAction{})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("malformed action reply can be retried: %v", err)
	}
}

// 需要真实 TextEdit 和系统权限；只编辑测试临时目录中的文档。
func TestDesktopIntegrationVisibleTextArea(t *testing.T) {
	helperPath := os.Getenv("DEEPAGENT_DESKTOP_TEST_HELPER")
	if runtime.GOOS != "darwin" || helperPath == "" {
		t.Skip("set DEEPAGENT_DESKTOP_TEST_HELPER to test the native Mac helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	desktop := &Desktop{helperPath: helperPath}
	defer desktop.Close(context.Background())
	_, err := desktop.PerformAction(ctx, t.Name(), "check", agentmodel.ComputerAction{})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	marker := "DEEPAGENT_VISIBLE_TEXT_TEST:" + filepath.Base(filepath.Dir(directory))
	documentPath := filepath.Join(directory, "DeepAgent-visible-text-test.txt")
	err = os.WriteFile(documentPath, []byte(marker+"\n"+strings.Repeat("Synthetic scrolling fixture.\n", 250)), 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = exec.CommandContext(ctx, "/usr/bin/open", "-a", "TextEdit", documentPath).Run()
	if err != nil {
		t.Fatal(err)
	}
	var observation *agentmodel.ComputerObservation
	for ctx.Err() == nil {
		observation, err = desktop.PerformAction(ctx, t.Name(), "observe", agentmodel.ComputerAction{App: "com.apple.TextEdit"})
		if err == nil && strings.Contains(observation.Text, marker) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || observation == nil || !strings.Contains(observation.Text, marker) {
		t.Fatalf("test document is not in front; no input sent: %v", err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		var textArea *agentmodel.ComputerElement
		for index := range observation.Elements {
			element := &observation.Elements[index]
			if element.Role == "AXTextArea" && strings.Contains(element.Name, marker) {
				textArea = element
				break
			}
		}
		if textArea == nil {
			t.Fatal("visible text area omitted because its document extends beyond the window")
		}
		if textArea.Width <= 0 || textArea.Height <= 0 || textArea.X < 0 || textArea.Y < 0 || textArea.X+textArea.Width > float64(observation.Width) || textArea.Y+textArea.Height > float64(observation.Height) {
			t.Fatalf("text area bounds exceed the visible window: %+v", textArea)
		}
		_, err = desktop.PerformAction(ctx, t.Name(), "click", agentmodel.ComputerAction{App: observation.App, ObservationID: observation.ID, ElementID: textArea.ID})
		if err == nil {
			return
		}
		// TextEdit 打开窗口时可能仍在布局；只重试明确未执行的过期操作。
		if errors.Is(err, ErrOutcomeUnknown) || !strings.Contains(err.Error(), "stale_observation") {
			t.Fatalf("click visible text area: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		observation, err = desktop.PerformAction(ctx, t.Name(), "observe", agentmodel.ComputerAction{App: "com.apple.TextEdit"})
		if err != nil || !strings.Contains(observation.Text, marker) {
			t.Fatalf("test document changed; no more input sent: %v", err)
		}
	}
	t.Fatalf("click never received a stable observation: %v", err)
}
