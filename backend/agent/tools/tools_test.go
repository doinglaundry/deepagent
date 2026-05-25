package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"

	"eino-cli/backend/config"
	"eino-cli/backend/consts"
	runtimecontext "eino-cli/backend/runtime/context"
	"eino-cli/backend/sandbox"
)

// invoke is a small helper: every tool here is built via utils.InferTool
// which returns a tool.InvokableTool; we just need to JSON-marshal a Go map
// then call InvokableRun.
func invoke(t *testing.T, bt tool.BaseTool, args string) string {
	t.Helper()
	it, ok := bt.(tool.InvokableTool)
	if !ok {
		t.Fatalf("tool is not InvokableTool")
	}
	out, err := it.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatalf("tool invoke failed: %v", err)
	}
	return out
}

// invokeExpectErr returns the error message; used for negative cases.
func invokeExpectErr(t *testing.T, bt tool.BaseTool, args string) error {
	t.Helper()
	it := bt.(tool.InvokableTool)
	_, err := it.InvokableRun(context.Background(), args)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	return err
}

func invokeWithContext(t *testing.T, ctx context.Context, bt tool.BaseTool, args string) string {
	t.Helper()
	it, ok := bt.(tool.InvokableTool)
	if !ok {
		t.Fatalf("tool is not InvokableTool")
	}
	out, err := it.InvokableRun(ctx, args)
	if err != nil {
		t.Fatalf("tool invoke failed: %v", err)
	}
	return out
}

func setToolRoot(t *testing.T, root string) {
	t.Helper()
	cleanup := config.SetRootDirForTest(root)
	t.Cleanup(cleanup)
}

type fakeSandboxManager struct {
	box           sandbox.Sandbox
	isolatedExec  bool
	acquireCalled *bool
	getCalled     *bool
}

func (m fakeSandboxManager) SessionID() string { return "session-a" }

func (m fakeSandboxManager) GetSandboxIdBySessionId(context.Context, string) (string, error) {
	if m.acquireCalled != nil {
		*m.acquireCalled = true
	}
	return "sandbox", nil
}
func (m fakeSandboxManager) Get(context.Context, string) (sandbox.Sandbox, error) {
	if m.getCalled != nil {
		*m.getCalled = true
	}
	return m.box, nil
}
func (m fakeSandboxManager) Release(context.Context, string) error { return nil }
func (m fakeSandboxManager) Reset()                                {}
func (m fakeSandboxManager) UsesSessionDataMounts() bool           { return true }
func (m fakeSandboxManager) AllowsIsolatedExec() bool              { return m.isolatedExec }

type fakeSandbox struct {
	command string
}

func (s *fakeSandbox) ID() string { return "sandbox" }

func (s *fakeSandbox) SessionID() string { return "session-a" }
func (s *fakeSandbox) ExecuteCommand(_ context.Context, command string) (string, error) {
	s.command = command
	return "sandbox: " + command, nil
}
func (s *fakeSandbox) ReadFile(context.Context, string) (string, error) { return "", nil }
func (s *fakeSandbox) WriteFile(context.Context, string, string, bool) error {
	return nil
}
func (s *fakeSandbox) UpdateFile(context.Context, string, []byte) error { return nil }
func (s *fakeSandbox) ListDir(context.Context, string, int) ([]string, error) {
	return nil, nil
}
func (s *fakeSandbox) Glob(context.Context, string, string, sandbox.GlobOpts) ([]string, bool, error) {
	return nil, false, nil
}
func (s *fakeSandbox) Grep(context.Context, string, string, sandbox.GrepOpts) ([]sandbox.GrepMatch, bool, error) {
	return nil, false, nil
}

func TestLs(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bt, err := GetLsTool(nil)
	if err != nil {
		t.Fatal(err)
	}
	got := invoke(t, bt, `{"path":"."}`)
	// os.ReadDir sorts by name → "a.txt\nb.txt".
	if got != "a.txt\nb.txt" {
		t.Fatalf("ls output mismatch:\ngot:  %q\nwant: %q", got, "a.txt\nb.txt")
	}

	emptyDir := t.TempDir()
	setToolRoot(t, emptyDir)
	bt2, _ := GetLsTool(nil)
	if got := invoke(t, bt2, `{"path":"."}`); got != consts.NoFilesFound {
		t.Fatalf("empty dir: got %q want %q", got, consts.NoFilesFound)
	}
}

