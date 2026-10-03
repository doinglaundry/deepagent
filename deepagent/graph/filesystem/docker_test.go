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
	"eino-cli/deepagent/sandbox"

	einotool "github.com/cloudwego/eino/components/tool"
)

type fileSandbox struct {
	sandbox.Sandbox
	containerID string
	files       map[string]string
	writes      int
	err         error
	lastPath    string
	grepOpts    sandbox.GrepOpts
	resolved    map[string]string
}

func (s *fileSandbox) DockerExecTarget() (string, bool) {
	if s.containerID != "" {
		return s.containerID, true
	}
	return "test-container", true
}

func (s *fileSandbox) ResolveContainerPath(_ context.Context, p string) (string, error) {
	target := s.resolved[p]
	if target != "" {
		return target, nil
	}
	return p, nil
}

func (s *fileSandbox) ReadFile(_ context.Context, p string) (string, error) {
	s.lastPath = p
	if s.err != nil {
		return "", s.err
	}
	content, ok := s.files[p]
	if !ok {
		return "", os.ErrNotExist
	}
	return content, nil
}

func (s *fileSandbox) WriteFile(_ context.Context, p, c string, appendMode bool) error {
	s.lastPath = p
	if s.err != nil {
		return s.err
	}
	s.writes++
	if appendMode {
		s.files[p] += c
	} else {
		s.files[p] = c
	}
	return nil
}

func (s *fileSandbox) UpdateFile(ctx context.Context, p string, c []byte) error {
	return s.WriteFile(ctx, p, string(c), false)
}

func (s *fileSandbox) ListDir(_ context.Context, p string, depth int) ([]string, error) {
	s.lastPath = p
	return []string{p + "/dir/", p + "/a.txt"}, s.err
}

func (s *fileSandbox) Glob(_ context.Context, p, pattern string, _ sandbox.GlobOpts) ([]string, bool, error) {
	s.lastPath = p
	return []string{p + "/a.txt"}, false, s.err
}

func (s *fileSandbox) Grep(_ context.Context, p, pattern string, opts sandbox.GrepOpts) ([]sandbox.GrepMatch, bool, error) {
	s.lastPath = p
	s.grepOpts = opts
	return []sandbox.GrepMatch{{Path: p + "/a.txt", LineNumber: 2, Line: "second"}}, false, s.err
}

