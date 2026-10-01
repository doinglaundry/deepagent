package backend_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/sandbox"
)

func (s *fileSandbox) FileExists(ctx context.Context, path string) (bool, error) {
	contextErr := ctx.Err()
	if contextErr != nil {
		return false, contextErr
	}
	if s.err != nil {
		return false, s.err
	}
	_, exists := s.files[path]
	return exists, nil
}

func (s *fileSandbox) CreateFileNoReplace(ctx context.Context, path, content string) error {
	contextErr := ctx.Err()
	if contextErr != nil {
		return contextErr
	}
	if s.err != nil {
		return s.err
	}
	_, exists := s.files[path]
	if exists {
		return backend.ErrAlreadyExists
	}
	s.files[path] = content
	s.writes++
	return nil
}

type cancelingPatchExistenceSandbox struct {
	*fileSandbox
	cancel context.CancelFunc
}

func (s *cancelingPatchExistenceSandbox) FileExists(ctx context.Context, path string) (bool, error) {
	exists, err := s.fileSandbox.FileExists(ctx, path)
	s.cancel()
	return exists, err
}

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
	docker, err := backend.NewDockerFilesystem(provider, "/remote", "docker-thread", nil)
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

func TestWorkspacePatchRejectsRepeatedSourcesAndSelfMoves(t *testing.T) {
	ctx := context.Background()
	dockerDir := t.TempDir()
	marker := filepath.Join(dockerDir, "invoked")
	dockerPath := filepath.Join(dockerDir, "docker")
	script := "#!/bin/sh\nprintf invoked > \"$DOCKER_MARKER\"\nexit 99\n"
	err := os.WriteFile(dockerPath, []byte(script), 0700)
	if err != nil {
		t.Fatal(err)
	}
	previousPath := os.Getenv("PATH")
	pathValue := dockerDir
	if previousPath != "" {
		pathValue += string(os.PathListSeparator) + previousPath
	}
	t.Setenv("PATH", pathValue)
	t.Setenv("DOCKER_MARKER", marker)

	cases := []struct {
		name    string
		patch   string
		wantErr string
	}{
		{
			name: "duplicate source alias",
			patch: "*** Begin Patch\n" +
				"*** Update File: a\n@@\n-old\n+first\n" +
				"*** Update File: a/../a\n@@\n-old\n+second\n" +
				"*** End Patch",
			wantErr: "patch repeats source file",
		},
		{
			name: "self move alias",
			patch: "*** Begin Patch\n" +
				"*** Update File: a\n*** Move to: a/../a\n@@\n-old\n+new\n" +
				"*** End Patch",
			wantErr: "onto itself",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			localRoot := t.TempDir()
			err := os.WriteFile(filepath.Join(localRoot, "a"), []byte("old\n"), 0600)
			if err != nil {
				t.Fatal(err)
			}
			local, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: localRoot, VirtualMode: true}, "local-patch-validation")
			if err != nil {
				t.Fatal(err)
			}
			defer local.Close(ctx)

			provider := &fileSandbox{files: map[string]string{"/remote/a": "old\n"}}
			docker, err := backend.NewDockerFilesystem(provider, "/remote", "docker-patch-validation", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close(ctx)

			workspaces := map[string]backend.Filesystem{
				"local":  local,
				"docker": docker,
			}
			for name, workspace := range workspaces {
				t.Run(name, func(t *testing.T) {
					_, err := workspace.ApplyPatch(ctx, testCase.patch)
					if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
						t.Fatalf("patch error = %v; want %q", err, testCase.wantErr)
					}
					if provider.writes != 0 {
						t.Fatalf("rejected patch wrote %d times", provider.writes)
					}
					content, readErr := workspace.Read(ctx, "a", nil, nil)
					if readErr != nil || content != "old\n" {
						t.Fatalf("rejected patch changed file: %q, %v", content, readErr)
					}
					_, statErr := os.Stat(marker)
					if statErr == nil {
						t.Fatal("rejected patch launched docker")
					}
					if !os.IsNotExist(statErr) {
						t.Fatal(statErr)
					}
				})
			}
		})
	}
}

