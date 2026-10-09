package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dalmodel "eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/manager"
)

func TestEmbeddedWebClient(t *testing.T) {
	handler := New(nil, t.TempDir()).Handler()
	for _, path := range []string{"/", "/app.js", "/i18n.js", "/app.css", "/assets/office.png"} {
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

func TestCancelRejectsSimpleCrossOriginForm(t *testing.T) {
	handler := New(nil, t.TempDir()).Handler()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/threads/1/cancel", strings.NewReader("reason=cancel"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://unrelated.example")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("cancel form status=%d", response.Code)
	}
}

func TestThreadViewProjectsCurrentRunOutcome(t *testing.T) {
	for _, status := range []string{"finished", "failed", "interrupted"} {
		view := viewThread(&dalmodel.Thread{ThreadID: 1, LastRun: &dalmodel.RunRecord{RunID: "current-run", Status: status}})
		if view.RunID != "current-run" || view.RunStatus != status {
			t.Fatalf("current Run projection = %+v", view)
		}
	}
}

func TestResumeRejectsSimpleCrossOriginRequest(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/threads/1/messages", strings.NewReader(`{"resume":{"approval":{"approved":true,"always_allow":true}}}`))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Origin", "https://unrelated.example")
	New(nil, t.TempDir()).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("resume form status=%d", response.Code)
	}
}

func TestThreadViewExposesParentIdentityAsString(t *testing.T) {
	view := viewThread(&dalmodel.Thread{
		ThreadID: 2000000000000026246,
		Metadata: map[string]string{"parent_thread_id": "2000000000000020608"},
	})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	err = json.Unmarshal(raw, &fields)
	if err != nil {
		t.Fatal(err)
	}
	if fields["parent_thread_id"] != "2000000000000020608" {
		t.Fatalf("parent identity lost or rounded: %s", raw)
	}
}

func TestMissingThreadErrorHasNotFoundStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"missing thread", fmt.Errorf("query thread: %w", manager.ErrThreadNotFound), http.StatusNotFound},
		{"database failure", errors.New("database unavailable"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeError(response, http.StatusInternalServerError, test.err)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}
