package main

import (
	"path/filepath"
	"testing"
)

func TestResolveRootPrefersFlag(t *testing.T) {
	got, err := resolveRoot(
		[]string{"--root", "from-flag"},
		func(string) string { return "from-env" },
		func() (string, error) { return "from-wd", nil },
	)
	if err != nil {
		t.Fatalf("resolveRoot: %v", err)
	}
	want, _ := filepath.Abs("from-flag")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveRootFallsBackToEnv(t *testing.T) {
	got, err := resolveRoot(
		nil,
		func(key string) string {
			if key == "SGADK_ROOT" {
				return "from-env"
			}
			return ""
		},
		func() (string, error) { return "from-wd", nil },
	)
	if err != nil {
		t.Fatalf("resolveRoot: %v", err)
	}
	want, _ := filepath.Abs("from-env")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveRootFallsBackToWorkingDirectory(t *testing.T) {
	got, err := resolveRoot(
		nil,
		func(string) string { return "" },
		func() (string, error) { return "from-wd", nil },
	)
	if err != nil {
		t.Fatalf("resolveRoot: %v", err)
	}
	want, _ := filepath.Abs("from-wd")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
