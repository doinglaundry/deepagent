package web

import (
	"net/http"
	"net/http/httptest"
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
