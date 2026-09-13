package deepagents

import (
	"context"
	"eino-cli/deepagent/core/backends"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/middleware/contextmanager"
	skillmw "eino-cli/deepagent/core/middleware/skill"
	deeptools "eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
)

type testSubAgentContextInjector struct{}

func (i *testSubAgentContextInjector) LoadContext(ctx context.Context, agentName string) ([]*schema.Message, error) {
	return []*schema.Message{schema.UserMessage("forked-context")}, nil
}

type testBuilderMiddleware struct {
	middleware.BaseMiddleware
}

func (m *testBuilderMiddleware) Name() string {
	return "test_builder_middleware"
}

type testSkillLoader struct{}

func (l *testSkillLoader) ListSkills(ctx context.Context) ([]*skillmw.SkillMetadata, error) {
	return []*skillmw.SkillMetadata{{
		Name:        "code_search",
		Description: "search codebase",
		Path:        "/skills/code_search/SKILL.md",
	}}, nil
}

func findSkillMiddleware(t *testing.T, middlewares []middleware.Middleware) *skillmw.Middleware {
	t.Helper()

	for _, mw := range middlewares {
		if skillMiddleware, ok := mw.(*skillmw.Middleware); ok {
			return skillMiddleware
		}
	}
	t.Fatalf("expected skill middleware to be present")
	return nil
}

func newTestBackend(t *testing.T) *backends.FilesystemBackend {
	t.Helper()
	return backends.NewFilesystemBackend(&backends.FilesystemBackendConfig{
		RootDir:     t.TempDir(),
		VirtualMode: true,
	})
}

type testApplyPatchBackend struct {
	*backends.FilesystemBackend
}

func newTestApplyPatchBackend(t *testing.T) *testApplyPatchBackend {
	t.Helper()
	return &testApplyPatchBackend{FilesystemBackend: newTestBackend(t)}
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
		FilesystemConfig:    &FilesystemConfig{WorkDir: "/specific"},
		SubAgentsDirs:       []string{"/agents"},
		InterruptAfterNodes: []string{"model"},
		HITLConfig: &HITLConfig{
			ToolPolicyGates: map[string]deeptools.ToolPolicyGate{"execute": {}},
		},
	}

	configured := buildCreateConfig(WithConfig(source))
	configured.SubAgentsDirs[0] = "/changed"
	configured.InterruptAfterNodes[0] = "tools"
	delete(configured.HITLConfig.ToolPolicyGates, "execute")

	if source.SubAgentsDirs[0] != "/agents" {
		t.Fatalf("source SubAgentsDirs was mutated: %+v", source.SubAgentsDirs)
	}
	if source.InterruptAfterNodes[0] != "model" {
		t.Fatalf("source InterruptAfterNodes was mutated: %+v", source.InterruptAfterNodes)
	}
	if _, exists := source.HITLConfig.ToolPolicyGates["execute"]; !exists {
		t.Fatalf("source HITLConfig was mutated: %+v", source.HITLConfig)
	}
	if workDir := configured.filesystemWorkDir(); workDir != "/specific" {
		t.Fatalf("filesystem workdir = %q, want /specific", workDir)
	}
}

func TestWithWorkDirWritesFilesystemConfig(t *testing.T) {
	configured := buildCreateConfig(WithWorkDir("/workspace"))

	if configured.FilesystemConfig == nil {
		t.Fatal("WithWorkDir() did not enable filesystem configuration")
	}
	if workDir := configured.filesystemWorkDir(); workDir != "/workspace" {
		t.Fatalf("filesystem workdir = %q, want /workspace", workDir)
	}
}

func TestFeatureConfigPresenceControlsEnablement(t *testing.T) {
	configured := buildCreateConfig()
	if configured.FilesystemConfig != nil || configured.WebConfig != nil {
		t.Fatalf("zero config unexpectedly enables features: %+v", configured)
	}

	configured = buildCreateConfig(WithFilesystem(), WithWeb())
	if configured.FilesystemConfig == nil {
		t.Fatal("WithFilesystem() did not create filesystem config")
	}
	if configured.WebConfig == nil || !configured.WebConfig.EnableWebSearch || !configured.WebConfig.EnableFetchURL {
		t.Fatalf("WithWeb() config = %+v", configured.WebConfig)
	}
}

func TestNew_RequiresModel(t *testing.T) {
	_, err := New(context.Background(), WithContextManager(contextmanager.New()))
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
				"counter": deeptools.ApprovalGate(func(context.Context, *deeptools.ApprovalInfo) bool { return true }),
			},
		},
	}

	allTools, err := collectAllTools(ctx, middleware.NewMiddlewareChain(), config)
	if err != nil {
		t.Fatalf("collectAllTools() error = %v", err)
	}
	if len(allTools) != 0 {
		t.Fatalf("expected masked tool to be removed before HITL wrapping, got %v", collectToolNames(t, ctx, allTools))
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

	allTools, err := collectAllTools(ctx, middleware.NewMiddlewareChain(), config)
	if err != nil {
		t.Fatalf("collectAllTools() error = %v", err)
	}
	if len(allTools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(allTools))
	}
	invokable, ok := allTools[0].(tool.InvokableTool)
	if !ok {
		t.Fatalf("tool is not invokable")
	}
	got, err := invokable.InvokableRun(ctx, `{"delta":3}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
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