func TestReadFile(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	body := "line1\nline2\nline3"
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetReadFileTool(nil)

	got := invoke(t, bt, `{"file_path":"f.txt"}`)
	want := "     1\tline1\n     2\tline2\n     3\tline3"
	if got != want {
		t.Fatalf("read_file default output:\ngot:  %q\nwant: %q", got, want)
	}

	// offset + limit slice in the middle (1-based offset).
	got = invoke(t, bt, `{"file_path":"f.txt","offset":2,"limit":1}`)
	want = "     2\tline2"
	if got != want {
		t.Fatalf("read_file offset/limit:\ngot:  %q\nwant: %q", got, want)
	}

	got = invoke(t, bt, `{"file_path":"missing.txt"}`)
	want = "File not found: " + filepath.Join(root, "missing.txt")
	if got != want {
		t.Fatalf("read_file missing file:\ngot:  %q\nwant: %q", got, want)
	}

	err := invokeExpectErr(t, bt, `{"file_path":".."}`)
	if !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("read_file escape error: %v", err)
	}
}

func TestWriteFile(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	bt, _ := GetWriteFileTool(nil)

	got := invoke(t, bt, `{"file_path":"sub/new.txt","content":"hello"}`)
	if got != "Updated file sub/new.txt" {
		t.Fatalf("write_file ack:\ngot:  %q\nwant: %q", got, "Updated file sub/new.txt")
	}
	data, err := os.ReadFile(filepath.Join(root, "sub", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("write_file content: got %q want %q", string(data), "hello")
	}
}

func TestEditFile(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	p := filepath.Join(root, "f.txt")
	if err := os.WriteFile(p, []byte("foo bar foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetEditFileTool(nil)

	// 2 occurrences without replace_all → ambiguity error.
	err := invokeExpectErr(t, bt, `{"file_path":"f.txt","old_string":"foo","new_string":"baz"}`)
	if !strings.Contains(err.Error(), "appears 2 times") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}

	// replace_all=true succeeds.
	got := invoke(t, bt, `{"file_path":"f.txt","old_string":"foo","new_string":"baz","replace_all":true}`)
	if got != "Successfully replaced the string in 'f.txt'" {
		t.Fatalf("edit_file ack:\ngot:  %q", got)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "baz bar baz" {
		t.Fatalf("edit_file content: got %q", string(data))
	}
}

func TestGlob(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bt, _ := GetGlobTool(nil)
	got := invoke(t, bt, `{"pattern":"*.go","path":""}`)
	// Glob returns absolute paths so follow-up tool calls can reuse them.
	want := filepath.Join(root, "a.go") + "\n" + filepath.Join(root, "b.go")
	if got != want {
		t.Fatalf("glob:\ngot:  %q\nwant: %q", got, want)
	}

	if got := invoke(t, bt, `{"pattern":"*.rs","path":""}`); got != consts.NoFilesFound {
		t.Fatalf("no match: got %q want %q", got, consts.NoFilesFound)
	}
}

func TestGlobDefaultsToRecursiveSearch(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	if err := os.MkdirAll(filepath.Join(root, "yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "yaml", "CHANGELOG.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetGlobTool(nil)

	got := invoke(t, bt, `{"pattern":"CHANGELOG.md","path":""}`)
	want := filepath.Join(root, "yaml", "CHANGELOG.md")
	if got != want {
		t.Fatalf("recursive glob:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestDeleteFile(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	path := filepath.Join(root, "old.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetDeleteFileTool(nil)

	got := invoke(t, bt, `{"file_path":"old.txt"}`)
	if got != "Deleted file "+path {
		t.Fatalf("delete_file:\ngot:  %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
	if got := invoke(t, bt, `{"file_path":"missing.txt"}`); !strings.Contains(got, "File does not exist") {
		t.Fatalf("delete missing: %q", got)
	}
}

func TestApplyPatchAddAndUpdate(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	bt, _ := GetApplyPatchTool(nil)
	addPatch := `*** Begin Patch
*** Add File: new.txt
+hello
+world
*** End Patch`

	got := invoke(t, bt, `{"patch":`+quoteJSON(t, addPatch)+`}`)
	if got != "Applied patch to 1 file(s)" {
		t.Fatalf("apply add: %q", got)
	}
	data, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\nworld" {
		t.Fatalf("added content: %q", string(data))
	}

	updatePatch := `*** Begin Patch
*** Update File: new.txt
@@
 hello
-world
+cursor
*** End Patch`
	got = invoke(t, bt, `{"patch":`+quoteJSON(t, updatePatch)+`}`)
	if got != "Applied patch to 1 file(s)" {
		t.Fatalf("apply update: %q", got)
	}
	data, _ = os.ReadFile(filepath.Join(root, "new.txt"))
	if string(data) != "hello\ncursor" {
		t.Fatalf("updated content: %q", string(data))
	}
}

func TestGrepFilesWithMatches(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetGrepTool(nil)

	// Default output_mode is files_with_matches; "Found N file" header.
	got := invoke(t, bt, `{"pattern":"hello"}`)
	want := "Found 1 file\na.txt"
	if got != want {
		t.Fatalf("grep files:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestGrepContent(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetGrepTool(nil)

	got := invoke(t, bt, `{"pattern":"hello","output_mode":"content"}`)
	want := "a.txt:1:hello"
	if got != want {
		t.Fatalf("grep content:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestGrepFallsBackToLiteralPattern(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n\\Middleware\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetGrepTool(nil)

	got := invoke(t, bt, `{"pattern":"\\Middleware","output_mode":"content"}`)
	want := "a.txt:2:\\Middleware"
	if got != want {
		t.Fatalf("grep literal fallback:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestGrepCount(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetGrepTool(nil)

	got := invoke(t, bt, `{"pattern":"x","output_mode":"count"}`)
	if !strings.Contains(got, "a.txt:2") {
		t.Fatalf("grep count missing per-file entry: %q", got)
	}
	if !strings.Contains(got, "Found 2 total occurrences across 1 file.") {
		t.Fatalf("grep count summary mismatch: %q", got)
	}
}

func TestRgContent(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetRgTool(nil)

	got := invoke(t, bt, `{"pattern":"hello","output_mode":"content"}`)
	if !strings.Contains(got, "a.txt:1:hello") {
		t.Fatalf("rg content: %q", got)
	}
	if got := invoke(t, bt, `{"pattern":"missing"}`); got != consts.NoMatchesFound {
		t.Fatalf("rg no match: %q", got)
	}
}

func TestExecute(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	bt, _ := GetExecuteTool(nil)

	got := invoke(t, bt, `{"command":"echo hi"}`)
	if !strings.HasPrefix(got, "hi") {
		t.Fatalf("execute echo: %q", got)
	}

	got = invoke(t, bt, `{"command":"exit 3"}`)
	if !strings.Contains(got, "[Command failed with exit code 3]") {
		t.Fatalf("execute non-zero exit: %q", got)
	}

	got = invoke(t, bt, `{"command":"true"}`)
	if got != "[Command executed successfully with no output]" {
		t.Fatalf("execute empty success: %q", got)
	}
}

func TestExecuteDeniedWhenRollbackProtected(t *testing.T) {
	setToolRoot(t, t.TempDir())
	bt, _ := GetExecuteTool(nil)
	ctx := runtimecontext.WithRollbackProtected(context.Background(), true)
	got := invokeWithContext(t, ctx, bt, `{"command":"echo hi"}`)
	if !strings.Contains(got, "disabled in rollback-protected runs") {
		t.Fatalf("execute rollback denial: %q", got)
	}
}

func TestExecuteDeniedInNonIsolatedSandboxWhenRollbackProtected(t *testing.T) {
	box := &fakeSandbox{}
	getCalled := false
	bt, _ := GetExecuteTool(fakeSandboxManager{box: box, getCalled: &getCalled})
	ctx := runtimecontext.WithSandboxID(context.Background(), "sandbox")
	ctx = runtimecontext.WithRollbackProtected(ctx, true)

	got := invokeWithContext(t, ctx, bt, `{"command":"echo hi"}`)
	if !strings.Contains(got, "disabled in rollback-protected runs") {
		t.Fatalf("execute rollback denial: %q", got)
	}
	if getCalled {
		t.Fatal("non-isolated sandbox should not be fetched before rollback denial")
	}
	if box.command != "" {
		t.Fatalf("non-isolated sandbox should not execute command, got %q", box.command)
	}
}

func TestExecuteAllowedInIsolatedSandboxWhenRollbackProtected(t *testing.T) {
	box := &fakeSandbox{}
	bt, _ := GetExecuteTool(fakeSandboxManager{box: box, isolatedExec: true})
	ctx := runtimecontext.WithSandboxID(context.Background(), "sandbox")
	ctx = runtimecontext.WithRollbackProtected(ctx, true)

	got := invokeWithContext(t, ctx, bt, `{"command":"echo hi"}`)
	if got != "sandbox: echo hi" {
		t.Fatalf("execute should use aio sandbox, got %q", got)
	}
	if box.command != "echo hi" {
		t.Fatalf("sandbox command = %q", box.command)
	}
}

func TestExecuteAllowedInIsolatedSandboxWithoutStampedIDWhenRollbackProtected(t *testing.T) {
	box := &fakeSandbox{}
	acquireCalled := false
	bt, _ := GetExecuteTool(fakeSandboxManager{box: box, isolatedExec: true, acquireCalled: &acquireCalled})
	ctx := runtimecontext.WithSessionID(context.Background(), "session-a")
	ctx = runtimecontext.WithRollbackProtected(ctx, true)

	got := invokeWithContext(t, ctx, bt, `{"command":"echo hi"}`)
	if got != "sandbox: echo hi" {
		t.Fatalf("execute should acquire aio sandbox, got %q", got)
	}
	if !acquireCalled {
		t.Fatal("execute should acquire sandbox when ctx has no sandbox id")
	}
	if box.command != "echo hi" {
		t.Fatalf("sandbox command = %q", box.command)
	}
}

func TestShellAndAwaitShell(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	bt, _ := GetShellTool(nil, nil)

	got := invoke(t, bt, `{"command":"echo hi","timeout_ms":1000}`)
	if !strings.HasPrefix(got, "hi") {
		t.Fatalf("shell echo: %q", got)
	}

	got = invoke(t, bt, `{"command":"printf ready; sleep 0.2","timeout_ms":1}`)
	if !strings.Contains(got, "task_id=") {
		t.Fatalf("shell background: %q", got)
	}
	taskID := strings.TrimSpace(strings.TrimPrefix(got, "Command is still running in background. task_id="))
	await, _ := GetAwaitShellTool()
	got = invoke(t, await, `{"task_id":"`+taskID+`","pattern":"ready","timeout_ms":1000}`)
	if !strings.Contains(got, "ready") {
		t.Fatalf("await_shell: %q", got)
	}
}

func TestShellDeniedWhenRollbackProtected(t *testing.T) {
	setToolRoot(t, t.TempDir())
	bt, _ := GetShellTool(nil, nil)
	ctx := runtimecontext.WithRollbackProtected(context.Background(), true)
	got := invokeWithContext(t, ctx, bt, `{"command":"echo hi","timeout_ms":1000}`)
	if !strings.Contains(got, "disabled in rollback-protected runs") {
		t.Fatalf("shell rollback denial: %q", got)
	}
}

func TestShellDeniedInNonIsolatedSandboxWhenRollbackProtected(t *testing.T) {
	box := &fakeSandbox{}
	getCalled := false
	bt, _ := GetShellTool(fakeSandboxManager{box: box, getCalled: &getCalled}, nil)
	ctx := runtimecontext.WithSandboxID(context.Background(), "sandbox")
	ctx = runtimecontext.WithRollbackProtected(ctx, true)

	got := invokeWithContext(t, ctx, bt, `{"command":"echo hi","timeout_ms":1000}`)
	if !strings.Contains(got, "disabled in rollback-protected runs") {
		t.Fatalf("shell rollback denial: %q", got)
	}
	if getCalled {
		t.Fatal("non-isolated sandbox should not be fetched before rollback denial")
	}
	if box.command != "" {
		t.Fatalf("non-isolated sandbox should not execute command, got %q", box.command)
	}
}

func TestShellAllowedInIsolatedSandboxWhenRollbackProtected(t *testing.T) {
	box := &fakeSandbox{}
	bt, _ := GetShellTool(fakeSandboxManager{box: box, isolatedExec: true}, nil)
	ctx := runtimecontext.WithSandboxID(context.Background(), "sandbox")
	ctx = runtimecontext.WithRollbackProtected(ctx, true)

	got := invokeWithContext(t, ctx, bt, `{"command":"echo hi","timeout_ms":1000}`)
	if got != "sandbox: echo hi" {
		t.Fatalf("shell should use aio sandbox, got %q", got)
	}
	if box.command != "echo hi" {
		t.Fatalf("sandbox command = %q", box.command)
	}
}

func TestShellAllowedInIsolatedSandboxWithoutStampedIDWhenRollbackProtected(t *testing.T) {
	box := &fakeSandbox{}
	acquireCalled := false
	bt, _ := GetShellTool(fakeSandboxManager{box: box, isolatedExec: true, acquireCalled: &acquireCalled}, nil)
	ctx := runtimecontext.WithSessionID(context.Background(), "session-a")
	ctx = runtimecontext.WithRollbackProtected(ctx, true)

	got := invokeWithContext(t, ctx, bt, `{"command":"echo hi","timeout_ms":1000}`)
	if got != "sandbox: echo hi" {
		t.Fatalf("shell should acquire aio sandbox, got %q", got)
	}
	if !acquireCalled {
		t.Fatal("shell should acquire sandbox when ctx has no sandbox id")
	}
	if box.command != "echo hi" {
		t.Fatalf("sandbox command = %q", box.command)
	}
}

func TestSemanticSearch(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "tool.go"), []byte("func buildToolCall() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetSemanticSearchTool(nil)

	got := invoke(t, bt, `{"query":"where is tool call built"}`)
	if !strings.Contains(got, "tool.go:1") {
		t.Fatalf("semantic_search: %q", got)
	}
}

func TestReadLintsTargets(t *testing.T) {
	root := t.TempDir()
	setToolRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, unsupported, err := getLintTargets(root, []string{"README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || len(unsupported) != 1 {
		t.Fatalf("lint targets: packages=%v unsupported=%v", got, unsupported)
	}
}

func TestAskClarificationTool(t *testing.T) {
	bt, err := GetAskClarificationTool()
	if err != nil {
		t.Fatal(err)
	}
	info, err := bt.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != consts.AskClarificationToolName {
		t.Fatalf("name: got %q want %q", info.Name, consts.AskClarificationToolName)
	}
	schema, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("ToJSONSchema: %v", err)
	}
	for _, name := range []string{"question", "clarification_type", "context", "options"} {
		if _, ok := schema.Properties.Get(name); !ok {
			t.Fatalf("schema missing %q", name)
		}
	}
	for _, name := range []string{"question", "clarification_type"} {
		if !containsString(schema.Required, name) {
			t.Fatalf("schema required missing %q: %v", name, schema.Required)
		}
	}

	got := invoke(t, bt, `{"question":"Which environment?","clarification_type":"approach_choice","options":["dev","prod"]}`)
	if got != "Clarification request processed by middleware" {
		t.Fatalf("clarification fallback output: %q", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func quoteJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuildBuiltinToolsCount(t *testing.T) {
	cleanup := config.SetRootDirForTest(t.TempDir())
	defer cleanup()
	got := BuildBuiltinTools(&config.Config{}, nil)
	if len(got) != 15 {
		t.Fatalf("BuildBuiltinTools: got %d tools, want 15", len(got))
	}
	// Names should match eino's expected wire identifiers exactly.
	want := []string{
		"ask_clarification", "ls", "read_file", "write_file", "edit_file", "glob", "grep", "execute",
		"apply_patch", "delete_file", "rg", "semantic_search", "read_lints", "shell", "await_shell",
	}
	for i, bt := range got {
		info, err := bt.Info(context.Background())
		if err != nil {
			t.Fatalf("tool[%d].Info: %v", i, err)
		}
		if info.Name != want[i] {
			t.Fatalf("tool[%d] name: got %q want %q", i, info.Name, want[i])
		}
	}
}

// web_search is gated by yaml; flipping the flag must change roster size
// AND tail name — drift between flag and prompt tool list is the bug.
func TestBuildBuiltinToolsWithWebSearch(t *testing.T) {
	cfg := &config.Config{
		WebSearch: config.WebSearch{Enabled: true, APIKey: "stub", MaxResults: 5},
	}
	cleanup := config.SetRootDirForTest(t.TempDir())
	defer cleanup()
	got := BuildBuiltinTools(cfg, nil)
	if len(got) != 16 {
		t.Fatalf("BuildBuiltinTools(enabled): got %d tools, want 16", len(got))
	}
	last, err := got[len(got)-1].Info(context.Background())
	if err != nil {
		t.Fatalf("tool.Info: %v", err)
	}
	if last.Name != "web_search" {
		t.Fatalf("last tool name: got %q want web_search", last.Name)
	}
}