func TestDockerFilesystemToolsUseProvider(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{"/remote/a.txt": "first\nsecond\n"}}
	b, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(ctx)
	registered := map[string]einotool.InvokableTool{}
	items, err := tools.NewFilesystemTools(b, tools.FilesystemToolOptions{EnableCommands: true, EnablePatch: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		info, err := item.Tool.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		registered[info.Name] = item.Tool.(einotool.InvokableTool)
	}
	read, err := registered["read_file"].InvokableRun(ctx, `{"path":"a.txt","offset":2,"limit":1}`)
	if err != nil || !strings.Contains(read, "second") || strings.Contains(read, "first") || provider.lastPath != "/remote/a.txt" {
		t.Fatalf("read=%q path=%q err=%v", read, provider.lastPath, err)
	}
	_, err = registered["edit_file"].InvokableRun(ctx, `{"path":"a.txt","old":"second","new":"changed"}`)
	if err != nil || provider.files["/remote/a.txt"] != "first\nchanged\n" || provider.writes != 1 {
		t.Fatalf("edit=%v files=%v", err, provider.files)
	}
	_, err = registered["write_file"].InvokableRun(ctx, `{"path":"b.txt","content":"new"}`)
	if err != nil || provider.files["/remote/b.txt"] != "new" {
		t.Fatalf("write=%v files=%v", err, provider.files)
	}
	out, err := registered["grep"].InvokableRun(ctx, `{"pattern":"second","glob":"*.txt"}`)
	if err != nil || out != "/remote/a.txt:2:second" || provider.grepOpts.Glob != "*.txt" || !provider.grepOpts.CaseSensitive {
		t.Fatalf("grep=%q %v opts=%+v", out, err, provider.grepOpts)
	}
	entries, err := b.List(ctx, "")
	if err != nil || len(entries) != 2 || !entries[0].IsDir || entries[1].IsDir {
		t.Fatalf("list=%v %v", entries, err)
	}
	matches, err := b.Glob(ctx, "*.txt", "")
	if err != nil || len(matches) != 1 || matches[0].Path != "/remote/a.txt" {
		t.Fatalf("glob=%v %v", matches, err)
	}
	changeDirErr := b.ChangeDir(ctx, "dir")
	if changeDirErr != nil {
		t.Fatal(changeDirErr)
	}
	_, writeErr := b.Write(ctx, "x", "nested")
	if writeErr != nil || provider.files["/remote/dir/x"] != "nested" {
		t.Fatalf("cwd=%v files=%v", writeErr, provider.files)
	}
	uploads, err := b.UploadFiles(ctx, []struct {
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
	b, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	write := tools.NewWriteFileTool(b).Tool.(einotool.InvokableTool)
	edit := tools.NewEditFileTool(b).Tool.(einotool.InvokableTool)

	for _, args := range []string{
		`{"path":"data.txt"}`,
		`{"path":"data.txt","content":null}`,
	} {
		_, err = write.InvokableRun(ctx, args)
		if err == nil {
			t.Fatalf("accepted write arguments: %s", args)
		}
	}
	if provider.files["/remote/data.txt"] != "twice twice" || provider.writes != 0 {
		t.Fatalf("invalid write changed provider: files=%v writes=%d", provider.files, provider.writes)
	}

	for _, args := range []string{
		`{"path":"data.txt","old":"twice"}`,
		`{"path":"data.txt","old":"twice","new":null}`,
	} {
		_, err = edit.InvokableRun(ctx, args)
		if err == nil {
			t.Fatalf("accepted edit arguments: %s", args)
		}
	}
	if provider.files["/remote/data.txt"] != "twice twice" || provider.writes != 0 {
		t.Fatalf("invalid edit changed provider: files=%v writes=%d", provider.files, provider.writes)
	}

	_, err = write.InvokableRun(ctx, `{"path":"empty.txt","content":""}`)
	if err != nil {
		t.Fatal(err)
	}
	empty, ok := provider.files["/remote/empty.txt"]
	if err != nil || !ok || empty != "" {
		t.Fatalf("explicit empty write failed: %q %v", empty, err)
	}
	_, err = edit.InvokableRun(ctx, `{"path":"remove.txt","old":"remove","new":""}`)
	if err != nil || provider.files["/remote/remove.txt"] != " me" {
		t.Fatalf("explicit empty edit failed: %q %v", provider.files["/remote/remove.txt"], err)
	}

	_, err = edit.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"once","replace_all":false}`)
	if err == nil {
		t.Fatal("replace_all=false accepted ambiguous edit")
	}
	if provider.files["/remote/data.txt"] != "twice twice" {
		t.Fatal("ambiguous edit changed provider")
	}
	_, err = edit.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"once","replace_all":true}`)
	if err != nil || provider.files["/remote/data.txt"] != "once once" {
		t.Fatalf("replace_all edit failed: %q %v", provider.files["/remote/data.txt"], err)
	}
}

func TestDockerFilesystemFailureDoesNotWriteOrUseHost(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{"/remote/a": "repeat repeat"}}
	b, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"", "missing", "repeat"} {
		_, err := b.Edit(ctx, "a", old, "new", false)
		if err == nil {
			t.Fatalf("accepted %q", old)
		}
	}
	if provider.writes != 0 {
		t.Fatal("failed edit mutated provider")
	}
	sentinel := errors.New("provider unavailable")
	provider.err = sentinel
	_, readErr := b.Read(ctx, "a", nil, nil)
	if !errors.Is(readErr, sentinel) {
		t.Fatal(readErr)
	}
	_, writeErr2 := b.Write(ctx, "a", "new")
	if !errors.Is(writeErr2, sentinel) {
		t.Fatal(writeErr2)
	}
	changeDirErr := b.ChangeDir(ctx, "bad")
	if !errors.Is(changeDirErr, sentinel) {
		t.Fatal(changeDirErr)
	}
	provider.err = nil
	_, bWriteErr := b.Write(ctx, "after", "x")
	if bWriteErr != nil || provider.lastPath != "/remote/after" {
		t.Fatalf("failed chdir changed cwd: %s %v", provider.lastPath, bWriteErr)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, writeErr := b.Write(canceled, "a", "x")
	if !errors.Is(writeErr, context.Canceled) {
		t.Fatal(writeErr)
	}
	if provider.writes != 1 {
		t.Fatal("canceled call reached provider")
	}
	_, backendNewDockerFilesystemErr := filesystempkg.NewDockerFilesystem(nil, "/remote", "thread", nil)
	if backendNewDockerFilesystemErr == nil {
		t.Fatal("nil provider accepted")
	}
	_, newDockerFilesystemErr := filesystempkg.NewDockerFilesystem(provider, "relative", "thread", nil)
	if newDockerFilesystemErr == nil {
		t.Fatal("relative root accepted")
	}
}

