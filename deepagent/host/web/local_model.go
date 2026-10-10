package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	daldb "eino-cli/deepagent/dal/db"
	"eino-cli/deepagent/localmodel"
	agentmodel "eino-cli/deepagent/model"
	"gorm.io/gorm"
)

// EnableLocalModel 只注册样本和状态接口；模型训练仍由 Worker 自动触发。
func (server *Server) EnableLocalModel(localModelDAO *daldb.LocalModelDAO, autoTraining bool) {
	server.Mux.HandleFunc("/api/local-model/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodDelete {
			contentType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
			if !strings.EqualFold(contentType, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, errors.New("application/json is required"))
				return
			}
		}
		var result any
		var err error
		status := http.StatusOK
		switch {
		case r.URL.Path == "/api/local-model/status" && r.Method == http.MethodGet:
			var count int64
			var jobs []agentmodel.TrainingJob
			count, err = localModelDAO.CountNewTrainingExamples(r.Context())
			if err == nil {
				jobs, err = localModelDAO.GetTrainingJobs(r.Context())
			}
			result = map[string]any{"enabled": true, "auto_training_enabled": autoTraining, "new_example_count": count, "required_example_count": localmodel.MinimumTrainingExamples, "jobs": jobs}
		case r.URL.Path == "/api/local-model/jobs" && r.Method == http.MethodGet:
			result, err = localModelDAO.GetTrainingJobs(r.Context())
		case r.URL.Path == "/api/local-model/examples" && r.Method == http.MethodGet && r.URL.Query().Get("message_id") == "":
			result, err = localModelDAO.GetTrainingExamples(r.Context())
		case r.URL.Path == "/api/local-model/examples":
			var selection struct {
				ThreadID        string `json:"thread_id"`
				MessageID       string `json:"message_id"`
				IncludePrevious bool   `json:"include_previous"`
				Answer          string `json:"answer"`
				Confirmed       bool   `json:"confirmed"`
			}
			selection.ThreadID, selection.MessageID = r.URL.Query().Get("thread_id"), r.URL.Query().Get("message_id")
			selection.IncludePrevious = r.URL.Query().Get("include_previous") == "true"
			if r.Method == http.MethodPost {
				r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
				err = decode(r, &selection)
				if err != nil || !selection.Confirmed || strings.TrimSpace(selection.Answer) == "" {
					writeError(w, http.StatusBadRequest, errors.New("confirmed:true and a nonempty answer are required"))
					return
				}
			}
			threadID, threadErr := strconv.ParseInt(selection.ThreadID, 10, 64)
			messageID, messageErr := strconv.ParseInt(selection.MessageID, 10, 64)
			if threadErr != nil || messageErr != nil || threadID <= 0 || messageID <= 0 {
				writeError(w, http.StatusBadRequest, errors.New("valid thread_id and message_id are required"))
				return
			}
			switch r.Method {
			case http.MethodGet, http.MethodPost:
				var messages []*agentmodel.Message
				messages, err = localModelDAO.GetTrainingConversation(r.Context(), threadID, messageID, selection.IncludePrevious)
				result = messages
				if err == nil && r.Method == http.MethodPost {
					// 只允许修改回答副本，问题与前文始终由服务端读取。
					messages[len(messages)-1].Content = strings.TrimSpace(selection.Answer)
					result, err = localModelDAO.ConfirmTrainingExample(r.Context(), messages, selection.MessageID)
					status = http.StatusCreated
				}
			case http.MethodDelete:
				err = localModelDAO.RemoveTrainingExample(r.Context(), threadID, selection.MessageID)
				result = map[string]bool{"removed": err == nil}
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			status = http.StatusInternalServerError
			if errors.Is(err, daldb.ErrInvalidTrainingExample) || errors.Is(err, gorm.ErrRecordNotFound) {
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, status, result)
	})
}
