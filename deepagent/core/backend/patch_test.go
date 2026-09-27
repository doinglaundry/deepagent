package backend_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/core/backend"
)

func TestWorkspacePatchHasSameLocalAndDockerBehavior(t *testing.T) {
	ctx := context.Background()
	localRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(localRoot, "a.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	local, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: localRoot, VirtualMode: true}, "local-thread")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close(ctx)
	provider := &fileSandbox{files: map[string]string{"/remote/a.txt": "old\n"}}
	docker, err := backend.NewDockerFilesystem(provider, "/remote", "docker-thread")
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close(ctx)
	for name, ws := range map[string]backend.ToolFilesystem{"local": local, "docker": docker} {
		t.Run(name, func(t *testing.T) {
			bad := "*** Begin Patch\n*** Update File: a.txt\n@@\n-missing\n+new\n*** End Patch"
			if _, err := ws.ApplyPatch(ctx, bad); err == nil {
				t.Fatal("stale patch succeeded")
			}
			patch := "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** Add File: b.txt\n+created\n*** End Patch"
			if _, err := ws.ApplyPatch(ctx, patch); err != nil {
				t.Fatal(err)
			}
			for file, want := range map[string]string{"a.txt": "new\n", "b.txt": "created\n"} {
				limit := 100
				got, err := ws.Read(ctx, file, nil, &limit)
				if err != nil || got != want {
					t.Fatalf("%s: %q, %v", file, got, err)
				}
			}
		})
	}
}

func TestDockerCommandRunsInSelectedContainer(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider := &fileSandbox{files: map[string]string{}}
	ws, err := backend.NewDockerFilesystem(provider, "/remote", "docker-thread")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close(context.Background())
	result, err := ws.Execute(context.Background(), backend.CommandRequest{Command: "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"exec", "-w", "/remote", "test-container", "/bin/sh", "pwd"} {
		if !strings.Contains(result.Output, expected) {
			t.Fatalf("missing %q in %q", expected, result.Output)
		}
	}
}

func TestDockerFilesystemRealContainer(t *testing.T) {
	image := os.Getenv("DEEPAGENT_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("set DEEPAGENT_DOCKER_TEST_IMAGE to run the real container test")
	}
	root, err := os.MkdirTemp("/private/tmp", "deepagent-docker-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	cmd := exec.Command("docker", "run", "-d", "--rm", "-v", root+":"+root, "--entrypoint", "/bin/sh", image, "-c", "sleep 120")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start container: %s: %v", out, err)
	}
	id := strings.TrimSpace(string(out))
	defer exec.Command("docker", "rm", "-f", id).Run()
	provider := &fileSandbox{containerID: id, files: map[string]string{}}
	ws, err := backend.NewDockerFilesystem(provider, root, "real-docker-thread")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close(context.Background())
	result, err := ws.Execute(context.Background(), backend.CommandRequest{Command: "printf docker-ok"})
	if err != nil || result.ExitCode != 0 || result.Output != "docker-ok" {
		t.Fatalf("command=%+v err=%v", result, err)
	}
	file := filepath.Join(root, "remove.txt")
	if err := os.WriteFile(file, []byte("remove me"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Delete(context.Background(), "remove.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("Docker delete did not remove mounted file: %v", err)
	}
	jobID, err := ws.Start(context.Background(), backend.CommandRequest{Command: "sleep 2; printf survived > " + filepath.Join(root, "leak.txt")})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := ws.Cancel(context.Background(), jobID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "leak.txt")); !os.IsNotExist(err) {
		t.Fatalf("cancelled Docker job continued running: %v", err)
	}
	timedJob, err := ws.Start(context.Background(), backend.CommandRequest{Command: "sleep 2; printf survived > " + filepath.Join(root, "timeout-leak.txt"), Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Wait(context.Background(), timedJob, "", 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "timeout-leak.txt")); !os.IsNotExist(err) {
		t.Fatalf("timed out Docker job continued running: %v", err)
	}
	if _, err := ws.Start(context.Background(), backend.CommandRequest{Command: "sleep 2; printf survived > " + filepath.Join(root, "close-leak.txt")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := ws.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "close-leak.txt")); !os.IsNotExist(err) {
		t.Fatalf("closed Docker workspace left job running: %v", err)
	}
}
