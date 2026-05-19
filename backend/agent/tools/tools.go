package tools

import (
	"github.com/cloudwego/eino/components/tool"

	"eino-cli/backend/config"
)

// BuildBuiltinTools returns the fixed built-in tool set; root is read
// from cfg.RootDir (single config.Config source — see AGENTS.md "Pass
// less data"). Optional tools (web_search) are appended only when their
// yaml flag is on, so the LLM does not see a tool that would always
// error out.
func BuildBuiltinTools(cfg *config.Config) []tool.BaseTool {
	root := cfg.RootDir
	tools := []tool.BaseTool{
		mustBuild(GetAskClarificationTool()),
		mustBuild(GetLsTool(root)),
		mustBuild(GetReadFileTool(root)),
		mustBuild(GetWriteFileTool(root)),
		mustBuild(GetEditFileTool(root)),
		mustBuild(GetGlobTool(root)),
		mustBuild(GetGrepTool(root)),
		mustBuild(GetExecuteTool(root)),
		mustBuild(GetApplyPatchTool(root)),
		mustBuild(GetDeleteFileTool(root)),
		mustBuild(GetRgTool(root)),
		mustBuild(GetSemanticSearchTool(root)),
		mustBuild(GetReadLintsTool(root)),
		mustBuild(GetShellTool(root)),
		mustBuild(GetAwaitShellTool(root)),
	}
	if cfg.WebSearch.Enabled {
		tools = append(tools, mustBuild(GetWebSearchTool(cfg)))
	}
	return tools
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
