package filesystem_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	filesystempkg "eino-cli/deepagent/graph/filesystem"
	"eino-cli/deepagent/graph/tools"
	agentmodel "eino-cli/deepagent/model"

	einotool "github.com/cloudwego/eino/components/tool"
)

type fileSandbox struct {
	agentmodel.Sandbox
	containerID string
	files       map[string]string
	writes      int
	err         error
	lastPath    string
	grepOpts    agentmodel.SandboxGrepOptions
	resolved    map[string]string
}

func (fileSandbox *fileSandbox) GetDockerExecTarget() (string, bool) {
	if fileSandbox.containerID != "" {
		return fileSandbox.containerID, true
	}
	return "test-container", true
}

func (fileSandbox *fileSandbox) ResolveContainerPath(_ context.Context, path string) (string, error) {
	target := fileSandbox.resolved[path]
	if target != "" {
		return target, nil
	}
	return path, nil
}

func (fileSandbox *fileSandbox) ReadFile(_ context.Context, path string) (string, error) {
	fileSandbox.lastPath = path
	if fileSandbox.err != nil {
		return "", fileSandbox.err
	}
	content, ok := fileSandbox.files[path]
	if !ok {
		return "", os.ErrNotExist
	}
	return content, nil
}

func (fileSandbox *fileSandbox) WriteFile(_ context.Context, path, content string, appendMode bool) error {
	fileSandbox.lastPath = path
	if fileSandbox.err != nil {
		return fileSandbox.err
	}
	fileSandbox.writes++
	if appendMode {
		fileSandbox.files[path] += content
	} else {
		fileSandbox.files[path] = content
	}
	return nil
}

func (fileSandbox *fileSandbox) UpdateFile(ctx context.Context, path string, contentBytes []byte) error {
	return fileSandbox.WriteFile(ctx, path, string(contentBytes), false)
}

func (fileSandbox *fileSandbox) ListDir(_ context.Context, path string, depth int) ([]string, error) {
	fileSandbox.lastPath = path
	return []string{path + "/dir/", path + "/a.txt"}, fileSandbox.err
}

func (fileSandbox *fileSandbox) Glob(_ context.Context, path, pattern string, _ agentmodel.SandboxGlobOptions) ([]string, bool, error) {
	fileSandbox.lastPath = path
	return []string{path + "/a.txt"}, false, fileSandbox.err
}

func (fileSandbox *fileSandbox) Grep(_ context.Context, path, pattern string, grepOptions agentmodel.SandboxGrepOptions) ([]agentmodel.SandboxGrepMatch, bool, error) {
	fileSandbox.lastPath = path
	fileSandbox.grepOpts = grepOptions
	return []agentmodel.SandboxGrepMatch{{Path: path + "/a.txt", LineNumber: 2, Line: "second"}}, false, fileSandbox.err
}

