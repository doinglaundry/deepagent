package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dalmodel "eino-cli/deepagent/dal/model"
)

func TestEmbeddedWebClient(t *testing.T) {
	handler := New(nil, t.TempDir()).Handler()
	for _, path := range []string{"/", "/app.js", "/app.css"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if path == "/" && !strings.Contains(response.Body.String(), `src="/app.js"`) {
			t.Fatal("page does not load embedded client")
		}
		if path == "/app.css" && !strings.Contains(response.Header().Get("Content-Type"), "text/css") {
			t.Fatal("stylesheet has incorrect MIME type")
		}
		if path == "/app.js" && !strings.Contains(response.Header().Get("Content-Type"), "javascript") {
			t.Fatal("client has incorrect MIME type")
		}
	}
}

func TestWorkspaceFilePreviewCannotFollowSymlinkOutsideRoot(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	writeErr2 := os.WriteFile(filepath.Join(workspace, "inside.txt"), []byte("inside"), 0600)
	if writeErr2 != nil {
		t.Fatal(writeErr2)
	}
	writeErr := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	symlinkErr := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(workspace, "escape.txt"))
	if symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	content, err := readWorkspaceFile(workspace, "inside.txt")
	if err != nil || string(content) != "inside" {
		t.Fatalf("inside file = %q, %v", content, err)
	}
	_, readWorkspaceFileErr := readWorkspaceFile(workspace, "escape.txt")
	if readWorkspaceFileErr == nil {
		t.Fatal("preview followed symlink outside workspace")
	}
}

func TestMissingTaskTitleAndMessageSummary(t *testing.T) {
	view := viewThread(&dalmodel.Thread{ThreadID: 2000000000000000001})
	if view.Title != "未命名任务" {
		t.Fatalf("missing title = %q", view.Title)
	}
	name := summarizeTaskTitle("  阅读项目\n  解释入口\t ")
	if name != "阅读项目 解释入口" {
		t.Fatalf("summary = %q", name)
	}
	long := summarizeTaskTitle(strings.Repeat("项", 49))
	if long != strings.Repeat("项", 48)+"…" {
		t.Fatalf("Unicode summary = %q", long)
	}
	named := viewThread(&dalmodel.Thread{Metadata: map[string]string{"title": "我的任务"}})
	if named.Title != "我的任务" {
		t.Fatalf("explicit title changed: %q", named.Title)
	}
}
