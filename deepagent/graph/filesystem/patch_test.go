package filesystem_test

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

	filesystempkg "eino-cli/deepagent/graph/filesystem"
	agentmodel "eino-cli/deepagent/model"
)

func (fileSandbox *fileSandbox) FileExists(ctx context.Context, path string) (bool, error) {
	contextErr := ctx.Err()
	if contextErr != nil {
		return false, contextErr
	}
	if fileSandbox.err != nil {
		return false, fileSandbox.err
	}
	_, exists := fileSandbox.files[path]
	return exists, nil
}

func (fileSandbox *fileSandbox) CreateFileNoReplace(ctx context.Context, path, content string) error {
	contextErr := ctx.Err()
	if contextErr != nil {
		return contextErr
	}
	if fileSandbox.err != nil {
		return fileSandbox.err
	}
	_, exists := fileSandbox.files[path]
	if exists {
		return agentmodel.ErrAlreadyExists
	}
	fileSandbox.files[path] = content
	fileSandbox.writes++
	return nil
}

type cancelingPatchExistenceSandbox struct {
	*fileSandbox
	cancel context.CancelFunc
}

func (cancelingPatchExistenceSandbox *cancelingPatchExistenceSandbox) FileExists(ctx context.Context, path string) (bool, error) {
	exists, err := cancelingPatchExistenceSandbox.fileSandbox.FileExists(ctx, path)
	cancelingPatchExistenceSandbox.cancel()
	return exists, err
}

