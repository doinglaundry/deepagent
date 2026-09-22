package thread

import (
	"testing"

	inputpkg "eino-cli/deepagent/protocol/input"
)

func TestManagerMessageIDSurvivesInputDecoding(t *testing.T) {
	command, err := decodeUserInputCommand(&TransportMessage{
		ID:      "2000000000000000042",
		Type:    MessageTypeInput,
		Payload: []byte(`{"parts":[{"type":"text","text":"hello"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := MessageID(command.schema); got != "2000000000000000042" {
		t.Fatalf("message id = %q", got)
	}
	if command.input.Parts[0].Type != inputpkg.MessagePartTypeText {
		t.Fatalf("unexpected input: %+v", command.input)
	}
}

func TestInputDecodingPreservesMultimediaParts(t *testing.T) {
	command, err := decodeUserInputCommand(&TransportMessage{
		ID:   "43",
		Type: MessageTypeInput,
		Payload: []byte(`{"parts":[
			{"type":"text","text":"describe"},
			{"type":"image","url":"https://example.test/photo.png","mime_type":"image/png"}
		]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := command.schema.UserInputMultiContent
	if len(parts) != 2 || parts[1].Image == nil || parts[1].Image.URL == nil || *parts[1].Image.URL != "https://example.test/photo.png" {
		t.Fatalf("multimedia input was flattened: %+v", command.schema)
	}
}