func TestDockerFilesystemToolsUseProvider(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{"/remote/a.txt": "first\nsecond\n"}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerFilesystem.Close(ctx)
	toolsByName := map[string]einotool.InvokableTool{}
	toolDescriptors, err := tools.NewFilesystemTools(dockerFilesystem, tools.FilesystemToolOptions{EnableCommands: true, EnablePatch: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, toolDescriptor := range toolDescriptors {
		toolInfo, err := toolDescriptor.Tool.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		toolsByName[toolInfo.Name] = toolDescriptor.Tool.(einotool.InvokableTool)
	}
	readOutput, err := toolsByName["read_file"].InvokableRun(ctx, `{"path":"a.txt","offset":2,"limit":1}`)
	if err != nil || !strings.Contains(readOutput, "second") || strings.Contains(readOutput, "first") || provider.lastPath != "/remote/a.txt" {
		t.Fatalf("read=%q path=%q err=%v", readOutput, provider.lastPath, err)
	}
	_, err = toolsByName["edit_file"].InvokableRun(ctx, `{"path":"a.txt","old":"second","new":"changed"}`)
	if err != nil || provider.files["/remote/a.txt"] != "first\nchanged\n" || provider.writes != 1 {
		t.Fatalf("edit=%v files=%v", err, provider.files)
	}
	_, err = toolsByName["write_file"].InvokableRun(ctx, `{"path":"b.txt","content":"new"}`)
	if err != nil || provider.files["/remote/b.txt"] != "new" {
		t.Fatalf("write=%v files=%v", err, provider.files)
	}
	grepOutput, err := toolsByName["grep"].InvokableRun(ctx, `{"pattern":"second","glob":"*.txt"}`)
	if err != nil || grepOutput != "/remote/a.txt:2:second" || provider.grepOpts.Glob != "*.txt" || !provider.grepOpts.CaseSensitive {
		t.Fatalf("grep=%q %v opts=%+v", grepOutput, err, provider.grepOpts)
	}
	entries, err := dockerFilesystem.List(ctx, "")
	if err != nil || len(entries) != 2 || !entries[0].IsDir || entries[1].IsDir {
		t.Fatalf("list=%v %v", entries, err)
	}
	matches, err := dockerFilesystem.Glob(ctx, "*.txt", "")
	if err != nil || len(matches) != 1 || matches[0].Path != "/remote/a.txt" {
		t.Fatalf("glob=%v %v", matches, err)
	}
	changeDirErr := dockerFilesystem.ChangeDir(ctx, "dir")
	if changeDirErr != nil {
		t.Fatal(changeDirErr)
	}
	_, writeErr := dockerFilesystem.Write(ctx, "x", "nested")
	if writeErr != nil || provider.files["/remote/dir/x"] != "nested" {
		t.Fatalf("cwd=%v files=%v", writeErr, provider.files)
	}
	uploads, err := dockerFilesystem.UploadFiles(ctx, []struct {
		Path    string
		Content []byte
	}{{Path: "binary", Content: []byte{0, 255}}})
	if err != nil || len(uploads) != 1 || provider.files["/remote/dir/binary"] != string([]byte{0, 255}) {
		t.Fatalf("upload=%v %v", uploads, err)
	}
}

func TestDockerFilesystemToolArgumentPresenceAndReplaceAll(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{
		"/remote/data.txt":   "twice twice",
		"/remote/remove.txt": "remove me",
	}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeTool := tools.NewWriteFileTool(dockerFilesystem).Tool.(einotool.InvokableTool)
	editTool := tools.NewEditFileTool(dockerFilesystem).Tool.(einotool.InvokableTool)

	for _, arguments := range []string{
		`{"path":"data.txt"}`,
		`{"path":"data.txt","content":null}`,
	} {
		_, err = writeTool.InvokableRun(ctx, arguments)
		if err == nil {
			t.Fatalf("accepted write arguments: %s", arguments)
		}
	}
	if provider.files["/remote/data.txt"] != "twice twice" || provider.writes != 0 {
		t.Fatalf("invalid write changed provider: files=%v writes=%d", provider.files, provider.writes)
	}

	for _, arguments := range []string{
		`{"path":"data.txt","old":"twice"}`,
		`{"path":"data.txt","old":"twice","new":null}`,
	} {
		_, err = editTool.InvokableRun(ctx, arguments)
		if err == nil {
			t.Fatalf("accepted edit arguments: %s", arguments)
		}
	}
	if provider.files["/remote/data.txt"] != "twice twice" || provider.writes != 0 {
		t.Fatalf("invalid edit changed provider: files=%v writes=%d", provider.files, provider.writes)
	}

	_, err = writeTool.InvokableRun(ctx, `{"path":"empty.txt","content":""}`)
	if err != nil {
		t.Fatal(err)
	}
	emptyContent, ok := provider.files["/remote/empty.txt"]
	if err != nil || !ok || emptyContent != "" {
		t.Fatalf("explicit empty write failed: %q %v", emptyContent, err)
	}
	_, err = editTool.InvokableRun(ctx, `{"path":"remove.txt","old":"remove","new":""}`)
	if err != nil || provider.files["/remote/remove.txt"] != " me" {
		t.Fatalf("explicit empty edit failed: %q %v", provider.files["/remote/remove.txt"], err)
	}

	_, err = editTool.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"once","replace_all":false}`)
	if err == nil {
		t.Fatal("replace_all=false accepted ambiguous edit")
	}
	if provider.files["/remote/data.txt"] != "twice twice" {
		t.Fatal("ambiguous edit changed provider")
	}
	_, err = editTool.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"once","replace_all":true}`)
	if err != nil || provider.files["/remote/data.txt"] != "once once" {
		t.Fatalf("replace_all edit failed: %q %v", provider.files["/remote/data.txt"], err)
	}
}