func TestWorkspacePatchProtectsExistingTargetsAndDeletesOversizedFiles(t *testing.T) {
	ctx := context.Background()
	oversized := strings.Repeat("x", (backend.MaxFileSizeMB<<20)+1)
	add := "*** Begin Patch\n*** Add File: target.txt\n+replacement\n*** End Patch"
	delete := "*** Begin Patch\n*** Delete File: target.txt\n*** End Patch"

	dockerDir := t.TempDir()
	logPath := filepath.Join(dockerDir, "docker.log")
	dockerPath := filepath.Join(dockerDir, "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DOCKER_LOG\"\nexit 0\n"
	err := os.WriteFile(dockerPath, []byte(script), 0700)
	if err != nil {
		t.Fatal(err)
	}
	previousPath := os.Getenv("PATH")
	pathValue := dockerDir
	if previousPath != "" {
		pathValue += string(os.PathListSeparator) + previousPath
	}
	t.Setenv("PATH", pathValue)
	t.Setenv("DOCKER_LOG", logPath)

	localRoot := t.TempDir()
	localTarget := filepath.Join(localRoot, "target.txt")
	err = os.WriteFile(localTarget, []byte(oversized), 0600)
	if err != nil {
		t.Fatal(err)
	}
	local, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: localRoot, VirtualMode: true, MaxFileSizeMB: 1}, "local-oversized-patch")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close(ctx)

	provider := &fileSandbox{files: map[string]string{"/remote/target.txt": oversized}}
	docker, err := backend.NewDockerFilesystem(provider, "/remote", "docker-oversized-patch", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close(ctx)

	for name, workspace := range map[string]backend.Filesystem{"local": local, "docker": docker} {
		t.Run(name, func(t *testing.T) {
			_, err := workspace.ApplyPatch(ctx, add)
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("add error = %v", err)
			}
		})
	}
	localContent, err := os.ReadFile(localTarget)
	if err != nil || string(localContent) != oversized {
		t.Fatalf("local oversized target changed: %d bytes, %v", len(localContent), err)
	}
	if provider.files["/remote/target.txt"] != oversized {
		t.Fatal("Docker oversized target changed")
	}

	_, err = local.ApplyPatch(ctx, delete)
	if err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(localTarget)
	if !os.IsNotExist(statErr) {
		t.Fatalf("local oversized target was not deleted: %v", err)
	}
	_, err = docker.ApplyPatch(ctx, delete)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "target.txt") {
		t.Fatalf("Docker delete command missing target: %q", log)
	}
}

