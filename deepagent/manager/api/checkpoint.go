package api

import "context"

// FencedCheckpointStore binds an execution checkpoint mutation to its live thread
// permit. Independent memory-tool checkpoints can use Manager.PutCheckpoint.
type FencedCheckpointStore interface {
	PutThreadCheckpoint(context.Context, Permit, string, []byte) error
}
