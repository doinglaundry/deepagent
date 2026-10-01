package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dalmodel "eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/manager"
	inputpkg "eino-cli/deepagent/protocol/input"
)

//go:embed index.html app.js
var files embed.FS

type Server struct {
	Manager *manager.Manager
	Root    string
	Mux     *http.ServeMux
}

func New(coordinator *manager.Manager, root string) *Server {
	s := &Server{Manager: coordinator, Root: root, Mux: http.NewServeMux()}
	s.Mux.HandleFunc("/", s.index)
	s.Mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		http.ServeFileFS(w, r, files, "app.js")
	})
	s.Mux.HandleFunc("/api/threads", s.threads)
	s.Mux.HandleFunc("/api/threads/", s.thread)
	return s
}

func (s *Server) Handler() http.Handler { return s.Mux }

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	t, err := template.ParseFS(files, "index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = t.Execute(w, nil)
}

func decode[T any](r *http.Request, out *T) error { return json.NewDecoder(r.Body).Decode(out) }
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

type threadView struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	WorkDir   string `json:"work_dir"`
}

func viewThread(thread *dalmodel.Thread) threadView {
	if thread == nil {
		return threadView{}
	}
	view := threadView{
		ID: strconv.FormatInt(thread.ThreadID, 10), SessionID: thread.SessionID,
		Status: thread.DisplayStatus(time.Now()), Title: thread.Metadata["title"],
	}
	if thread.Profile != nil {
		view.WorkDir = thread.Profile.Cwd
	}
	return view
}

type createRequest struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	WorkDir   string `json:"work_dir"`
}

func (s *Server) threads(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		result, err := s.Manager.ListThreads(r.Context(), manager.ListThreadsRequest{SessionID: r.URL.Query().Get("session_id"), Limit: 100})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		rows := make([]threadView, 0, len(result.Threads))
		for _, thread := range result.Threads {
			rows = append(rows, viewThread(thread))
		}
		writeJSON(w, http.StatusOK, rows)
	case http.MethodPost:
		var req createRequest
		{
			err := decode(r, &req)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		if strings.TrimSpace(req.SessionID) == "" {
			writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
			return
		}
		if strings.TrimSpace(req.WorkDir) == "" {
			req.WorkDir = s.Root
		}
		result, err := s.Manager.Submit(r.Context(), manager.SubmitRequest{
			SessionID: req.SessionID, Title: req.Name, Profile: &dalmodel.Profile{Cwd: req.WorkDir},
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, viewThread(result.Thread))
	default:
		w.Header().Set("allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) thread(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/threads/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	threadID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || threadID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid thread id"))
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		result, err := s.Manager.ListThreads(r.Context(), manager.ListThreadsRequest{ThreadID: threadID})
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, viewThread(result.Thread))
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	switch {
	case parts[1] == "messages" && r.Method == http.MethodPost:
		s.submitMessage(w, r, threadID)
	case parts[1] == "events" && r.Method == http.MethodGet:
		s.events(w, r, threadID)
	case parts[1] == "file" && r.Method == http.MethodGet:
		s.openFile(w, r, threadID)
	default:
		http.NotFound(w, r)
	}
}

type submitRequest struct {
	Text   string
	Mode   inputpkg.UserMessageMode
	Resume *inputpkg.ResumeRunPayload
}

