// Package gateway exposes the multi-user multi-thread HTTP/SSE surface (Gin).
package gateway

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"eino-cli/backend/runtime/deepagent"
)

// Server bundles gin.Engine with the dependencies handlers need.
type Server struct {
	router *deepagent.Router
	engine *gin.Engine
	log    *slog.Logger
}

// New builds the Server with routes registered.
func New(router *deepagent.Router) *Server {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())

	s := &Server{
		router: router,
		engine: engine,
		log:    slog.Default(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := s.engine.Group("/api/v1")
	api.Use(userContextMiddleware())

	api.POST("/threads/:tid/run", s.handleRun)
	api.POST("/threads/:tid/clear", s.handleClear)
	api.POST("/threads/:tid/plan-mode", s.handlePlanMode)

	api.POST("/threads/:tid/uploads", s.handleUploadCreate)
	api.GET("/threads/:tid/uploads", s.handleUploadList)
	api.DELETE("/threads/:tid/uploads/:name", s.handleUploadDelete)
}

// ListenAndServe starts the HTTP server on addr.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.engine,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}
