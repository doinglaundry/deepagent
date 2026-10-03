package types

import "encoding/json"

type Interrupt struct {
	InterruptID  string
	CallID       string
	CheckpointID string
	Kind         string
	Data         json.RawMessage
}
type RequestUserInputOption struct{ Label, Description string }
type RequestUserInputQuestion struct {
	ID, Header, Question string
	Options              []RequestUserInputOption
}
type RequestUserInputInfo struct{ Questions []RequestUserInputQuestion }
type RequestUserInputAnswer struct{ Answers []string }
type RequestUserInputResponse struct {
	Answers map[string]RequestUserInputAnswer
}
