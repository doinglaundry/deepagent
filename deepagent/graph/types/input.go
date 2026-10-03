package types

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"github.com/cloudwego/eino/schema"
)

// Input keeps the original multimodal message and its caller-owned identity metadata.
type Input struct {
	MessageID string
	Message   *schema.Message
	Meta      any
}

// AppendInputs keeps delivery identity stable across checkpoint restoration.
// Anonymous inputs have no identity and are always appended.
func AppendInputs(existing []Input, incoming ...Input) []Input {
	seen := make(map[string]bool, len(existing))
	for _, input := range existing {
		if input.MessageID != "" {
			seen[input.MessageID] = true
		}
	}
	for _, input := range incoming {
		if input.MessageID != "" && seen[input.MessageID] {
			continue
		}
		existing = append(existing, input)
		if input.MessageID != "" {
			seen[input.MessageID] = true
		}
	}
	return existing
}

// Metadata remains typed across checkpoint restore (in particular the
// ThreadHost's map[string]string and 64-bit message identifiers).
func init() { gob.Register(map[string]string{}); gob.Register(map[string]any{}); gob.Register([]any{}) }

type persistedInput struct {
	MessageID string
	Message   *schema.Message
	Meta      []byte
}

func (input Input) MarshalJSON() ([]byte, error) {
	var encoded bytes.Buffer
	if input.Meta != nil {
		err := gob.NewEncoder(&encoded).Encode(&input.Meta)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(persistedInput{MessageID: input.MessageID, Message: input.Message, Meta: encoded.Bytes()})
}
func (input *Input) UnmarshalJSON(raw []byte) error {
	var persisted persistedInput
	err := json.Unmarshal(raw, &persisted)
	if err != nil {
		return err
	}
	var metadata any
	if len(persisted.Meta) > 0 {
		err = gob.NewDecoder(bytes.NewReader(persisted.Meta)).Decode(&metadata)
		if err != nil {
			return err
		}
	}
	input.MessageID = persisted.MessageID
	input.Message = persisted.Message
	input.Meta = metadata
	return nil
}
func CopyMessage(message *schema.Message) *schema.Message {
	if message == nil {
		return nil
	}
	encodedMessage, err := json.Marshal(message)
	if err != nil {
		return nil
	}
	var copiedMessage schema.Message
	if json.Unmarshal(encodedMessage, &copiedMessage) != nil {
		return nil
	}
	return &copiedMessage
}
