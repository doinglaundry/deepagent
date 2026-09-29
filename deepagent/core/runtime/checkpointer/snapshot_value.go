package checkpointer

// checkpointValue contains the checkpoint JSON fields used to validate resume targets.
type checkpointValue struct {
	MapValues   map[string]*checkpointValue `json:",omitempty"`
	SliceValues []*checkpointValue          `json:",omitempty"`
}