func TestWorkspacePatchHasSameLocalAndDockerBehavior(t *testing.T) {
	ctx := context.Background()
	localRoot := t.TempDir()
	writeErr := os.WriteFile(filepath.Join(localRoot, "a.txt"), []byte("old\n"), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	localFilesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: localRoot, VirtualMode: true}, "local-thread")
	if err != nil {
		t.Fatal(err)
	}
	defer localFilesystem.Close(ctx)
	provider := &fileSandbox{files: map[string]string{"/remote/a.txt": "old\n"}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "docker-thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerFilesystem.Close(ctx)
	for workspaceName, filesystem := range map[string]agentmodel.ToolFilesystem{"local": localFilesystem, "docker": dockerFilesystem} {
		t.Run(workspaceName, func(t *testing.T) {
			invalidPatch := "*** Begin Patch\n*** Update File: a.txt\n@@\n-missing\n+new\n*** End Patch"
			_, stalePatchErr := filesystem.ApplyPatch(ctx, invalidPatch)
			if stalePatchErr == nil {
				t.Fatal("stale patch succeeded")
			}
			patch := "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** Add File: b.txt\n+created\n*** End Patch"
			_, applyPatchErr := filesystem.ApplyPatch(ctx, patch)
			if applyPatchErr != nil {
				t.Fatal(applyPatchErr)
			}
			for filePath, expectedContent := range map[string]string{"a.txt": "new\n", "b.txt": "created\n"} {
				limit := 100
				fileContent, err := filesystem.Read(ctx, filePath, nil, &limit)
				if err != nil || fileContent != expectedContent {
					t.Fatalf("%s: %q, %v", filePath, fileContent, err)
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
			localFilesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: localRoot, VirtualMode: true}, "local-patch-validation")
			if err != nil {
				t.Fatal(err)
			}
			defer localFilesystem.Close(ctx)

			provider := &fileSandbox{files: map[string]string{"/remote/a": "old\n"}}
			dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "docker-patch-validation", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer dockerFilesystem.Close(ctx)

			workspaces := map[string]agentmodel.Filesystem{
				"local":  localFilesystem,
				"docker": dockerFilesystem,
			}
			for workspaceName, filesystem := range workspaces {
				t.Run(workspaceName, func(t *testing.T) {
					_, err := filesystem.ApplyPatch(ctx, testCase.patch)
					if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
						t.Fatalf("patch error = %v; want %q", err, testCase.wantErr)
					}
					if provider.writes != 0 {
						t.Fatalf("rejected patch wrote %d times", provider.writes)
					}
					content, readErr := filesystem.Read(ctx, "a", nil, nil)
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
	oversizedContent := strings.Repeat("x", (agentmodel.MaxFileSizeMB<<20)+1)
	addPatch := "*** Begin Patch\n*** Add File: target.txt\n+replacement\n*** End Patch"
	deletePatch := "*** Begin Patch\n*** Delete File: target.txt\n*** End Patch"

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
	err = os.WriteFile(localTarget, []byte(oversizedContent), 0600)
	if err != nil {
		t.Fatal(err)
	}
	localFilesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: localRoot, VirtualMode: true, MaxFileSizeMB: 1}, "local-oversized-patch")
	if err != nil {
		t.Fatal(err)
	}
	defer localFilesystem.Close(ctx)

	provider := &fileSandbox{files: map[string]string{"/remote/target.txt": oversizedContent}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "docker-oversized-patch", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerFilesystem.Close(ctx)

	for workspaceName, filesystem := range map[string]agentmodel.Filesystem{"local": localFilesystem, "docker": dockerFilesystem} {
		t.Run(workspaceName, func(t *testing.T) {
			_, err := filesystem.ApplyPatch(ctx, addPatch)
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("add error = %v", err)
			}
		})
	}
	localContent, err := os.ReadFile(localTarget)
	if err != nil || string(localContent) != oversizedContent {
		t.Fatalf("local oversized target changed: %d bytes, %v", len(localContent), err)
	}
	if provider.files["/remote/target.txt"] != oversizedContent {
		t.Fatal("Docker oversized target changed")
	}

	_, err = localFilesystem.ApplyPatch(ctx, deletePatch)
	if err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(localTarget)
	if !os.IsNotExist(statErr) {
		t.Fatalf("local oversized target was not deleted: %v", err)
	}
	_, err = dockerFilesystem.ApplyPatch(ctx, deletePatch)
	if err != nil {
		t.Fatal(err)
	}
	dockerLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerLog), "target.txt") {
		t.Fatalf("Docker delete command missing target: %q", dockerLog)
	}
}

func TestWorkspacePatchRejectsArbitraryAddReadErrors(t *testing.T) {
	sentinel := errors.New("provider read failed")
	provider := &fileSandbox{files: map[string]string{"/remote/target.txt": "original"}, err: sentinel}
	filesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "docker-add-error", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(context.Background())

	patch := "*** Begin Patch\n*** Add File: target.txt\n+replacement\n*** End Patch"
	_, err = filesystem.ApplyPatch(context.Background(), patch)
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
	localFilesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: localRoot, VirtualMode: true}, "local-move-destination")
	if err != nil {
		t.Fatal(err)
	}
	defer localFilesystem.Close(ctx)

	provider := &fileSandbox{files: map[string]string{"/remote/source.txt": "source\n", "/remote/destination.txt": "destination\n"}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "docker-move-destination", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerFilesystem.Close(ctx)

	for workspaceName, filesystem := range map[string]agentmodel.Filesystem{"local": localFilesystem, "docker": dockerFilesystem} {
		t.Run(workspaceName, func(t *testing.T) {
			_, err := filesystem.ApplyPatch(ctx, patch)
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
	filesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "docker-canceled-patch", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(context.Background())

	patch := "*** Begin Patch\n*** Add File: target.txt\n+created\n*** End Patch"
	_, err = filesystem.ApplyPatch(ctx, patch)
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
	localFilesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: localRoot, VirtualMode: true}, "local-no-replace")
	if err != nil {
		t.Fatal(err)
	}
	defer localFilesystem.Close(ctx)
	_, err = localFilesystem.ApplyPatch(ctx, patch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = localFilesystem.CreateFileNoReplace(ctx, "target.txt", "second\n")
	if err == nil || !errors.Is(err, agentmodel.ErrAlreadyExists) {
		t.Fatalf("direct local create error = %v", err)
	}
	_, err = localFilesystem.ApplyPatch(ctx, patch)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("repeated add error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(localRoot, "target.txt"))
	if err != nil || string(content) != "first\n" {
		t.Fatalf("repeated add changed file: %q %v", content, err)
	}
}

type execDockerSandbox struct {
	agentmodel.Sandbox
}

func (*execDockerSandbox) GetDockerExecTarget() (string, bool) {
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
	filesystem, err := filesystempkg.NewDockerFilesystem(provider, filepath.ToSlash(storageDir), "docker-exec-create", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(context.Background())
	patch := "*** Begin Patch\n*** Add File: target.txt\n+first\n*** End Patch"
	_, err = filesystem.ApplyPatch(context.Background(), patch)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(storageDir, "target.txt"))
	if err != nil || string(content) != "first\n" {
		t.Fatalf("container create content: %q %v", content, err)
	}
	_, err = filesystem.CreateFileNoReplace(context.Background(), "target.txt", "second\n")
	if err == nil || !errors.Is(err, agentmodel.ErrAlreadyExists) {
		t.Fatalf("direct container create error = %v", err)
	}
	_, err = filesystem.ApplyPatch(context.Background(), patch)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("repeated container add error = %v", err)
	}
	content, err = os.ReadFile(filepath.Join(storageDir, "target.txt"))
	if err != nil || string(content) != "first\n" {
		t.Fatalf("repeated container add changed file: %q %v", content, err)
	}
	// Competing calls must never truncate the winner's file.
	var createGroup sync.WaitGroup
	startCreates := make(chan struct{})
	successfulContents := make(chan string, 8)
	for i := 0; i < 8; i++ {
		content := strings.Repeat(string(rune('a'+i)), 1000)
		createGroup.Add(1)
		go func() {
			defer createGroup.Done()
			<-startCreates
			_, createErr := filesystem.CreateFileNoReplace(context.Background(), "race.txt", content)
			if createErr == nil {
				successfulContents <- content
			}
		}()
	}
	close(startCreates)
	createGroup.Wait()
	close(successfulContents)
	if len(successfulContents) != 1 {
		t.Fatalf("successful competing creates=%d", len(successfulContents))
	}
	winningContent := <-successfulContents
	content, err = os.ReadFile(filepath.Join(storageDir, "race.txt"))
	if err != nil || string(content) != winningContent {
		t.Fatalf("winner overwritten: %v", err)
	}

	outsideDir := t.TempDir()
	err = os.Symlink(outsideDir, filepath.Join(storageDir, "escape"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = filesystem.CreateFileNoReplace(context.Background(), "escape/new.txt", "forbidden")
	if err == nil {
		t.Fatal("Docker patch followed symlink parent")
	}
	_, err = os.Stat(filepath.Join(outsideDir, "new.txt"))
	if !os.IsNotExist(err) {
		t.Fatalf("outside file changed: %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(storageDir, ".patch-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files leaked: %v %v", leftovers, err)
	}

}

func TestDockerCommandRunsInSelectedContainer(t *testing.T) {
	dockerDir := t.TempDir()
	dockerPath := filepath.Join(dockerDir, "docker")
	writeErr := os.WriteFile(dockerPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider := &fileSandbox{files: map[string]string{}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "docker-thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerFilesystem.Close(context.Background())
	result, err := dockerFilesystem.Execute(context.Background(), agentmodel.CommandRequest{Command: "pwd"})
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
	imageName := os.Getenv("DEEPAGENT_DOCKER_TEST_IMAGE")
	if imageName == "" {
		t.Skip("set DEEPAGENT_DOCKER_TEST_IMAGE to run the real container test")
	}
	rootDir, err := os.MkdirTemp("/private/tmp", "deepagent-docker-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(rootDir)
	dockerCommand := exec.Command("docker", "run", "-d", "--rm", "-v", rootDir+":"+rootDir, "--entrypoint", "/bin/sh", imageName, "-c", "sleep 120")
	dockerOutput, err := dockerCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("start container: %s: %v", dockerOutput, err)
	}
	containerID := strings.TrimSpace(string(dockerOutput))
	defer exec.Command("docker", "rm", "-f", containerID).Run()
	provider := &fileSandbox{containerID: containerID, files: map[string]string{}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, rootDir, "real-docker-thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerFilesystem.Close(context.Background())
	result, err := dockerFilesystem.Execute(context.Background(), agentmodel.CommandRequest{Command: "printf docker-ok"})
	if err != nil || result.ExitCode != 0 || result.Output != "docker-ok" {
		t.Fatalf("command=%+v err=%v", result, err)
	}
	filePath := filepath.Join(rootDir, "remove.txt")
	writeErr := os.WriteFile(filePath, []byte("remove me"), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	_, deleteErr := dockerFilesystem.Delete(context.Background(), "remove.txt")
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	_, deleteStatErr := os.Stat(filePath)
	if !os.IsNotExist(deleteStatErr) {
		t.Fatalf("Docker delete did not remove mounted file: %v", deleteStatErr)
	}
	jobID, err := dockerFilesystem.Start(context.Background(), agentmodel.CommandRequest{Command: "sleep 2; printf survived > " + filepath.Join(rootDir, "leak.txt")})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	cancelErr := dockerFilesystem.Cancel(context.Background(), jobID)
	if cancelErr != nil {
		t.Fatal(cancelErr)
	}
	time.Sleep(2500 * time.Millisecond)
	_, cancelStatErr := os.Stat(filepath.Join(rootDir, "leak.txt"))
	if !os.IsNotExist(cancelStatErr) {
		t.Fatalf("cancelled Docker job continued running: %v", cancelStatErr)
	}
	timedJobID, err := dockerFilesystem.Start(context.Background(), agentmodel.CommandRequest{Command: "sleep 2; printf survived > " + filepath.Join(rootDir, "timeout-leak.txt"), Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, waitErr := dockerFilesystem.Wait(context.Background(), timedJobID, "", 0)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	time.Sleep(2500 * time.Millisecond)
	_, timeoutStatErr := os.Stat(filepath.Join(rootDir, "timeout-leak.txt"))
	if !os.IsNotExist(timeoutStatErr) {
		t.Fatalf("timed out Docker job continued running: %v", timeoutStatErr)
	}
	_, startErr := dockerFilesystem.Start(context.Background(), agentmodel.CommandRequest{Command: "sleep 2; printf survived > " + filepath.Join(rootDir, "close-leak.txt")})
	if startErr != nil {
		t.Fatal(startErr)
	}
	time.Sleep(100 * time.Millisecond)
	closeErr := dockerFilesystem.Close(context.Background())
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	time.Sleep(2500 * time.Millisecond)
	_, closeStatErr := os.Stat(filepath.Join(rootDir, "close-leak.txt"))
	if !os.IsNotExist(closeStatErr) {
		t.Fatalf("closed Docker workspace left job running: %v", closeStatErr)
	}
}

type rejectedPatchWrite struct{ agentmodel.Filesystem }

func (*rejectedPatchWrite) HasFile(_ context.Context, filePath string) (bool, error) {
	return filepath.Base(filePath) == "source", nil
}

func (*rejectedPatchWrite) CreateFileNoReplace(context.Context, string, string) (*agentmodel.WriteResult, error) {
	return &agentmodel.WriteResult{Error: "write rejected"}, nil
}

func TestPatchDoesNotDeleteSourceAfterResultError(t *testing.T) {
	localFilesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}, "patch-result")
	if err != nil {
		t.Fatal(err)
	}
	defer localFilesystem.Close(context.Background())
	_, err = localFilesystem.Write(context.Background(), "source", "original\n")
	if err != nil {
		t.Fatal(err)
	}
	// Resolve returns absolute paths; existence checks use the basename.
	patch := "*** Begin Patch\n*** Update File: source\n*** Move to: target\n@@\n-original\n+changed\n*** End Patch"
	_, err = filesystempkg.ApplyWorkspacePatch(context.Background(), &rejectedPatchWrite{localFilesystem}, patch)
	if err == nil || !strings.Contains(err.Error(), "write rejected") {
		t.Fatalf("error=%v", err)
	}
	content, err := localFilesystem.Read(context.Background(), "source", nil, nil)
	if err != nil || content != "original\n" {
		t.Fatalf("source changed: %q %v", content, err)
	}
}
