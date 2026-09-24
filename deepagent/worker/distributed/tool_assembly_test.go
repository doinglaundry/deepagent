package distributed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/core/modelhub"
	"eino-cli/deepagent/manager/api"
	manager "eino-cli/deepagent/manager/compat"
	"eino-cli/deepagent/protocol"
	"github.com/cloudwego/eino/schema"
)

func TestFactoryUsesCanonicalToolsAndPlanPrompt(t *testing.T) {
	for _, planMode := range []bool{false, true} {
		t.Run(fmt.Sprint(planMode), func(t *testing.T) {
			type request struct {
				Messages []schema.Message
				Tools    []struct{ Function schema.ToolInfo }
			}
			requests := make(chan request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input request
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				requests <- input
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			m := manager.NewMemory("tool-assembly")
			factory, cleanup, err := NewFactory(ctx, m, Config{Manager: manager.Config{Namespace: "tool-assembly", MySQLDSN: "unused", RedisAddr: "unused"}, Web: WebConfig{Enabled: true, SearchURL: "https://search.example"}, DefaultModel: "test", Models: []modelhub.Config{{Name: "test", Provider: "openai", Model: "test", BaseURL: server.URL, APIKey: "test"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			input := protocol.Input{Kind: protocol.InputUser, Text: "inspect"}
			workDir := t.TempDir()
			skillDir := filepath.Join(workDir, ".agents", "skills", "verify")
			if err := os.MkdirAll(skillDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: verify\ndescription: PROJECT_SKILL_SUMMARY\n---\nSKILL_BODY_ONLY"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workDir, "AGENTS.md"), []byte("## Core Principles\nREFERENCE_ONLY\n## Agent Working Discipline\nPROJECT_DISCIPLINE\n## Details\nREFERENCE_ONLY"), 0600); err != nil {
				t.Fatal(err)
			}
			owner, err := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: workDir, PlanMode: planMode, Input: &input})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := m.ClaimThread(ctx, owner.ID, "worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := factory(ctx, claim)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			if _, err := runtime.PostMessage(ctx, claim.Inputs[0]); err != nil {
				t.Fatal(err)
			}
			for {
				select {
				case event := <-runtime.Events():
					if event.Kind == protocol.EventRunFailed {
						t.Fatalf("run failed: %+v", event)
					}
					if event.Kind != protocol.EventRunCompleted {
						continue
					}
					input := <-requests
					counts := map[string]int{}
					for _, item := range input.Tools {
						counts[item.Function.Name]++
					}
					for name, count := range counts {
						if count != 1 {
							t.Fatalf("duplicate %s: %d", name, count)
						}
					}
					for _, name := range []string{"activate_skill", "update_plan", "ask_user", "read_file", "glob", "grep", "rg", "semantic_search", "search_files", "read_url", "web_search"} {
						if counts[name] != 1 {
							t.Fatalf("missing %s: %v", name, counts)
						}
					}
					for _, name := range []string{"write_file", "edit_file", "delete_file", "execute", "shell", "await_shell"} {
						if (counts[name] > 0) == planMode {
							t.Fatalf("planMode=%t tool=%s names=%v", planMode, name, counts)
						}
					}
					instructions := 0
					discipline := 0
					skills := 0
					for _, message := range input.Messages {
						if strings.Contains(message.Content, "PROJECT_SKILL_SUMMARY") {
							skills++
						}
						if strings.Contains(message.Content, "SKILL_BODY_ONLY") {
							t.Fatal("skill body was loaded before activation")
						}
						if strings.Contains(message.Content, "PROJECT_DISCIPLINE") {
							discipline++
						}
						if strings.Contains(message.Content, "REFERENCE_ONLY") {
							t.Fatal("reference sections leaked into model prompt")
						}
						if strings.Contains(message.Content, "<plan_mode>") && strings.Contains(message.Content, "update_plan") {
							instructions++
						}
					}
					if skills != 1 {
						t.Fatalf("skill prompts=%d", skills)
					}
					if discipline != 1 {
						t.Fatalf("project discipline prompts=%d", discipline)
					}
					if instructions != 1 {
						t.Fatalf("plan prompts=%d", instructions)
					}
					return
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		})
	}
}
