package main

import (
	"eino-cli/deepagent/host/cli"
	"os"
	"path/filepath"
	"testing"
)

func TestRootFlagPrecedesEnvironment(t *testing.T) {
	t.Setenv("SGADK_ROOT", "from-env")
	opts, err := cli.Parse([]string{"--root", "from-flag"})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs("from-flag")
	if opts.Root != want {
		t.Fatalf("got %q want %q", opts.Root, want)
	}
}
func TestRootEnvironmentAndWorkingDirectory(t *testing.T) {
	t.Setenv("SGADK_ROOT", "from-env")
	opts, err := cli.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs("from-env")
	if opts.Root != want {
		t.Fatal(opts.Root)
	}
	t.Setenv("SGADK_ROOT", "")
	opts, err = cli.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ = os.Getwd()
	if opts.Root != want {
		t.Fatal(opts.Root)
	}
}
