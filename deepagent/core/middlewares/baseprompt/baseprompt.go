package baseprompt

import "eino-cli/deepagent/core/middlewares"

type Middleware = middleware.BasePromptMiddleware

func New(prompt string) middleware.Middleware { return middleware.NewBasePromptMiddleware(prompt) }
