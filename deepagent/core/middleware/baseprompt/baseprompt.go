package baseprompt
import "eino-cli/deepagent/core/middleware"
type Middleware = middleware.BasePromptMiddleware
func New(prompt string) middleware.Middleware { return middleware.NewBasePromptMiddleware(prompt) }
