package deepagents

import (
	"context"
	"eino-cli/deepagent/core/backend"
	canonical "eino-cli/deepagent/core/middleware"
	deeptools "eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type testSkillLoader struct{}

func (l *testSkillLoader) ListSkills(ctx context.Context) ([]*backend.SkillMetadata, error) {
	return []*backend.SkillMetadata{{
		Name:        "code_search",
		Description: "search codebase",
		Path:        "/skills/code_search/SKILL.md",
	}}, nil
}

type fakeToolCounter struct{ total int }

func (t *fakeToolCounter) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "counter", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"delta": {Type: schema.Integer, Required: true},
	})}, nil
}

func (t *fakeToolCounter) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	t.total++
	return "ok", nil
}

func findSkillMiddleware(t *testing.T, middlewares []canonical.Middleware) *canonical.SkillMiddleware {
	t.Helper()

	for _, mw := range middlewares {
		if skillMiddleware, ok := mw.(*canonical.SkillMiddleware); ok {
			return skillMiddleware
		}
	}
	t.Fatalf("expected skill middleware to be present")
	return nil
}

func newTestBackend(t *testing.T) *backend.LocalFilesystem {
	t.Helper()
	return mustLocalFilesystem(t, &backend.LocalFilesystemConfig{
		RootDir:     t.TempDir(),
		VirtualMode: true,
	})
}

type testApplyPatchBackend struct {
	*backend.LocalFilesystem
}

func newTestApplyPatchBackend(t *testing.T) *testApplyPatchBackend {
	t.Helper()
	return &testApplyPatchBackend{LocalFilesystem: newTestBackend(t)}
}

func (b *testApplyPatchBackend) SupportsApplyPatch() bool {
	return true
}

func (b *testApplyPatchBackend) ApplyPatch(context.Context, string) (string, error) {
	return "patched", nil
}

func collectToolNames(t *testing.T, ctx context.Context, toolList []tool.BaseTool) []string {
	t.Helper()

	names := make([]string, 0, len(toolList))
	for _, tl := range toolList {
		info, err := tl.Info(ctx)
		if err != nil {
			t.Fatalf("tool.Info() error = %v", err)
		}
		names = append(names, info.Name)
	}
	return names
}

func TestSelectBackendProvidesCommandExecution(t *testing.T) {
	ctx := context.Background()
	m := &publicModel{}
	filesystem, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(ctx)
	a, err := New(ctx, WithModel(m), WithFilesystem(filesystem))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	found := false
	for _, info := range m.infos {
		if info.Name == "execute" {
			found = true
		}
	}
	if !found {
		t.Fatal("local filesystem does not expose command execution")
	}
}

func TestFeatureConfigPresenceControlsEnablement(t *testing.T) {
	configured := buildCreateConfig()
	if configured.FilesystemConfig != nil || configured.WebConfig != nil {
		t.Fatalf("zero config unexpectedly enables features: %+v", configured)
	}

	configured = buildCreateConfig(WithFilesystemConfig(nil), WithWeb())
	if configured.FilesystemConfig == nil {
		t.Fatal("WithFilesystemConfig() did not create filesystem config")
	}
	if configured.WebConfig == nil || !configured.WebConfig.EnableWebSearch || !configured.WebConfig.EnableFetchURL {
		t.Fatalf("WithWeb() config = %+v", configured.WebConfig)
	}
}

func TestNew_RequiresModel(t *testing.T) {
	_, err := New(context.Background())
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("expected model required error, got %v", err)
	}
}

type explicitReadOnlyTool struct{ fakeToolCounter }

func (*explicitReadOnlyTool) ReadOnly() bool { return true }
func (*explicitReadOnlyTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "readonly_counter"}, nil
}

func TestCollectAllTools_ReadOnlyBoundaryRejectsUnknownCapabilities(t *testing.T) {
	ctx := context.Background()
	config := &Config{
		ToolDescriptors:   []deeptools.Descriptor{{Tool: &fakeToolCounter{}}, {Tool: &explicitReadOnlyTool{}, ReadOnly: true}},
		ReadOnlyToolsOnly: true,
	}
	m := &publicModel{}
	config.Model = m
	a, err := New(ctx, WithConfig(config))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	if len(m.infos) != 1 || m.infos[0].Name != "readonly_counter" {
		t.Fatalf("read-only tools = %v", m.infos)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestWithConfigCopiesInput(t *testing.T) {
	source := &Config{FilesystemConfig: &FilesystemConfig{ReadOnly: true}, Prompts: []*schema.Message{schema.SystemMessage("original")}}
	configured := buildCreateConfig(WithConfig(source))
	configured.Prompts[0] = schema.SystemMessage("changed")
	configured.FilesystemConfig.ReadOnly = false
	if source.Prompts[0].Content != "original" || !source.FilesystemConfig.ReadOnly {
		t.Fatal("WithConfig mutated the source")
	}
}

func TestToolMaskExcludesToolFromModel(t *testing.T) {
	m := &publicModel{}
	a, err := New(context.Background(), WithConfig(&Config{
		Model:           m,
		ToolDescriptors: []deeptools.Descriptor{{Tool: &fakeToolCounter{}}},
		ToolMask:        func(_ context.Context, info *schema.ToolInfo) bool { return info.Name != "counter" },
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	if len(m.infos) != 0 {
		t.Fatalf("masked tool exposed: %v", m.infos)
	}
}

func TestToolPolicyDeniesWithoutRunningTool(t *testing.T) {
	counter := &fakeToolCounter{}
	m := &publicModel{call: true}
	a, err := New(context.Background(), WithConfig(&Config{
		Model:           m,
		ToolDescriptors: []deeptools.Descriptor{{Tool: counter}},
		Policy: deeptools.PolicyFunc(func(context.Context, types.ToolCall, deeptools.Descriptor) (deeptools.Decision, error) {
			return deeptools.Decision{Action: deeptools.Deny, Reason: "blocked"}, nil
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	_, err = a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if counter.total != 0 || len(m.inputs) != 2 {
		t.Fatalf("executions=%d models=%d", counter.total, len(m.inputs))
	}
	last := m.inputs[1][len(m.inputs[1])-1]
	if last.Role != schema.Tool || last.Content != "blocked" {
		t.Fatalf("denial missing: %+v", last)
	}
}
