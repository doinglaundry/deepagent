package distributed

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type projectSkill struct{ name, description, path, body string }
type skillCatalog struct {
	skills map[string]projectSkill
	names  []string
}

func discoverSkills(workDir string, configured []string) (*skillCatalog, error) {
	catalog := &skillCatalog{skills: map[string]projectSkill{}}
	paths := append([]string(nil), configured...)
	if len(paths) == 0 {
		paths = []string{filepath.Join(workDir, ".agents", "skills"), filepath.Join(workDir, ".codex", "skills")}
	}
	seen := map[string]bool{}
	directories := map[string]bool{}
	totalBytes := 0
	var load func(string, bool) error
	load = func(path string, required bool) error {
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) && !required {
			return nil
		}
		if err != nil {
			return err
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if directories[canonical] {
				return nil
			}
			directories[canonical] = true
			if len(directories) > 1024 {
				return fmt.Errorf("skill directory scan exceeds 1024 directories")
			}
			direct := filepath.Join(path, "SKILL.md")
			if _, err := os.Stat(direct); err == nil {
				return load(direct, true)
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.IsDir() {
					if err := load(filepath.Join(path, entry.Name()), false); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if seen[canonical] {
			return nil
		}
		seen[canonical] = true
		if len(seen) > 64 || info.Size() > 128<<10 {
			return fmt.Errorf("configured skills exceed 64 files or 128 KiB per skill")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(file, (128<<10)+1))
		_ = file.Close()
		if err != nil {
			return err
		}
		totalBytes += len(data)
		if len(data) > 128<<10 || totalBytes > 512<<10 {
			return fmt.Errorf("configured skills exceed size limits")
		}
		skill := projectSkill{name: filepath.Base(filepath.Dir(path)), path: path, body: string(data)}
		if strings.HasPrefix(skill.body, "---\n") || strings.HasPrefix(skill.body, "---\r\n") {
			normalized := strings.ReplaceAll(skill.body, "\r\n", "\n")
			remaining := strings.TrimPrefix(normalized, "---\n")
			if end := strings.Index(remaining, "\n---"); end >= 0 {
				var metadata struct {
					Name        string `yaml:"name"`
					Description string `yaml:"description"`
				}
				if err := yaml.Unmarshal([]byte(remaining[:end]), &metadata); err != nil {
					return fmt.Errorf("skill metadata %s: %w", path, err)
				}
				if strings.TrimSpace(metadata.Name) != "" {
					skill.name = strings.TrimSpace(metadata.Name)
				}
				skill.description = strings.Join(strings.Fields(metadata.Description), " ")
			}
		}
		if skill.description == "" {
			skill.description = "Project skill instructions."
		}
		if len(skill.description) > 1000 {
			skill.description = skill.description[:1000] + "…"
		}
		if _, exists := catalog.skills[skill.name]; exists {
			return fmt.Errorf("duplicate skill name %q; configure an unambiguous collection", skill.name)
		}
		catalog.skills[skill.name] = skill
		catalog.names = append(catalog.names, skill.name)
		return nil
	}
	for _, path := range paths {
		if err := load(path, len(configured) > 0); err != nil {
			return nil, fmt.Errorf("load skill catalog: %w", err)
		}
	}
	sort.Strings(catalog.names)
	return catalog, nil
}
func (c *skillCatalog) prompt() string {
	if len(c.names) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("Available project skills. When a skill applies, call activate_skill with its exact name before using it. The tool returns its full instructions and source path.\n")
	for _, name := range c.names {
		skill := c.skills[name]
		fmt.Fprintf(&out, "- %s: %s (source: %s)\n", name, skill.description, skill.path)
	}
	return out.String()
}
func (c *skillCatalog) tool() *activateSkillTool { return &activateSkillTool{catalog: c} }

type activateSkillTool struct{ catalog *skillCatalog }

func (*activateSkillTool) ReadOnly() bool { return true }
func (t *activateSkillTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "activate_skill", Desc: "Load a discovered project's full skill instructions into the conversation. Choose a relevant skill by exact catalog name; its instructions remain in shared model history.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"name": {Type: schema.String, Required: true, Enum: t.catalog.names}})}, nil
}
func (t *activateSkillTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", err
	}
	skill, ok := t.catalog.skills[in.Name]
	if !ok {
		return "", fmt.Errorf("skill %q is not in the configured catalog", in.Name)
	}
	return fmt.Sprintf("Skill: %s\nSource: %s\n\n%s", skill.name, skill.path, skill.body), nil
}