func TestDockerFilesystemFailureDoesNotWriteOrUseHost(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{"/remote/a": "repeat repeat"}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, oldText := range []string{"", "missing", "repeat"} {
		_, err := dockerFilesystem.Edit(ctx, "a", oldText, "new", false)
		if err == nil {
			t.Fatalf("accepted %q", oldText)
		}
	}
	if provider.writes != 0 {
		t.Fatal("failed edit mutated provider")
	}
	sentinel := errors.New("provider unavailable")
	provider.err = sentinel
	_, readErr := dockerFilesystem.Read(ctx, "a", nil, nil)
	if !errors.Is(readErr, sentinel) {
		t.Fatal(readErr)
	}
	_, providerWriteErr := dockerFilesystem.Write(ctx, "a", "new")
	if !errors.Is(providerWriteErr, sentinel) {
		t.Fatal(providerWriteErr)
	}
	changeDirErr := dockerFilesystem.ChangeDir(ctx, "bad")
	if !errors.Is(changeDirErr, sentinel) {
		t.Fatal(changeDirErr)
	}
	provider.err = nil
	_, writeAfterChangeDirErr := dockerFilesystem.Write(ctx, "after", "x")
	if writeAfterChangeDirErr != nil || provider.lastPath != "/remote/after" {
		t.Fatalf("failed chdir changed cwd: %s %v", provider.lastPath, writeAfterChangeDirErr)
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, writeErr := dockerFilesystem.Write(canceledCtx, "a", "x")
	if !errors.Is(writeErr, context.Canceled) {
		t.Fatal(writeErr)
	}
	if provider.writes != 1 {
		t.Fatal("canceled call reached provider")
	}
	_, missingProviderErr := filesystempkg.NewDockerFilesystem(nil, "/remote", "thread", nil)
	if missingProviderErr == nil {
		t.Fatal("nil provider accepted")
	}
	_, invalidRootErr := filesystempkg.NewDockerFilesystem(provider, "relative", "thread", nil)
	if invalidRootErr == nil {
		t.Fatal("relative root accepted")
	}
}

func TestDockerFilesystemRejectsSymlinkOutsideWorkspace(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{}, resolved: map[string]string{"/remote/escape/file": "/outside/file"}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerFilesystem.Close(ctx)
	_, writeErr := dockerFilesystem.Write(ctx, "escape/file", "outside")
	if !errors.Is(writeErr, agentmodel.ErrInvalidPath) {
		t.Fatalf("write through escaping symlink = %v", writeErr)
	}
	_, readErr := dockerFilesystem.Read(ctx, "escape/file", nil, nil)
	if !errors.Is(readErr, agentmodel.ErrInvalidPath) {
		t.Fatalf("read through escaping symlink = %v", readErr)
	}
	if provider.writes != 0 || provider.lastPath != "" {
		t.Fatal("escaping path reached the sandbox file API")
	}
}

func TestDockerFilesystemAllowsSymlinkInsideWorkspace(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{"/remote/alias": "inside"}, resolved: map[string]string{"/remote/alias": "/remote/target"}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerFilesystem.Close(ctx)
	content, err := dockerFilesystem.Read(ctx, "alias", nil, nil)
	if err != nil || content != "inside" {
		t.Fatalf("in-root symlink read = %q, %v", content, err)
	}
}

type cancelAfterReadSandbox struct {
	*fileSandbox
	cancel context.CancelFunc
}

func (cancelAfterReadSandbox *cancelAfterReadSandbox) ReadFile(ctx context.Context, path string) (string, error) {
	content, err := cancelAfterReadSandbox.fileSandbox.ReadFile(ctx, path)
	cancelAfterReadSandbox.cancel()
	return content, err
}

func TestDockerEditDoesNotWriteAfterReadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &cancelAfterReadSandbox{fileSandbox: &fileSandbox{files: map[string]string{"/remote/a": "original"}}, cancel: cancel}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dockerFilesystem.Edit(ctx, "a", "original", "changed", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("edit lost cancellation: %v", err)
	}
	if provider.writes != 0 || provider.files["/remote/a"] != "original" {
		t.Fatal("edit wrote after canceled read")
	}
}

type cancelingResolverSandbox struct {
	*fileSandbox
	cancel context.CancelFunc
}

func (cancelingResolverSandbox *cancelingResolverSandbox) ResolveContainerPath(_ context.Context, path string) (string, error) {
	cancelingResolverSandbox.cancel()
	return "", errors.New("resolver canceled")
}

