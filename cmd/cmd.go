package cmd

import "eino-cli/internal/app"

var knownCLICommands = []string{
	"/bootstrap",
	"/new",
	"/status",
	"/models",
	"/memory",
	"/help",
}

func Run() error {
	application, err := app.New(app.Options{KnownCommands: knownCLICommands})
	if err != nil {
		return err
	}

	return application.Run()
}
