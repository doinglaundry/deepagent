package deepagents

import "eino-cli/deepagent/core/graph"

type Config = graph.Config
type Option = graph.Option
type HITLConfig = graph.HITLConfig
type FilesystemConfig = graph.FilesystemConfig

var WithConfig = graph.WithConfig
var WithCustomGraphState = graph.WithCustomGraphState
var WithSubAgentSharedCustomState = graph.WithSubAgentSharedCustomState
var WithModel = graph.WithModel
var WithMaxSteps = graph.WithMaxSteps
var WithMaxModelCalls = graph.WithMaxModelCalls
var WithTools = graph.WithTools
var WithToolMask = graph.WithToolMask
var WithSubAgents = graph.WithSubAgents
var WithSubAgentsDir = graph.WithSubAgentsDir
var WithSubAgentsDirs = graph.WithSubAgentsDirs
var WithSubAgentContextInjector = graph.WithSubAgentContextInjector
var WithSubAgentTaskStreaming = graph.WithSubAgentTaskStreaming
var WithSkillLoader = graph.WithSkillLoader
var WithPlanMiddleware = graph.WithPlanMiddleware
var WithFilesystem = graph.WithFilesystem
var WithFilesystemConfig = graph.WithFilesystemConfig
var WithDisableSubAgent = graph.WithDisableSubAgent
var WithDisableUploadDownload = graph.WithDisableUploadDownload
var WithDisableExecute = graph.WithDisableExecute
var WithPatchToolCalls = graph.WithPatchToolCalls
var WithStreamToolCall = graph.WithStreamToolCall
var WithWeb = graph.WithWeb
var WithWebConfig = graph.WithWebConfig
var WithMiddleware = graph.WithMiddleware
var WithHooks = graph.WithHooks
var WithDefaultCallbacks = graph.WithDefaultCallbacks
var WithCheckpointStore = graph.WithCheckpointStore
var WithInterruptBeforeNodes = graph.WithInterruptBeforeNodes
var WithInterruptAfterNodes = graph.WithInterruptAfterNodes
var WithAllFeatures = graph.WithAllFeatures
var WithHITLConfig = graph.WithHITLConfig
var WithToolInfoRewriter = graph.WithToolInfoRewriter
var WithToolNodePreHandler = graph.WithToolNodePreHandler
var WithToolNodePostHandler = graph.WithToolNodePostHandler
