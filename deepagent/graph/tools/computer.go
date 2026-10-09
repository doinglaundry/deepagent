package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"eino-cli/deepagent/graph/computer"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const ComputerPrompt = "Browser and Mac tools return a screenshot and observation_id. Observe before acting; use the latest observation_id and element_id, or screenshot coordinates. Every browser call must include its URL; every Mac call must include its app bundle ID. On stale_observation, observe again. Computer tools are for this root agent only. Never retry an action whose outcome is unknown."

func NewBrowserTools(browser *computer.Browser) []agentmodel.ToolDescriptor {
	return newScreenTools("browser", "open", browser.PerformAction)
}
func NewComputerTools(desktop *computer.Desktop) []agentmodel.ToolDescriptor {
	return newScreenTools("computer", "open_app", func(ctx context.Context, operation string, action agentmodel.ComputerAction) (*agentmodel.ComputerObservation, error) {
		return desktop.PerformAction(ctx, agentmodel.GetRunState(ctx).RunID, operation, action)
	})
}

// screenTool only adapts real operations to Eino's existing image result interface.
type screenTool struct {
	name      string
	operation string
	params    map[string]*schema.ParameterInfo
	perform   func(context.Context, string, agentmodel.ComputerAction) (*agentmodel.ComputerObservation, error)
}

func newScreenTools(prefix, openOperation string, perform func(context.Context, string, agentmodel.ComputerAction) (*agentmodel.ComputerObservation, error)) []agentmodel.ToolDescriptor {
	var descriptors []agentmodel.ToolDescriptor
	for _, operation := range []string{openOperation, "observe", "click", "type_text", "press_key", "scroll"} {
		target := "app"
		if prefix == "browser" {
			target = "url"
		}
		params := map[string]*schema.ParameterInfo{target: {Type: schema.String, Required: true, Desc: "Exact current URL or application bundle ID"}}
		if operation != openOperation && operation != "observe" {
			params["observation_id"] = &schema.ParameterInfo{Type: schema.String, Required: true, Desc: "ID from the latest observation"}
		}
		if operation == "click" || operation == "type_text" {
			params["element_id"] = &schema.ParameterInfo{Type: schema.Integer, Desc: "Visible element ID from the observation"}
		}
		if operation == "click" {
			params["x"] = &schema.ParameterInfo{Type: schema.Number, Desc: "Screenshot X when element_id is unavailable"}
			params["y"] = &schema.ParameterInfo{Type: schema.Number, Desc: "Screenshot Y when element_id is unavailable"}
		}
		if operation == "type_text" {
			params["text"] = &schema.ParameterInfo{Type: schema.String, Required: true}
		}
		if operation == "press_key" {
			params["key"] = &schema.ParameterInfo{Type: schema.String, Required: true, Desc: "Enter, Tab, Escape, Backspace, ArrowUp/Down/Left/Right; Mac also supports Command+A/C/V/S/Z/N"}
		}
		if operation == "scroll" {
			params["delta_x"] = &schema.ParameterInfo{Type: schema.Integer}
			params["delta_y"] = &schema.ParameterInfo{Type: schema.Integer, Desc: "Positive scrolls down"}
		}
		descriptors = append(descriptors, agentmodel.ToolDescriptor{Tool: &screenTool{name: prefix + "_" + operation, operation: operation, params: params, perform: perform}, ReadOnly: operation == "observe", RequiresApproval: operation != "observe"})
	}
	return descriptors
}
func (screenTool *screenTool) Info(context.Context) (*schema.ToolInfo, error) {
	return newToolInfo(screenTool.name, "Perform "+screenTool.operation+" and return the current screen and visible elements.", screenTool.params)
}
func (screenTool *screenTool) InvokableRun(ctx context.Context, argument *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
	runState := agentmodel.GetRunState(ctx)
	if runState == nil || runState.Depth != 0 || runState.RunID == "" {
		return nil, errors.New("computer tools require a root Run")
	}
	if argument == nil {
		return nil, errors.New("tool arguments are required")
	}
	var action agentmodel.ComputerAction
	err := json.Unmarshal([]byte(argument.Text), &action)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(screenTool.name, "browser_") && action.URL == "" || strings.HasPrefix(screenTool.name, "computer_") && action.App == "" {
		return nil, errors.New("URL or app bundle ID is required")
	}
	observation, err := screenTool.perform(ctx, screenTool.operation, action)
	if err != nil {
		if errors.Is(err, computer.ErrOutcomeUnknown) {
			return nil, &agentmodel.InternalError{Err: err}
		}
		return nil, err
	}
	image := observation.Image
	snapshot := *observation
	snapshot.Image = ""
	text, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	result := &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: string(text)}}}
	if image != "" {
		result.Parts = append(result.Parts, schema.ToolOutputPart{Type: schema.ToolPartTypeImage, Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &image, MIMEType: "image/png"}}})
	}
	return result, nil
}
