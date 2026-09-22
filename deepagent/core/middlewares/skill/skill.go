package skill

import (
	"context"
	"eino-cli/deepagent/core/middlewares"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"io"
	"os"
	"sort"
	"strings"
)

type Loader interface {
	ListSkills(context.Context) ([]*SkillMetadata, error)
}
type SkillMetadata struct {
	Name        string
	Description string
	Path        string
}
type Middleware struct {
	middleware.BaseMiddleware
	loader Loader
}

func New(loader Loader) middleware.Middleware { return &Middleware{loader: loader} }
func (m *Middleware) Name() string            { return "skill" }

func (m *Middleware) BuildPrompt(ctx context.Context) ([]*schema.Message, error) {
	skills, err := m.list(ctx)
	if err != nil || len(skills) == 0 {
		return nil, err
	}
	var prompt strings.Builder
	prompt.WriteString("Available project skills. Call activate_skill with an exact name before applying one.\n")
	for _, item := range skills {
		fmt.Fprintf(&prompt, "- %s: %s\n", item.Name, item.Description)
	}
	return []*schema.Message{schema.SystemMessage(prompt.String())}, nil
}

func (m *Middleware) Tools(context.Context) ([]tool.BaseTool, error) {
	if m == nil || m.loader == nil {
		return nil, nil
	}
	return []tool.BaseTool{&activateSkillTool{loader: m.loader}}, nil
}

func (m *Middleware) list(ctx context.Context) ([]*SkillMetadata, error) {
	if m == nil || m.loader == nil {
		return nil, nil
	}
	items, err := m.loader.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	items = append([]*SkillMetadata(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

type skillContentLoader interface {
	LoadSkill(context.Context, string) (string, error)
}

type activateSkillTool struct{ loader Loader }

func (*activateSkillTool) ReadOnly() bool { return true }

func (t *activateSkillTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	items, err := t.loader.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil && strings.TrimSpace(item.Name) != "" {
			names = append(names, item.Name)
		}
	}
	sort.Strings(names)
	return &schema.ToolInfo{Name: "activate_skill", Desc: "Load full instructions for one available project skill.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"name": {Type: schema.String, Required: true, Enum: names},
	})}, nil
}

func (t *activateSkillTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "", err
	}
	if loader, ok := t.loader.(skillContentLoader); ok {
		return loader.LoadSkill(ctx, input.Name)
	}
	items, err := t.loader.ListSkills(ctx)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item == nil || item.Name != input.Name {
			continue
		}
		file, err := os.Open(item.Path)
		if err != nil {
			return "", err
		}
		data, err := io.ReadAll(io.LimitReader(file, (128<<10)+1))
		_ = file.Close()
		if err != nil {
			return "", err
		}
		if len(data) > 128<<10 {
			return "", fmt.Errorf("skill %q exceeds 128 KiB", input.Name)
		}
		return fmt.Sprintf("Skill: %s\nSource: %s\n\n%s", item.Name, item.Path, data), nil
	}
	return "", fmt.Errorf("skill %q is unavailable", input.Name)
}
