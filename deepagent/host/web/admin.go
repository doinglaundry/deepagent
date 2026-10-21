package web

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"eino-cli/deepagent/dal/db"
	agentmodel "eino-cli/deepagent/model"
)

type inspectedMessage struct {
	ID        string            `json:"id"`
	ThreadID  string            `json:"thread_id"`
	RunID     string            `json:"run_id"`
	Kind      string            `json:"kind"`
	Status    string            `json:"status"`
	CreatedAt time.Time         `json:"created_at"`
	Payload   json.RawMessage   `json:"payload"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type inspectedRun struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	Status   string `json:"status"`
}

type inspectionResponse struct {
	Thread           threadView         `json:"thread"`
	Threads          []threadView       `json:"threads"`
	Runs             []inspectedRun     `json:"runs"`
	Messages         []inspectedMessage `json:"messages"`
	HasMore          bool               `json:"has_more"`
	NextID           string             `json:"next_id"`
	MatchedMessageID string             `json:"matched_message_id,omitempty"`
}

func (server *Server) registerAdmin() {
	assets, _ := fs.Sub(files, "管理系统")
	server.Mux.Handle("/admin/", http.StripPrefix("/admin/", http.FileServer(http.FS(assets))))
	server.Mux.HandleFunc("/api/admin/messages", server.inspectMessages)
}

func (server *Server) inspectMessages(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", "GET")
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	parameters := request.URL.Query()
	query := agentmodel.MessageInspectionQuery{Kind: parameters.Get("kind"), ID: strings.TrimSpace(parameters.Get("id"))}
	var err error
	if parameters.Has("after") {
		query.AfterID, err = strconv.ParseInt(parameters.Get("after"), 10, 64)
	}
	if err == nil && parameters.Has("limit") {
		query.Limit, err = strconv.Atoi(parameters.Get("limit"))
	}
	if err == nil && parameters.Has("include_subtasks") {
		query.IncludeSubtasks, err = strconv.ParseBool(parameters.Get("include_subtasks"))
	}
	if err == nil {
		err = query.Validate()
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, agentmodel.ErrInvalidInspectionQuery)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	dao := db.MessageDAO{Client: server.Manager.DB()}
	inspection, err := dao.InspectMessages(ctx, query)
	if errors.Is(err, db.MySQLErrRecordNotFound) {
		writeError(writer, http.StatusNotFound, errors.New("未找到该 ID，请检查查询类型和 ID"))
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	runsByID := make(map[string]*agentmodel.RunRecord, len(inspection.Runs))
	for _, run := range inspection.Runs {
		runsByID[run.RunID] = run
	}
	for _, thread := range inspection.Threads {
		thread.LastRun = runsByID[thread.LastRunID]
	}
	inspection.Thread.LastRun = runsByID[inspection.Thread.LastRunID]
	response := inspectionResponse{Thread: viewThread(inspection.Thread), Threads: []threadView{}, Runs: []inspectedRun{}, Messages: []inspectedMessage{}, HasMore: inspection.HasMore, MatchedMessageID: inspection.MatchedMessageID}
	for _, thread := range inspection.Threads {
		response.Threads = append(response.Threads, viewThread(thread))
	}
	for _, run := range inspection.Runs {
		response.Runs = append(response.Runs, inspectedRun{ID: run.RunID, ThreadID: strconv.FormatInt(run.ThreadID, 10), Status: run.Status})
	}
	for _, message := range inspection.Messages {
		payload := json.RawMessage(message.Payload)
		if !json.Valid(payload) {
			payload, _ = json.Marshal(string(message.Payload))
		}
		response.Messages = append(response.Messages, inspectedMessage{ID: strconv.FormatInt(message.MessageID, 10), ThreadID: strconv.FormatInt(message.ThreadID, 10), RunID: message.TriggerRunID, Kind: message.MessageType, Status: message.Status, CreatedAt: message.CreatedAt, Payload: payload, Metadata: message.Metadata})
		response.NextID = strconv.FormatInt(message.MessageID, 10)
	}
	writeJSON(writer, http.StatusOK, response)
}
