package types

import (
	"bytes"
	"encoding/gob"
	"encoding/json"

	"github.com/cloudwego/eino/schema"
)

// Metadata remains typed across checkpoint restore (in particular the
// ThreadHost's map[string]string and 64-bit message identifiers).
func init() { gob.Register(map[string]string{}); gob.Register(map[string]any{}); gob.Register([]any{}) }

type persistedInput struct {
	MessageID string
	Message   *schema.Message
	Meta      []byte
}

func (i Input) MarshalJSON() ([]byte, error) {
	var encoded bytes.Buffer
	if i.Meta != nil {
		err := gob.NewEncoder(&encoded).Encode(&i.Meta)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(persistedInput{MessageID: i.MessageID, Message: i.Message, Meta: encoded.Bytes()})
}
func (i *Input) UnmarshalJSON(raw []byte) error {
	var persisted persistedInput
	err := json.Unmarshal(raw, &persisted)
	if err != nil {
		return err
	}
	var meta any
	if len(persisted.Meta) > 0 {
		err = gob.NewDecoder(bytes.NewReader(persisted.Meta)).Decode(&meta)
		if err != nil {
			return err
		}
	}
	i.MessageID = persisted.MessageID
	i.Message = persisted.Message
	i.Meta = meta
	return nil
}
