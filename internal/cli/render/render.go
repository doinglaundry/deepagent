package render

import (
	"fmt"
	"io"
	"os"

	clistatus "eino-cli/internal/cli/status"
)

type ConsoleRenderer struct {
	out io.Writer
}

func NewConsoleRenderer(out io.Writer) *ConsoleRenderer {
	if out == nil {
		out = os.Stdout
	}
	return &ConsoleRenderer{out: out}
}

func (r *ConsoleRenderer) Render(message Message) error {
	_, err := fmt.Fprintf(r.out, "%s\n", message.Content)
	return err
}

func (r *ConsoleRenderer) RenderStatus(snapshot clistatus.Snapshot) error {
	_, err := fmt.Fprintf(r.out, "[status] %s\n", snapshot.String())
	return err
}

func (r *ConsoleRenderer) RenderError(view ErrorView) error {
	_, err := fmt.Fprintf(r.out, "[error] %s: %s\n", view.Code, view.Message)
	return err
}
