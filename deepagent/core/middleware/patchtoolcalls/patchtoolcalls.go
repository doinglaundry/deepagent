package patchtoolcalls
import "eino-cli/deepagent/core/middleware"
func New() middleware.Middleware { return &middleware.BaseMiddleware{} }
func PatchDanglingToolCalls[T any](in []T) []T { return in }
