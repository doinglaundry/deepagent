package computer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	_, err = desktop.PerformAction(ctx, "run-1", "hang", Action{})
	if err == nil {
		t.Fatal("hung helper was not canceled")
	}
	observation, err := desktop.PerformAction(context.Background(), "run-2", "observe", Action{})
	if err != nil || observation.ID != "fresh" {
		t.Fatalf("restart: %+v %v", observation, err)
	}
	_, err = desktop.PerformAction(context.Background(), "run-2", "release", Action{})
	if err != nil {
		t.Fatal(err)
	}
	err = desktop.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = desktop.PerformAction(context.Background(), "run-3", "observe", Action{})
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
	_, err = desktop.PerformAction(context.Background(), "run", "type_text", Action{Text: "once"})
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
	_, err = desktop.PerformAction(context.Background(), "run", "click", Action{})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("malformed action reply can be retried: %v", err)
	}
}
