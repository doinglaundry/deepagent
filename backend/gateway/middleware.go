package gateway

import (
	"github.com/gin-gonic/gin"

	"eino-cli/backend/runtime"
	runtimecontext "eino-cli/backend/runtime/context"
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
			ctx = runtimecontext.WithThreadID(ctx, tid)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
