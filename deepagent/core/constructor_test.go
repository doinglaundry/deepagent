package deepagents

import (
	"context"
	"eino-cli/deepagent/core/backend"
	canonical "eino-cli/deepagent/core/middleware"
	deeptools "eino-cli/deepagent/core/tools"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type testBuilderMiddleware struct {
	canonical.BaseMiddleware
}

func (m *testBuilderMiddleware) Name() string {
	return "test_builder_middleware"
}

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

func TestWithConfigCopiesInput(t *testing.T) {
	source := &Config{
		FilesystemConfig:    &FilesystemConfig{ReadOnly: true},
		InterruptAfterNodes: []string{"model"},
		HITLConfig: &HITLConfig{
			ToolPolicyGates: map[string]deeptools.ToolPolicyGate{"execute": {}},
		},
	}

	configured := buildCreateConfig(WithConfig(source))
	configured.InterruptAfterNodes[0] = "tools"
	delete(configured.HITLConfig.ToolPolicyGates, "execute")

	if source.InterruptAfterNodes[0] != "model" {
		t.Fatalf("source InterruptAfterNodes was mutated: %+v", source.InterruptAfterNodes)
	}
	if _, exists := source.HITLConfig.ToolPolicyGates["execute"]; !exists {
		t.Fatalf("source HITLConfig was mutated: %+v", source.HITLConfig)
	}
	if !configured.FilesystemConfig.ReadOnly {
		t.Fatal("filesystem options were not copied")
	}
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
	_, err := New(context.Background(), WithContextManager(&testBuilderMiddleware{}))
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("expected model required error, got %v", err)
	}
}

func TestCollectAllTools_ToolMaskRunsBeforeHITLWrapping(t *testing.T) {
	ctx := context.Background()
	config := &Config{
		Tools: []tool.BaseTool{&fakeToolCounter{}},
		ToolMask: func(_ context.Context, info *schema.ToolInfo) bool {
			return info.Name != "counter"
		},
		HITLConfig: &HITLConfig{
			ToolPolicyGates: map[string]deeptools.ToolPolicyGate{
				"counter": {Policy: func(context.Context, *deeptools.ApprovalInfo) (deeptools.ToolCallDecision, error) {
					return deeptools.ToolCallDecision{}, nil
				}},
			},
		},
	}

	m := &publicModel{}
	config.Model, config.DisableSubAgent = m, true
	a, err := New(ctx, WithConfig(config))
	if err != nil {
		t.Fatalf("collectAllTools() error = %v", err)
	}
	defer a.Close(ctx)
	if len(m.infos) != 0 {
		t.Fatalf("expected masked tool to be removed before HITL wrapping, got %v", m.infos)
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
		Tools:             []tool.BaseTool{&fakeToolCounter{}, &explicitReadOnlyTool{}},
		ReadOnlyToolsOnly: true,
	}
	m := &publicModel{}
	config.Model, config.DisableSubAgent = m, true
	a, err := New(ctx, WithConfig(config))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	if len(m.infos) != 1 || m.infos[0].Name != "readonly_counter" {
		t.Fatalf("read-only tools = %v", m.infos)
	}
}

func TestCollectAllTools_ToolPolicyGateDeniesWithoutRunningTool(t *testing.T) {
	ctx := context.Background()
	counter := &fakeToolCounter{}
	config := &Config{
		Tools: []tool.BaseTool{counter},
		HITLConfig: &HITLConfig{
			ToolPolicyGates: map[string]deeptools.ToolPolicyGate{
				"counter": {
					Policy: func(context.Context, *deeptools.ApprovalInfo) (deeptools.ToolCallDecision, error) {
						return deeptools.ToolCallDecision{Action: deeptools.ToolCallDeny, Reason: "blocked"}, nil
					},
					DenyFormatter: func(ctx context.Context, info *deeptools.ApprovalInfo, decision deeptools.ToolCallDecision) (string, error) {
						return `{"denied":true,"reason":"` + decision.Reason + `"}`, nil
					},
				},
			},
		},
	}

	m := &publicModel{call: true}
	config.Model, config.DisableSubAgent = m, true
	a, err := New(ctx, WithConfig(config))
	if err != nil {
		t.Fatalf("collectAllTools() error = %v", err)
	}
	defer a.Close(ctx)
	if len(m.infos) != 1 {
		t.Fatalf("tools len = %d, want 1", len(m.infos))
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if len(m.inputs) != 2 {
		t.Fatalf("model calls=%d", len(m.inputs))
	}
	got := m.inputs[1][len(m.inputs[1])-1].Content
	if got != `{"denied":true,"reason":"blocked"}` {
		t.Fatalf("output = %q", got)
	}
	if counter.total != 0 {
		t.Fatalf("counter total = %d, want 0", counter.total)
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
