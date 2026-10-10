package web

import (
	"errors"
	"net/http"
	"strings"

	daldb "eino-cli/deepagent/dal/db"
	agentmodel "eino-cli/deepagent/model"
)

// EnableLocalModel registers sample/job APIs. MLX processes belong to Worker.
func (server *Server) EnableLocalModel(localModelDAO *daldb.LocalModelDAO) {
	server.Mux.HandleFunc("/api/local-model/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			contentType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
			if !strings.EqualFold(contentType, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, errors.New("application/json is required"))
				return
			}
		}
		var responsePayload any
		var err error
		responseStatusCode := http.StatusOK
		switch {
		case r.URL.Path == "/api/local-model/examples" && r.Method == http.MethodPost:
			var confirmationRequest struct {
				Messages  []*agentmodel.Message `json:"messages"`
				Confirmed bool                  `json:"confirmed"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			err = decode(r, &confirmationRequest)
			if err != nil || !confirmationRequest.Confirmed {
				writeError(w, http.StatusBadRequest, errors.New("confirmed:true and a text conversation are required"))
				return
			}
			responsePayload, err = localModelDAO.ConfirmTrainingExample(r.Context(), confirmationRequest.Messages)
			responseStatusCode = http.StatusCreated
		case r.URL.Path == "/api/local-model/jobs" && r.Method == http.MethodGet:
			responsePayload, err = localModelDAO.GetTrainingJobs(r.Context())
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			responseStatusCode = http.StatusInternalServerError
			if errors.Is(err, daldb.ErrInvalidTrainingExample) {
				responseStatusCode = http.StatusBadRequest
			}
			writeError(w, responseStatusCode, err)
			return
		}
		writeJSON(w, responseStatusCode, responsePayload)
	})
}
