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

//go:embed index.html app.js app.css
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
	s.Mux.HandleFunc("/app.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		http.ServeFileFS(w, r, files, "app.css")
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
		Status: thread.DisplayStatus(time.Now()), Title: strings.TrimSpace(thread.Metadata["title"]),
	}
	if view.Title == "" {
		view.Title = "未命名任务"
	}
	if thread.Profile != nil {
		view.WorkDir = thread.Profile.Cwd
	}
	return view
}

// Resolve missing titles in one read; do not rewrite stored metadata or history.
func (s *Server) getThreadViews(ctx context.Context, threads ...*dalmodel.Thread) ([]threadView, error) {
	views := make([]threadView, len(threads))
	var untitledIDs []int64
	for index, thread := range threads {
		views[index] = viewThread(thread)
		if strings.TrimSpace(thread.Metadata["title"]) == "" {
			untitledIDs = append(untitledIDs, thread.ThreadID)
		}
	}
	if len(untitledIDs) == 0 {
		return views, nil
	}
	database := s.Manager.DB().DB(ctx, true)
	firstInputIDs := database.Model(&dalmodel.Message{}).Select("MIN(message_id)").
		Where("thread_id IN ? AND message_type = ?", untitledIDs, inputpkg.MessageTypeInput).Group("thread_id")
	var messages []*dalmodel.Message
	err := database.Select("thread_id", "message_type", "payload").Where("message_id IN (?)", firstInputIDs).Find(&messages).Error
	if err != nil {
		return nil, err
	}
	titles := make(map[string]string, len(messages))
	for _, message := range messages {
		text := summarizeTaskTitle(messageText(message))
		if text != "" {
			titles[strconv.FormatInt(message.ThreadID, 10)] = text
		}
	}
	for index, thread := range threads {
		if strings.TrimSpace(thread.Metadata["title"]) == "" && titles[views[index].ID] != "" {
			views[index].Title = titles[views[index].ID]
		}
	}
	return views, nil
}

func summarizeTaskTitle(text string) string {
	characters := []rune(strings.Join(strings.Fields(text), " "))
	if len(characters) > 48 {
		return string(characters[:48]) + "…"
	}
	return string(characters)
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
		rows, err := s.getThreadViews(r.Context(), result.Threads...)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, rows)
	case http.MethodPost:
		var req createRequest
		decodeErr := decode(r, &req)
		if decodeErr != nil {
			writeError(w, http.StatusBadRequest, decodeErr)
			return
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
		views, err := s.getThreadViews(r.Context(), result.Thread)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, views[0])
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
	case parts[1] == "stream" && r.Method == http.MethodGet:
		s.stream(w, r, threadID)
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
	decodeErr := decode(r, &req)
	if decodeErr != nil {
		writeError(w, http.StatusBadRequest, decodeErr)
		return
	}
	var (
		result manager.ThreadMessageResult
		err    error
	)
	if req.Resume != nil {
		validationErr := req.Resume.Validate()
		if validationErr != nil {
			writeError(w, http.StatusBadRequest, validationErr)
			return
		}
		payload, marshalErr := json.Marshal(req.Resume)
		if marshalErr != nil {
			writeError(w, http.StatusBadRequest, marshalErr)
			return
		}
		var metadata map[string]string
		if req.Mode == inputpkg.UserMessageModeImplPlan {
			metadata = map[string]string{inputpkg.MetadataRunMode: inputpkg.RunModePlan}
		}
		result, err = s.Manager.Resume(r.Context(), threadID, &manager.InputMessage{
			Metadata:   metadata,
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

// stream forwards existing Manager events; persisted messages remain the history source.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, threadID int64) {
	result, err := s.Manager.ListThreads(r.Context(), manager.ListThreadsRequest{ThreadID: threadID})
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	subscription, err := s.Manager.SubscribeSession(r.Context(), result.Thread.SessionID, r.Header.Get("Last-Event-ID"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	defer subscription.Close()
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	_, err = fmt.Fprint(w, ": connected\n\n")
	if err != nil {
		return
	}
	err = controller.Flush()
	if err != nil {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case frame, ok := <-subscription.Events:
			if !ok {
				return
			}
			// Session streams can contain multiple threads. Never display another task's events.
			if frame.ThreadID != threadID {
				continue
			}
			data, marshalErr := json.Marshal(eventView{RunID: frame.RunID, Kind: frame.EventType, Payload: json.RawMessage(frame.Payload)})
			if marshalErr != nil {
				return
			}
			_, err = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", frame.QueueID, data)
		case <-heartbeat.C:
			_, err = fmt.Fprint(w, ": heartbeat\n\n")
		case <-r.Context().Done():
			return
		}
		if err != nil {
			return
		}
		err = controller.Flush()
		if err != nil {
			return
		}
	}
}
