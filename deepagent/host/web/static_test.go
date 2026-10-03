package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedWebClient(t *testing.T) {
	handler := New(nil, t.TempDir()).Handler()
	for _, path := range []string{"/", "/app.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if path == "/" && !strings.Contains(response.Body.String(), `src="/app.js"`) {
			t.Fatal("page does not load embedded client")
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
