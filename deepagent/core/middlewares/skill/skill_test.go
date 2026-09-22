package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

type testLoader struct{ item *SkillMetadata }

func (l testLoader) ListSkills(context.Context) ([]*SkillMetadata, error) {
	return []*SkillMetadata{l.item}, nil
}

func TestActivateSkillLoadsInstructions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("follow these instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	middleware := New(testLoader{item: &SkillMetadata{Name: "review", Description: "Review code", Path: path}})
	prompt, err := middleware.BuildPrompt(context.Background())
	if err != nil || len(prompt) != 1 || !strings.Contains(prompt[0].Content, "review") {
		t.Fatalf("prompt = %+v, %v", prompt, err)
	}
	items, err := middleware.Tools(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("tools = %d, %v", len(items), err)
	}
	output, err := items[0].(tool.InvokableTool).InvokableRun(context.Background(), `{"name":"review"}`)
	if err != nil || !strings.Contains(output, "follow these instructions") {
		t.Fatalf("activate_skill = %q, %v", output, err)
	}
}