func (s *Server) submitMessage(w http.ResponseWriter, r *http.Request, threadID int64) {
	var req submitRequest
	{
		err := decode(r, &req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	var (
		result manager.ThreadMessageResult
		err    error
	)
	if req.Resume != nil {
		{
			err := req.Resume.Validate()
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		payload, marshalErr := json.Marshal(req.Resume)
		if marshalErr != nil {
			writeError(w, http.StatusBadRequest, marshalErr)
			return
		}
		result, err = s.Manager.Resume(r.Context(), threadID, &manager.InputMessage{
			SenderType: "user", MessageType: inputpkg.MessageTypeResume, Payload: payload,
		})
	} else {
		if strings.TrimSpace(req.Text) == "" {
			writeError(w, http.StatusBadRequest, errors.New("text is required"))
			return
		}
		payload, marshalErr := json.Marshal(inputpkg.UserMessage{
			Mode:  req.Mode,
			Parts: []inputpkg.MessagePart{{Type: inputpkg.MessagePartTypeText, Text: req.Text}},
		})
		if marshalErr != nil {
			writeError(w, http.StatusBadRequest, marshalErr)
			return
		}
		result, err = s.Manager.Submit(r.Context(), manager.SubmitRequest{ThreadID: threadID, Input: &manager.InputMessage{
			SenderType: "user", MessageType: inputpkg.MessageTypeInput, Payload: payload,
		}})
	}
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result.Message)
}

type eventView struct {
	Sequence string          `json:"sequence"`
	Status   string          `json:"status,omitempty"`
	RunID    string          `json:"run_id,omitempty"`
	Kind     string          `json:"kind"`
	Text     string          `json:"text,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, threadID int64) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	result, err := s.Manager.ListMessages(r.Context(), manager.ListMessagesRequest{ThreadID: threadID, AfterID: after, Limit: 200})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	rows := make([]eventView, 0, len(result.Messages))
	for _, message := range result.Messages {
		rows = append(rows, eventView{
			Sequence: strconv.FormatInt(message.MessageID, 10), RunID: message.TriggerRunID, Kind: message.MessageType,
			Status: messageDisplayStatus(message, result.Runs[message.TriggerRunID]),
			Text:   messageText(message), Payload: append(json.RawMessage(nil), message.Payload...),
		})
	}
	writeJSON(w, http.StatusOK, rows)
}

func messageDisplayStatus(message *dalmodel.Message, run *dalmodel.RunRecord) string {
	if message.OutputKey != nil {
		return ""
	}
	if message.Status == dalmodel.MessageStatusCanceled {
		return "canceled"
	}
	if run != nil {
		switch run.Status {
		case "finished":
			return "completed"
		case "interrupted", "failed":
			return "interrupted"
		}
	}
	return message.Status
}

func messageText(message *dalmodel.Message) string {
	if message == nil {
		return ""
	}
	if message.MessageType == inputpkg.MessageTypeInput {
		var input inputpkg.UserMessage
		if json.Unmarshal(message.Payload, &input) == nil {
			var parts []string
			for _, part := range input.Parts {
				if part.Text != "" {
					parts = append(parts, part.Text)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	var payload struct {
		Delta   string
		Message string
		Parts   []struct{ Text string }
	}
	if json.Unmarshal(message.Payload, &payload) != nil {
		return ""
	}
	if payload.Delta != "" {
		return payload.Delta
	}
	if payload.Message != "" {
		return payload.Message
	}
	var parts []string
	for _, part := range payload.Parts {
		if part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (s *Server) openFile(w http.ResponseWriter, r *http.Request, threadID int64) {
	result, err := s.Manager.ListThreads(r.Context(), manager.ListThreadsRequest{ThreadID: threadID})
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if result.Thread.Profile == nil || strings.TrimSpace(result.Thread.Profile.Cwd) == "" {
		writeError(w, http.StatusBadRequest, errors.New("thread has no work directory"))
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("path"))
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("path required"))
		return
	}
	base, err := filepath.Abs(result.Thread.Profile.Cwd)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path, err := filepath.Abs(filepath.Join(base, name))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeError(w, http.StatusForbidden, fmt.Errorf("path outside thread work directory"))
		return
	}
	data, err := readWorkspaceFile(base, rel)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": rel, "content": string(data)})
}

func readWorkspaceFile(base, relative string) ([]byte, error) {
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(relative)
}

func (s *Server) Shutdown(context.Context) error { return nil }
