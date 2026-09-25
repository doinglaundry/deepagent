package checkpointer

import "encoding/json"

// checkpointValue mirrors the Eino checkpoint JSON fields inspected during resume.
type checkpointValue struct {
	Type        json.RawMessage             `json:",omitempty"`
	JSONValue   json.RawMessage             `json:",omitempty"`
	MapValues   map[string]*checkpointValue `json:",omitempty"`
	SliceValues []*checkpointValue          `json:",omitempty"`
}
