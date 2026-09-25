package backend_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/tools"
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
	if target := s.resolved[p]; target != "" {
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
	b, err := backend.NewDockerFilesystem(provider, "/remote", "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(ctx)
	registered := map[string]einotool.InvokableTool{}
	items, err := tools.NewWorkspaceTools(b, tools.WorkspaceToolOptions{EnableCommands: true, EnablePatch: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		info, err := item.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		registered[info.Name] = item.(einotool.InvokableTool)
	}
	read, err := registered["read_file"].InvokableRun(ctx, `{"file_path":"a.txt","offset":2,"limit":1}`)
	if err != nil || !strings.Contains(read, "second") || strings.Contains(read, "first") || provider.lastPath != "/remote/a.txt" {
		t.Fatalf("read=%q path=%q err=%v", read, provider.lastPath, err)
	}
	_, err = registered["edit_file"].InvokableRun(ctx, `{"path":"a.txt","old_string":"second","new_string":"changed"}`)
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
	entries, err := b.LsInfo(ctx, "")
	if err != nil || len(entries) != 2 || !entries[0].IsDir || entries[1].IsDir {
		t.Fatalf("list=%v %v", entries, err)
	}
	matches, err := b.GlobInfo(ctx, "*.txt", "")
	if err != nil || len(matches) != 1 || matches[0].Path != "/remote/a.txt" {
		t.Fatalf("glob=%v %v", matches, err)
	}
	if err := b.ChangeDir(ctx, "dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write(ctx, "x", "nested"); err != nil || provider.files["/remote/dir/x"] != "nested" {
		t.Fatalf("cwd=%v files=%v", err, provider.files)
	}
	uploads, err := b.UploadFiles(ctx, []struct {
		Path    string
		Content []byte
	}{{Path: "binary", Content: []byte{0, 255}}})
	if err != nil || len(uploads) != 1 || provider.files["/remote/dir/binary"] != string([]byte{0, 255}) {
		t.Fatalf("upload=%v %v", uploads, err)
	}
}
func TestDockerFilesystemFailureDoesNotWriteOrUseHost(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{"/remote/a": "repeat repeat"}}
	b, err := backend.NewDockerFilesystem(provider, "/remote", "thread")
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"", "missing", "repeat"} {
		if _, err := b.Edit(ctx, "a", old, "new", false); err == nil {
			t.Fatalf("accepted %q", old)
		}
	}
	if provider.writes != 0 {
		t.Fatal("failed edit mutated provider")
	}
	sentinel := errors.New("provider unavailable")
	provider.err = sentinel
	if _, err := b.Read(ctx, "a", nil, nil); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := b.Write(ctx, "a", "new"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := b.ChangeDir(ctx, "bad"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	provider.err = nil
	if _, err := b.Write(ctx, "after", "x"); err != nil || provider.lastPath != "/remote/after" {
		t.Fatalf("failed chdir changed cwd: %s %v", provider.lastPath, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.Write(canceled, "a", "x"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if provider.writes != 1 {
		t.Fatal("canceled call reached provider")
	}
	if _, err := backend.NewDockerFilesystem(nil, "/remote", "thread"); err == nil {
		t.Fatal("nil provider accepted")
	}
	if _, err := backend.NewDockerFilesystem(provider, "relative", "thread"); err == nil {
		t.Fatal("relative root accepted")
	}
}

func TestDockerFilesystemRejectsSymlinkOutsideWorkspace(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{}, resolved: map[string]string{"/remote/escape/file": "/outside/file"}}
	files, err := backend.NewDockerFilesystem(provider, "/remote", "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close(ctx)
	if _, err := files.Write(ctx, "escape/file", "outside"); !errors.Is(err, backend.ErrInvalidPath) {
		t.Fatalf("write through escaping symlink = %v", err)
	}
	if _, err := files.Read(ctx, "escape/file", nil, nil); !errors.Is(err, backend.ErrInvalidPath) {
		t.Fatalf("read through escaping symlink = %v", err)
	}
	if provider.writes != 0 || provider.lastPath != "" {
		t.Fatal("escaping path reached the sandbox file API")
	}
}

func TestDockerFilesystemAllowsSymlinkInsideWorkspace(t *testing.T) {
	ctx := context.Background()
	provider := &fileSandbox{files: map[string]string{"/remote/alias": "inside"}, resolved: map[string]string{"/remote/alias": "/remote/target"}}
	files, err := backend.NewDockerFilesystem(provider, "/remote", "thread")
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
	files, err := backend.NewDockerFilesystem(provider, "/remote", "thread")
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
