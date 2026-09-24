package types

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
