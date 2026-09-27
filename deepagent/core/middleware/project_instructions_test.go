package middleware

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/deepagent/core/backend"
)

func TestProjectInstructionsOnlyLoadDiscipline(t *testing.T) {
	root := t.TempDir()
	files := mustLocalFilesystem(t, &backend.LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	mw := NewProjectInstructions(files)
	if out, err := mw.BuildPrompt(context.Background()); err != nil || len(out) != 0 {
		t.Fatalf("missing file: %v %v", out, err)
	}
	cases := []struct{ text, want string }{
		{"## Other\nstyle only", ""},
		{"## Agent Working Discipline extra\nwrong section", ""},
		{"## Other\nstyle only\n## Agent Working Discipline\nfollow project\n### Nested\nretain\n## Other\nomit", "follow project\n### Nested\nretain"},
		{strings.Repeat("line\n", 2100) + "## Agent Working Discipline\nlate section", "late section"},
		{"## Agent Working Discipline\r\nwindows\r\n## Other\r\nomit", "windows"},
	}
	for _, tc := range cases {
		if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(tc.text), 0600); err != nil {
			t.Fatal(err)
		}
		out, err := mw.BuildPrompt(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" {
			if len(out) != 0 {
				t.Fatal(out)
			}
			continue
		}
		if len(out) != 1 || out[0].Content != "<agent_discipline>\n"+tc.want+"\n</agent_discipline>" {
			t.Fatalf("output=%v want=%q", out, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mw.BuildPrompt(ctx); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
}
