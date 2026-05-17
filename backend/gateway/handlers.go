package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"eino-cli/backend/agent/middlewares"
)

type runRequest struct {
	Prompt         string `json:"prompt"`
	PermissionMode string `json:"permission_mode,omitempty"`
}

// handleRun is the SSE-streaming primary endpoint. Each chunk emitted by
// the agent is forwarded as an SSE `data:` line; the run terminates with
// a `done` or `error` event so clients can stop reading without parsing.
//
// We spawn a goroutine for the agent run so this handler is free to do
// SSE flushes — sharing the HTTP goroutine between agent execution and
// SSE writes would block one or the other.
func (s *Server) handleRun(c *gin.Context) {
	tid := c.Param("tid")
	if tid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "thread_id required"})
		return
	}
	var req runRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt is required"})
		return
	}
	ctx := c.Request.Context()
	if mode := middlewares.PermissionMode(req.PermissionMode); mode != "" {
		if !middlewares.IsKnownMode(mode) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown permission_mode %q", mode)})
			return
		}
		ctx = middlewares.WithPermissionMode(ctx, mode)
	}

	rt, err := s.router.Get(ctx, tid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	type chunkEvent struct {
		Chunk string `json:"chunk"`
	}
	type errorEvent struct {
		Error string `json:"error"`
	}
	type doneEvent struct {
		Output string `json:"output"`
	}

	chunkCh := make(chan string, 64)
	errCh := make(chan error, 1)
	doneCh := make(chan string, 1)

	go func() {
		defer close(chunkCh)
		result, err := rt.ExecuteStream(ctx, req.Prompt, func(chunk string) {
			// Non-blocking: drop chunks if the SSE side fell behind by
			// more than the buffer. Better than wedging the agent.
			select {
			case chunkCh <- chunk:
			default:
			}
		})
		if err != nil {
			errCh <- err
			return
		}
		doneCh <- result.Output
	}()

	emit := func(event string, payload any) bool {
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, b)
		if err != nil {
			return false
		}
		c.Writer.Flush()
		return true
	}

	for {
		select {
		case chunk, ok := <-chunkCh:
			if !ok {
				chunkCh = nil
				continue
			}
			if !emit("chunk", chunkEvent{Chunk: chunk}) {
				return
			}
		case output := <-doneCh:
			emit("done", doneEvent{Output: output})
			return
		case err := <-errCh:
			emit("error", errorEvent{Error: err.Error()})
			return
		case <-ctx.Done():
			emit("error", errorEvent{Error: context.Cause(ctx).Error()})
			return
		}
	}
}

// handleClear: drop the runtime's conversation history. Cheap, no SSE.
func (s *Server) handleClear(c *gin.Context) {
	tid := c.Param("tid")
	rt, err := s.router.Get(c.Request.Context(), tid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rt.ClearHistory()
	c.JSON(http.StatusOK, gin.H{"status": "cleared"})
}

type planModeRequest struct {
	On bool `json:"on"`
}

// handlePlanMode: flip planning-mode hint on the runtime. Client can
// toggle this each turn — runtime treats it as a per-turn flag.
func (s *Server) handlePlanMode(c *gin.Context) {
	tid := c.Param("tid")
	var req planModeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rt, err := s.router.Get(c.Request.Context(), tid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	state, err := rt.SetPlanMode(c.Request.Context(), req.On)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plan_mode": state})
}
