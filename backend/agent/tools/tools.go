package tools

import (
	"github.com/cloudwego/eino/components/tool"
)

// Shared output literals reused by ls / glob / grep so the wire format
// matches eino's filesystem implementation byte-for-byte.
const (
	noFilesFound   = "No files found"
	noMatchesFound = "No matches found"
)

// BuildBuiltinTools returns the fixed built-in tool set rooted at root.
// There is no per-agent filtering: every agent that opts into
// ToolsConfig.Tools gets all tools. The caller decides what root means
// (cfg.RootDir today, a subagent sandbox dir later) — this package never
// reaches for os.Getwd or any other process state on its own.
func BuildBuiltinTools(root string) []tool.BaseTool {
	return []tool.BaseTool{
		mustBuild(GetAskClarificationTool()),
		mustBuild(GetLsTool(root)),
		mustBuild(GetReadFileTool(root)),
		mustBuild(GetWriteFileTool(root)),
		mustBuild(GetEditFileTool(root)),
		mustBuild(GetGlobTool(root)),
		mustBuild(GetGrepTool(root)),
		mustBuild(GetExecuteTool(root)),
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
