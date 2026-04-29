package render

import (
	"eino-cli/backend/runtime/eino"
)

type Message struct {
	Kind    string
	Content string
}

type ErrorView struct {
	Code    eino.ErrorCode
	Message string
}

type Renderer interface {
	Render(Message) error
	RenderError(ErrorView) error
}
