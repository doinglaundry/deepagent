package deepagents

import "eino-cli/deepagent/core/graph"

type RunOptions = graph.RunOptions
type RunOptionFunc = graph.RunOptionFunc

var WithCallbacks = graph.WithCallbacks
var WithCheckpointID = graph.WithCheckpointID
var WithWriteToCheckpointID = graph.WithWriteToCheckpointID
var WithForceNewRun = graph.WithForceNewRun
var WithResume = graph.WithResume
var WithResumeData = graph.WithResumeData
