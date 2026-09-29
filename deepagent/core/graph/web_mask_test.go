package graph

import (
	"context"
	"reflect"
	"testing"

	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type webMaskTestTool struct{ name string }
type graphWebMaskContextKey struct{}

func (t *webMaskTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}
func (*webMaskTestTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", nil
}

func TestWebMasksApplyLocallyAndGlobally(t *testing.T) {
	ctx := context.WithValue(context.Background(), graphWebMaskContextKey{}, "present")
	cases := []struct {
		name               string
		localRejectsSearch bool
		wantGlobalCalls    int
	}{
		{"local mask rejects search", true, 3},
		{"global mask rejects search", false, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			localCalls, globalCalls := 0, 0
			config := Config{
				Model: &sequenceModel{},
				WebConfig: &tools.WebConfig{
					EnableFetchURL: true, EnableWebSearch: true, SearchURL: "https://search.example/query",
					ToolMask: func(maskCtx context.Context, info *schema.ToolInfo) bool {
						localCalls++
						if maskCtx.Value(graphWebMaskContextKey{}) != "present" {
							t.Error("web mask lost run context")
						}
						return !tc.localRejectsSearch || info.Name == "read_url"
					},
				},
				ToolDescriptors: []tools.ToolDescriptor{{Tool: &webMaskTestTool{name: "keep"}}, {Tool: &webMaskTestTool{name: "discard"}}},
				ToolMask: func(maskCtx context.Context, info *schema.ToolInfo) bool {
					globalCalls++
					if maskCtx.Value(graphWebMaskContextKey{}) != "present" {
						t.Error("global mask lost run context")
					}
					return info.Name != "discard" && info.Name != "web_search"
				},
			}
			agent, err := New(ctx, WithConfig(&config))
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close(ctx)
			infos, err := agent.tools.ModelTools(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, info := range infos {
				names = append(names, info.Name)
			}
			if !reflect.DeepEqual(names, []string{"keep", "read_url"}) || localCalls != 2 || globalCalls != tc.wantGlobalCalls {
				t.Fatalf("tools=%v local calls=%d global calls=%d", names, localCalls, globalCalls)
			}
			allowed := config.WebConfig.ToolMask(ctx, &schema.ToolInfo{Name: "discard"})
			if allowed != !tc.localRejectsSearch || globalCalls != tc.wantGlobalCalls {
				t.Fatal("caller web mask was combined with global policy")
			}
		})
	}
}
