package graph

import "github.com/cloudwego/eino/schema"

// This private interrupt creates Eino's real execution cursor before prepare.
// execute resumes it internally, within the same Run lifecycle.
type initialCheckpoint struct{}
type initialCheckpointKey struct{}

func init() { schema.RegisterName[*initialCheckpoint]("deepagent_initial_checkpoint_v1") }
