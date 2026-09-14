package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"eino-cli/manager/api"
	"eino-cli/protocol"
)

//go:embed index.html
var files embed.FS

type Server struct {
	Manager api.Manager
	Mux     *http.ServeMux
}

func New(m api.Manager) *Server {
	s := &Server{Manager: m, Mux: http.NewServeMux()}
	s.Mux.HandleFunc("/", s.index)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

type createRequest struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	WorkDir   string `json:"work_dir"`
}

func (s *Server) threads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		rows, err := s.Manager.ListSessionThreads(ctx, r.URL.Query().Get("session_id"), 100, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, rows)
	case http.MethodPost:
		var req createRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.SessionID) == "" {
			req.SessionID = protocol.NewID("session")
		}
		thread, err := s.Manager.CreateThread(ctx, api.CreateThreadRequest{SessionID: req.SessionID, Name: req.Name, WorkDir: req.WorkDir})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, thread)
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
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		thread, err := s.Manager.GetThread(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, thread)
		return
	}
	if len(parts) != 2 || parts[1] != "messages" || r.Method != http.MethodPost {
		if len(parts) == 2 && parts[1] == "file" && r.Method == http.MethodGet {
			s.openFile(w, r, id)
			return
		}
		if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
			var after int64
			if v := r.URL.Query().Get("after"); v != "" {
				_, _ = fmt.Sscan(v, &after)
			}
			rows, err := s.Manager.ListEvents(r.Context(), api.EventFilter{ThreadID: id, After: after, Limit: 200})
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, rows)
			return
		}
		http.NotFound(w, r)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input, err := s.Manager.SubmitInput(r.Context(), id, protocol.Input{Kind: protocol.InputUser, Text: req.Text})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, input)
}

func (s *Server) openFile(w http.ResponseWriter, r *http.Request, id string) {
	t, err := s.Manager.GetThread(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("path"))
	if name == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("path required"))
		return
	}
	base, err := filepath.Abs(t.WorkDir)
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
	b, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": rel, "content": string(b)})
}

// Shutdown closes resources owned by the HTTP server's Manager when supported.
func (s *Server) Shutdown(ctx context.Context) error {
	if c, ok := s.Manager.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}