func TestWorkspacePatchRejectsArbitraryAddReadErrors(t *testing.T) {
	sentinel := errors.New("provider read failed")
	provider := &fileSandbox{files: map[string]string{"/remote/target.txt": "original"}, err: sentinel}
	workspace, err := backend.NewDockerFilesystem(provider, "/remote", "docker-add-error", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close(context.Background())

	patch := "*** Begin Patch\n*** Add File: target.txt\n+replacement\n*** End Patch"
	_, err = workspace.ApplyPatch(context.Background(), patch)
	if !errors.Is(err, sentinel) {
		t.Fatalf("add error = %v", err)
	}
	if provider.writes != 0 || provider.files["/remote/target.txt"] != "original" {
		t.Fatalf("read error changed target: writes=%d files=%v", provider.writes, provider.files)
	}
}

func TestWorkspacePatchRejectsExistingMoveDestinations(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: source.txt\n*** Move to: destination.txt\n@@\n-source\n+changed\n*** End Patch"
	ctx := context.Background()
	localRoot := t.TempDir()
	err := os.WriteFile(filepath.Join(localRoot, "source.txt"), []byte("source\n"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(localRoot, "destination.txt"), []byte("destination\n"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	local, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: localRoot, VirtualMode: true}, "local-move-destination")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close(ctx)

	provider := &fileSandbox{files: map[string]string{"/remote/source.txt": "source\n", "/remote/destination.txt": "destination\n"}}
	docker, err := backend.NewDockerFilesystem(provider, "/remote", "docker-move-destination", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close(ctx)

	for name, workspace := range map[string]backend.Filesystem{"local": local, "docker": docker} {
		t.Run(name, func(t *testing.T) {
			_, err := workspace.ApplyPatch(ctx, patch)
			if err == nil || !strings.Contains(err.Error(), "destination already exists") {
				t.Fatalf("move error = %v", err)
			}
		})
	}
	localSource, err := os.ReadFile(filepath.Join(localRoot, "source.txt"))
	if err != nil || string(localSource) != "source\n" {
		t.Fatalf("local source changed: %q %v", localSource, err)
	}
	localDestination, err := os.ReadFile(filepath.Join(localRoot, "destination.txt"))
	if err != nil || string(localDestination) != "destination\n" {
		t.Fatalf("local destination changed: %q %v", localDestination, err)
	}
	if provider.writes != 0 || provider.files["/remote/source.txt"] != "source\n" || provider.files["/remote/destination.txt"] != "destination\n" {
		t.Fatalf("Docker move changed files: writes=%d files=%v", provider.writes, provider.files)
	}
}

func TestWorkspacePatchStopsBeforeMutationOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &cancelingPatchExistenceSandbox{fileSandbox: &fileSandbox{files: map[string]string{}}, cancel: cancel}
	workspace, err := backend.NewDockerFilesystem(provider, "/remote", "docker-canceled-patch", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close(context.Background())

	patch := "*** Begin Patch\n*** Add File: target.txt\n+created\n*** End Patch"
	_, err = workspace.ApplyPatch(ctx, patch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled patch error = %v", err)
	}
	if provider.writes != 0 {
		t.Fatalf("canceled patch wrote %d times", provider.writes)
	}
}

func TestWorkspacePatchCreateDoesNotReplaceExistingFile(t *testing.T) {
	ctx := context.Background()
	patch := "*** Begin Patch\n*** Add File: target.txt\n+first\n*** End Patch"
	localRoot := t.TempDir()
	local, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: localRoot, VirtualMode: true}, "local-no-replace")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close(ctx)
	_, err = local.ApplyPatch(ctx, patch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = local.CreateFileNoReplace(ctx, "target.txt", "second\n")
	if err == nil || !errors.Is(err, backend.ErrAlreadyExists) {
		t.Fatalf("direct local create error = %v", err)
	}
	_, err = local.ApplyPatch(ctx, patch)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("repeated add error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(localRoot, "target.txt"))
	if err != nil || string(content) != "first\n" {
		t.Fatalf("repeated add changed file: %q %v", content, err)
	}
}

type execDockerSandbox struct {
	sandbox.Sandbox
}

func (*execDockerSandbox) DockerExecTarget() (string, bool) {
	return "test-container", true
}

func (*execDockerSandbox) ResolveContainerPath(_ context.Context, path string) (string, error) {
	return path, nil
}

func TestDockerPatchFallbackUsesContainerNoReplaceCreation(t *testing.T) {
	dockerDir := t.TempDir()
	storageDir := t.TempDir()
	dockerPath := filepath.Join(dockerDir, "docker")
	// Execute the production script unchanged; replace only the Docker transport.
	script := "#!/bin/sh\n" +
		"[ \"$1\" = exec ] || exit 99\n" +
		"shift\n" +
		"if [ \"$1\" = -i ]; then shift; fi\n" +
		"shift\n" +
		"exec \"$@\"\n"
	err := os.WriteFile(dockerPath, []byte(script), 0700)
	if err != nil {
		t.Fatal(err)
	}
	previousPath := os.Getenv("PATH")
	pathValue := dockerDir
	if previousPath != "" {
		pathValue += string(os.PathListSeparator) + previousPath
	}
	t.Setenv("PATH", pathValue)

	provider := &execDockerSandbox{Sandbox: &fileSandbox{files: map[string]string{}}}
	workspace, err := backend.NewDockerFilesystem(provider, filepath.ToSlash(storageDir), "docker-exec-create", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close(context.Background())
	patch := "*** Begin Patch\n*** Add File: target.txt\n+first\n*** End Patch"
	_, err = workspace.ApplyPatch(context.Background(), patch)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(storageDir, "target.txt"))
	if err != nil || string(content) != "first\n" {
		t.Fatalf("container create content: %q %v", content, err)
	}
	_, err = workspace.CreateFileNoReplace(context.Background(), "target.txt", "second\n")
	if err == nil || !errors.Is(err, backend.ErrAlreadyExists) {
		t.Fatalf("direct container create error = %v", err)
	}
	_, err = workspace.ApplyPatch(context.Background(), patch)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("repeated container add error = %v", err)
	}
	content, err = os.ReadFile(filepath.Join(storageDir, "target.txt"))
	if err != nil || string(content) != "first\n" {
		t.Fatalf("repeated container add changed file: %q %v", content, err)
	}
	// Competing calls must never truncate the winner's file.
	var group sync.WaitGroup
	start := make(chan struct{})
	successes := make(chan string, 8)
	for i := 0; i < 8; i++ {
		content := strings.Repeat(string(rune('a'+i)), 1000)
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, createErr := workspace.CreateFileNoReplace(context.Background(), "race.txt", content)
			if createErr == nil {
				successes <- content
			}
		}()
	}
	close(start)
	group.Wait()
	close(successes)
	if len(successes) != 1 {
		t.Fatalf("successful competing creates=%d", len(successes))
	}
	winner := <-successes
	content, err = os.ReadFile(filepath.Join(storageDir, "race.txt"))
	if err != nil || string(content) != winner {
		t.Fatalf("winner overwritten: %v", err)
	}

	outside := t.TempDir()
	err = os.Symlink(outside, filepath.Join(storageDir, "escape"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = workspace.CreateFileNoReplace(context.Background(), "escape/new.txt", "forbidden")
	if err == nil {
		t.Fatal("Docker patch followed symlink parent")
	}
	_, err = os.Stat(filepath.Join(outside, "new.txt"))
	if !os.IsNotExist(err) {
		t.Fatalf("outside file changed: %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(storageDir, ".patch-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files leaked: %v %v", leftovers, err)
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
	ws, err := backend.NewDockerFilesystem(provider, "/remote", "docker-thread", nil)
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
	ws, err := backend.NewDockerFilesystem(provider, root, "real-docker-thread", nil)
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

type rejectedPatchWrite struct{ backend.Filesystem }

func (*rejectedPatchWrite) FileExists(_ context.Context, name string) (bool, error) {
	return filepath.Base(name) == "source", nil
}
func (*rejectedPatchWrite) CreateFileNoReplace(context.Context, string, string) (*backend.WriteResult, error) {
	return &backend.WriteResult{Error: "write rejected"}, nil
}
func TestPatchDoesNotDeleteSourceAfterResultError(t *testing.T) {
	local, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}, "patch-result")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close(context.Background())
	_, err = local.Write(context.Background(), "source", "original\n")
	if err != nil {
		t.Fatal(err)
	}
	// Resolve returns absolute paths; existence checks use the basename.
	patch := "*** Begin Patch\n*** Update File: source\n*** Move to: target\n@@\n-original\n+changed\n*** End Patch"
	_, err = backend.ApplyWorkspacePatch(context.Background(), &rejectedPatchWrite{local}, patch)
	if err == nil || !strings.Contains(err.Error(), "write rejected") {
		t.Fatalf("error=%v", err)
	}
	content, err := local.Read(context.Background(), "source", nil, nil)
	if err != nil || content != "original\n" {
		t.Fatalf("source changed: %q %v", content, err)
	}
}
