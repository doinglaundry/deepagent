package tools

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"

	"eino-cli/deepagent/config"
	"eino-cli/deepagent/constant"
	"eino-cli/deepagent/sandbox"
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

func TestBuildBuiltinToolsCount(t *testing.T) {
	cleanup := config.SetRootDirForTest(t.TempDir())
	defer cleanup()
	got := BuildBuiltinTools(&config.Config{}, nil)
	if len(got) != 1 {
		t.Fatalf("BuildBuiltinTools: got %d tools, want 1", len(got))
	}
	// Names should match eino's expected wire identifiers exactly.
	want := []string{
		"ask_clarification",
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
	if len(got) != 2 {
		t.Fatalf("BuildBuiltinTools(enabled): got %d tools, want 2", len(got))
	}
	last, err := got[len(got)-1].Info(context.Background())
	if err != nil {
		t.Fatalf("tool.Info: %v", err)
	}
	if last.Name != "web_search" {
		t.Fatalf("last tool name: got %q want web_search", last.Name)
	}
}
