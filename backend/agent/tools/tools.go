package tools

import (
	"github.com/cloudwego/eino/components/tool"

	"eino-cli/backend/config"
	"eino-cli/backend/sandbox"
)

// BuildBuiltinTools returns the fixed built-in tool set. Optional tools (web_search) are appended only when their
// yaml flag is on, so the LLM does not see a tool that would always
// error out.
func BuildBuiltinTools(cfg *config.Config, sandboxManager sandbox.SandboxManager) []tool.BaseTool {
	tools := []tool.BaseTool{
		mustBuild(GetAskClarificationTool()),
		mustBuild(GetLsTool(sandboxManager)),
		mustBuild(GetReadFileTool(sandboxManager)),
		mustBuild(GetWriteFileTool(sandboxManager)),
		mustBuild(GetEditFileTool(sandboxManager)),
		mustBuild(GetGlobTool(sandboxManager)),
		mustBuild(GetGrepTool(sandboxManager)),
		mustBuild(GetExecuteTool(sandboxManager)),
		mustBuild(GetApplyPatchTool()),
		mustBuild(GetDeleteFileTool()),
		mustBuild(GetRgTool()),
		mustBuild(GetSemanticSearchTool()),
		mustBuild(GetReadLintsTool()),
		mustBuild(GetShellTool(sandboxManager)),
		mustBuild(GetAwaitShellTool()),
	}
	if cfg.WebSearch.Enabled {
		tools = append(tools, mustBuild(GetWebSearchTool(cfg.WebSearch)))
	}
	return tools
}

func BuildAutoDreamTools(sandboxManager sandbox.SandboxManager) []tool.BaseTool {
	memoryRoot := config.DreamMemoryDir()
	return []tool.BaseTool{
		mustBuild(GetLsTool(sandboxManager)),
		mustBuild(GetReadFileTool(sandboxManager)),
		mustBuild(GetGlobTool(sandboxManager)),
		mustBuild(GetGrepTool(sandboxManager)),
		mustBuild(GetRgTool()),
		mustBuild(GetAutoDreamShellTool(sandboxManager)),
		mustBuild(GetAutoDreamWriteFileTool(memoryRoot)),
		mustBuild(GetAutoDreamEditFileTool(memoryRoot)),
	}
}

// mustBuild collapses InferTool's (tool, err) into a single value. InferTool
// only fails when struct→schema reflection breaks, which is a compile-time
// bug — panicking at startup beats checking the same error 7 times.
func mustBuild(t tool.BaseTool, err error) tool.BaseTool {
	if err != nil {
		panic(err)
	}
	return t
}
