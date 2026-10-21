package web

import (
	agentmodel "eino-cli/deepagent/model"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminRejectsInvalidQueriesBeforeAccessingStorage(t *testing.T) {
	handler := New(nil, t.TempDir()).Handler()
	for _, query := range []string{"", "kind=unknown&id=1", "kind=thread&id=-1", "kind=message&id=1.5", "kind=run&id=", "kind=thread&id=1&after=bad", "kind=thread&id=1&include_subtasks=perhaps"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/messages?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Errorf("query=%q status=%d body=%s", query, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/admin/messages", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("write request status=%d", response.Code)
	}
}

func TestAdminAssetsHaveCorrectContentTypes(t *testing.T) {
	handler := New(nil, t.TempDir()).Handler()
	for _, asset := range []struct{ path, contentType string }{{"/admin/", "text/html"}, {"/admin/app.js", "javascript"}, {"/admin/app.css", "text/css"}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, asset.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), asset.contentType) {
			t.Errorf("%s: status=%d content-type=%s", asset.path, response.Code, response.Header().Get("Content-Type"))
		}
	}
}

func TestMessageTextRetainsAssistantTextWithFullModelPayload(t *testing.T) {
	payload, err := json.Marshal(agentmodel.MessageEventPayload{Message: agentmodel.NewAssistantMessage("complete", nil), Parts: []agentmodel.OutputMessagePart{{Type: agentmodel.OutputMessagePartTypeText, Text: "complete"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := messageText(&agentmodel.MailboxMessage{MessageType: "assistant", Payload: payload})
	if got != "complete" {
		t.Fatalf("assistant text=%q", got)
	}
}
