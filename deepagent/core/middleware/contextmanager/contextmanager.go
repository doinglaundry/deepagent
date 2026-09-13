package contextmanager
import "eino-cli/deepagent/core/middleware"
func New() middleware.Middleware { return middleware.NewSimpleContextManager() }
