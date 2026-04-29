package router

import "strings"

type InputType string

type Target string

const (
	InputTypeNaturalLanguage InputType = "natural_language"
	InputTypeSlashCommand    InputType = "slash_command"

	TargetAgent   Target = "agent"
	TargetCommand Target = "command"
)

type Route struct {
	RawInput    string
	InputType   InputType
	Target      Target
	CommandName string
	Args        []string
}

type Parser struct{}

func New() *Parser {
	return &Parser{}
}

func (p *Parser) Parse(input string) Route {
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "/") {
		parts := strings.Fields(strings.TrimPrefix(trimmed, "/"))
		route := Route{RawInput: trimmed, InputType: InputTypeSlashCommand, Target: TargetCommand}
		if len(parts) > 0 {
			route.CommandName = parts[0]
		}
		if len(parts) > 1 {
			route.Args = parts[1:]
		}
		return route
	}

	return Route{RawInput: trimmed, InputType: InputTypeNaturalLanguage, Target: TargetAgent}
}
