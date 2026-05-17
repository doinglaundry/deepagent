package gateway

import (
	"github.com/gin-gonic/gin"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/runtime"
)

const (
	headerUserID = "X-User-Id"
)

// userContextMiddleware stamps UserID and ThreadID onto the request's ctx.
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