func TestDockerFilesystemRejectsSymlinkOutsideWorkspace(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{}, resolved: map[string]string{"/remote/escape/file": "/outside/file"}}
	files, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close(ctx)
	_, writeErr := files.Write(ctx, "escape/file", "outside")
	if !errors.Is(writeErr, filesystempkg.ErrInvalidPath) {
		t.Fatalf("write through escaping symlink = %v", writeErr)
	}
	_, readErr := files.Read(ctx, "escape/file", nil, nil)
	if !errors.Is(readErr, filesystempkg.ErrInvalidPath) {
		t.Fatalf("read through escaping symlink = %v", readErr)
	}
	if provider.writes != 0 || provider.lastPath != "" {
		t.Fatal("escaping path reached the sandbox file API")
	}
}

func TestDockerFilesystemAllowsSymlinkInsideWorkspace(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{"/remote/alias": "inside"}, resolved: map[string]string{"/remote/alias": "/remote/target"}}
	files, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close(ctx)
	got, err := files.Read(ctx, "alias", nil, nil)
	if err != nil || got != "inside" {
		t.Fatalf("in-root symlink read = %q, %v", got, err)
	}
}

type cancelAfterReadSandbox struct {
	*fileSandbox
	cancel context.CancelFunc
}

func (s *cancelAfterReadSandbox) ReadFile(ctx context.Context, p string) (string, error) {
	content, err := s.fileSandbox.ReadFile(ctx, p)
	s.cancel()
	return content, err
}

func TestDockerEditDoesNotWriteAfterReadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &cancelAfterReadSandbox{fileSandbox: &fileSandbox{files: map[string]string{"/remote/a": "original"}}, cancel: cancel}
	files, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = files.Edit(ctx, "a", "original", "changed", false)
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

func (s *cancelingResolverSandbox) ResolveContainerPath(_ context.Context, p string) (string, error) {
	s.cancel()
	return "", errors.New("resolver canceled")
}

func TestDockerFilesystemDeletePreservesResolverCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &cancelingResolverSandbox{
		fileSandbox: &fileSandbox{files: map[string]string{}},
		cancel:      cancel,
	}
	files, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = files.Delete(ctx, "a")
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
	files, err := filesystempkg.NewDockerFilesystem(provider, "/remote", "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, deleteErr := files.Delete(ctx, "a")
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

	ready := time.NewTicker(10 * time.Millisecond)
	defer ready.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
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
		case <-ready.C:
		case <-deadline.C:
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
	var released atomic.Int32
	filesystem, err := filesystempkg.NewDockerFilesystem(&fileSandbox{}, "/remote", "thread", func() { released.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	var callers sync.WaitGroup
	for i := 0; i < 8; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			closeErr := filesystem.Close(context.Background())
			if closeErr != nil {
				t.Error(closeErr)
			}
		}()
	}
	callers.Wait()
	if released.Load() != 1 {
		t.Fatalf("container releases=%d, want 1", released.Load())
	}
}

func TestDockerFilesystemConstructionFailureReleasesContainer(t *testing.T) {
	for _, root := range []string{"relative", "/remote"} {
		t.Run(root, func(t *testing.T) {
			released := 0
			filesystem, err := filesystempkg.NewDockerFilesystem(&fileSandbox{}, root, "", func() { released++ })
			if err == nil || filesystem != nil || released != 1 {
				t.Fatalf("failed construction must release ownership: filesystem=%v error=%v releases=%d", filesystem, err, released)
			}
		})
	}
}
