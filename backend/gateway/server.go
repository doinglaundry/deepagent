// Package gateway exposes the multi-user, multi-thread HTTP/SSE surface
// in front of the agent runtime. Gin chosen for its mature router +
// middleware story and small surface.
package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"eino-cli/backend/config"
	"eino-cli/backend/runtime/eino"
)

// Server wraps gin.Engine plus the dependencies handlers need (Router,
// cfg). New returns it ready-to-Listen; the caller decides on the port
// and lifetime.
type Server struct {
	cfg    *config.Config
	router *eino.Router
	engine *gin.Engine
	log    *slog.Logger
}

// New wires the HTTP server. ctx is intentionally NOT a parameter — the
// Router idle loop has its own stop channel and the engine's lifetime is
// the process's, not a per-call ctx.
func New(cfg *config.Config, router *eino.Router) *Server {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())

	s := &Server{
		cfg:    cfg,
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

// ListenAndServe starts the HTTP server. Plain wrapper — keep the
// http.Server boilerplate (timeouts, graceful shutdown) close to the
// caller that needs to coordinate the lifecycle.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.engine,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

// Engine exposes the underlying gin engine so callers can compose extra
// routes (admin endpoints, prometheus middleware, …) without forking
// this package.
func (s *Server) Engine() *gin.Engine { return s.engine }

// Shutdown is a stub that callers compose with their own http.Server's
// Shutdown when they want graceful drain. Kept here so the surface is
// symmetric with Router.Shutdown.
func (s *Server) Shutdown(ctx context.Context) error {
	s.router.Shutdown()
	return nil
}
