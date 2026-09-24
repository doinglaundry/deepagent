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
	Message *schema.Message
	Meta    []byte
}

func (i Input) MarshalJSON() ([]byte, error) {
	var encoded bytes.Buffer
	if i.Meta != nil {
		if err := gob.NewEncoder(&encoded).Encode(&i.Meta); err != nil {
			return nil, err
		}
	}
	return json.Marshal(persistedInput{Message: i.Message, Meta: encoded.Bytes()})
}
func (i *Input) UnmarshalJSON(raw []byte) error {
	var persisted persistedInput
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return err
	}
	var meta any
	if len(persisted.Meta) > 0 {
		if err := gob.NewDecoder(bytes.NewReader(persisted.Meta)).Decode(&meta); err != nil {
			return err
		}
	}
	i.Message = persisted.Message
	i.Meta = meta
	return nil
}
