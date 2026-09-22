package contextmanager

import "eino-cli/deepagent/core/middlewares"

func New() middleware.Middleware { return middleware.NewSimpleContextManager() }
