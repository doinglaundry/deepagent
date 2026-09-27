package deepagents

import "eino-cli/deepagent/core/graph"

type Config = graph.Config
type Option = graph.Option

type FilesystemConfig = graph.FilesystemConfig

var WithConfig = graph.WithConfig

var WithModel = graph.WithModel
var WithMaxSteps = graph.WithMaxSteps
var WithMaxModelCalls = graph.WithMaxModelCalls
var WithTools = graph.WithTools
var WithToolMask = graph.WithToolMask
var WithSubAgents = graph.WithSubAgents

var WithSkillLoader = graph.WithSkillLoader
var WithPlanMiddleware = graph.WithPlanMiddleware
var WithFilesystem = graph.WithFilesystem
var WithFilesystemConfig = graph.WithFilesystemConfig

var WithDisableUploadDownload = graph.WithDisableUploadDownload
var WithDisableExecute = graph.WithDisableExecute

var WithEagerTools = graph.WithEagerTools
var WithWeb = graph.WithWeb
var WithWebConfig = graph.WithWebConfig
var WithMiddleware = graph.WithMiddleware

var WithDefaultCallbacks = graph.WithDefaultCallbacks
var WithCheckpointStore = graph.WithCheckpointStore

var WithAllFeatures = graph.WithAllFeatures

type SubAgent = graph.SubAgent

var LoadSubAgents = graph.LoadSubAgents
