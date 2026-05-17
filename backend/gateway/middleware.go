package gateway

import (
	"github.com/gin-gonic/gin"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/runtime"
)

const (
	headerUserID = "X-User-Id"
)

// userContextMiddleware stamps UserID and ThreadID onto the request's
// context so downstream handlers / agent middlewares / tools can pull
// them via runtime.GetEffectiveUserID + middlewares.GetThreadID.
//
// Missing X-User-Id falls back to runtime.DefaultUserID — that matches
// the CLI's "single user" semantics so dev mode just works without a
// gateway auth layer in front.
func userContextMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := c.GetHeader(headerUserID)
		if uid == "" {
			uid = runtime.DefaultUserID
		}
		tid := c.Param("tid")
		ctx := runtime.WithUserID(c.Request.Context(), uid)
		if tid != "" {
			ctx = middlewares.WithThreadID(ctx, tid)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
