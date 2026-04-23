package render

import (
	clistatus "eino-cli/internal/cli/status"
	"eino-cli/internal/runtime/eino"
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
	RenderStatus(clistatus.Snapshot) error
	RenderError(ErrorView) error
}