func TestDockerFilesystemDeletePreservesResolverCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &cancelingResolverSandbox{
		fileSandbox: &fileSandbox{files: map[string]string{}},
		cancel:      cancel,
	}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dockerFilesystem.Delete(ctx, "a")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("delete lost resolver cancellation: %v", err)
	}
	if !strings.Contains(err.Error(), "resolver canceled") {
		t.Fatalf("delete lost resolver diagnostic: %v", err)
	}
}

func TestDockerFilesystemDeleteCancelsInFlightDockerCommand(t *testing.T) {
	dockerDir := t.TempDir()
	readyPath := filepath.Join(dockerDir, "ready")
	dockerPath := filepath.Join(dockerDir, "docker")
	script := "#!/bin/sh\n" +
		"set -eu\n" +
		"tmp=\"$DOCKER_READY_FILE.tmp\"\n" +
		"printf '%s\\n' \"$$\" > \"$tmp\"\n" +
		"mv \"$tmp\" \"$DOCKER_READY_FILE\"\n" +
		"while :; do\n" +
		"  kill -STOP $$\n" +
		"done\n"
	err := os.WriteFile(dockerPath, []byte(script), 0755)
	if err != nil {
		t.Fatal(err)
	}
	previousPath := os.Getenv("PATH")
	pathValue := dockerDir
	if previousPath != "" {
		pathValue += string(os.PathListSeparator) + previousPath
	}
	t.Setenv("PATH", pathValue)
	t.Setenv("DOCKER_READY_FILE", readyPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &fileSandbox{files: map[string]string{}}
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, deleteErr := dockerFilesystem.Delete(ctx, "a")
		resultCh <- deleteErr
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("docker delete did not stop during cleanup")
		}
	}()

	readyTicker := time.NewTicker(10 * time.Millisecond)
	defer readyTicker.Stop()
	readyDeadline := time.NewTimer(10 * time.Second)
	defer readyDeadline.Stop()
	for {
		_, statErr := os.Stat(readyPath)
		if statErr == nil {
			break
		}
		if !os.IsNotExist(statErr) {
			t.Fatal(statErr)
		}
		select {
		case earlyErr := <-resultCh:
			t.Fatalf("docker delete exited before readiness: %v", earlyErr)
		case <-readyTicker.C:
		case <-readyDeadline.C:
			cancel()
			t.Fatal("fake docker did not become ready")
		}
	}
	cancel()

	resultDeadline := time.NewTimer(2 * time.Second)
	defer resultDeadline.Stop()
	var deleteErr error
	select {
	case deleteErr = <-resultCh:
	case <-resultDeadline.C:
		t.Fatal("canceled docker delete did not finish")
	}
	if !errors.Is(deleteErr, context.Canceled) {
		t.Fatalf("delete lost command cancellation: %v", deleteErr)
	}
	if !strings.Contains(deleteErr.Error(), "docker delete a:") {
		t.Fatalf("delete lost command diagnostic: %v", deleteErr)
	}
}

func TestDockerFilesystemCloseReleasesContainerOnce(t *testing.T) {
	var releaseCount atomic.Int32
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(&fileSandbox{}, "/remote", "thread", func() { releaseCount.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	var closeCallers sync.WaitGroup
	for i := 0; i < 8; i++ {
		closeCallers.Add(1)
		go func() {
			defer closeCallers.Done()
			closeErr := dockerFilesystem.Close(context.Background())
			if closeErr != nil {
				t.Error(closeErr)
			}
		}()
	}
	closeCallers.Wait()
	if releaseCount.Load() != 1 {
		t.Fatalf("container releases=%d, want 1", releaseCount.Load())
	}
}

func TestDockerFilesystemConstructionFailureReleasesContainer(t *testing.T) {
	for _, rootDir := range []string{"relative", "/remote"} {
		t.Run(rootDir, func(t *testing.T) {
			releaseCount := 0
			dockerFilesystem, err := filesystempkg.NewDockerFilesystem(&fileSandbox{}, rootDir, "", func() { releaseCount++ })
			if err == nil || dockerFilesystem != nil || releaseCount != 1 {
				t.Fatalf("failed construction must release ownership: filesystem=%v error=%v releases=%d", dockerFilesystem, err, releaseCount)
			}
		})
	}
}
