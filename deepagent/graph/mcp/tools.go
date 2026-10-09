package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

type mcpTool struct {
	client     *mcpClient
	remoteName string
	info       *schema.ToolInfo
}

func (mcpTool *mcpTool) Info(context.Context) (*schema.ToolInfo, error) { return mcpTool.info, nil }
func (mcpTool *mcpTool) InvokableRun(ctx context.Context, argumentsJSON string, _ ...tool.Option) (string, error) {
	var arguments map[string]any
	decodeErr := json.Unmarshal([]byte(argumentsJSON), &arguments)
	if decodeErr != nil {
		return "", decodeErr
	}
	callResult, err := mcpTool.client.sendRequest(ctx, "tools/call", map[string]any{"name": mcpTool.remoteName, "arguments": arguments}, false)
	if err != nil {
		return "", err
	}
	var callStatus struct {
		IsError bool `json:"isError"`
	}
	err = json.Unmarshal(callResult, &callStatus)
	if err != nil {
		return "", err
	}
	if callStatus.IsError {
		return "", fmt.Errorf("MCP tool error: %s", callResult)
	}
	return string(callResult), nil
}

var toolNameCleaner = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func LoadMCP(ctx context.Context, mcpConfigs []MCPConfig) (toolDescriptors []agentmodel.ToolDescriptor, err error) {
	seenToolNames := map[string]bool{}

	var mcpClients []*mcpClient
	defer func() {
		retainedClients := make(map[*mcpClient]bool)
		if err == nil {
			for _, toolDescriptor := range toolDescriptors {
				loadedTool, ok := toolDescriptor.Tool.(*mcpTool)
				if ok {
					retainedClients[loadedTool.client] = true
				}
			}
		}
		for _, mcpClient := range mcpClients {
			if !retainedClients[mcpClient] {
				mcpClient.close()
			}
		}
	}()
	for _, mcpConfig := range mcpConfigs {
		endpointURL, operationErr := url.Parse(mcpConfig.URL)
		if operationErr != nil || endpointURL.Host == "" || (endpointURL.Scheme != "http" && endpointURL.Scheme != "https") || mcpConfig.Name == "" {
			return toolDescriptors, fmt.Errorf("MCP name and HTTP(S) URL required")
		}
		timeout := time.Duration(mcpConfig.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		httpClient := mcpConfig.HTTPClient
		if httpClient == nil {
			httpClient = &http.Client{Timeout: timeout}
		}
		mcpClient := &mcpClient{config: mcpConfig, client: httpClient}
		mcpClients = append(mcpClients, mcpClient)
		initializeResponse, operationErr := mcpClient.sendRequest(ctx, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "deepagent-worker", "version": "1.0"}}, false)
		if operationErr != nil {
			return toolDescriptors, operationErr
		}
		var initializeResult struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		operationErr = json.Unmarshal(initializeResponse, &initializeResult)
		if operationErr != nil {
			return toolDescriptors, operationErr
		}
		mcpClient.version = initializeResult.ProtocolVersion
		_, operationErr = mcpClient.sendRequest(ctx, "notifications/initialized", nil, true)
		if operationErr != nil {
			return toolDescriptors, operationErr
		}
		cursor := ""
		seenCursors := map[string]bool{}
		for {
			parameters := map[string]any{}
			if cursor != "" {
				parameters["cursor"] = cursor
			}
			toolListResponse, operationErr := mcpClient.sendRequest(ctx, "tools/list", parameters, false)
			if operationErr != nil {
				return toolDescriptors, operationErr
			}
			var toolList struct {
				Tools []struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					InputSchema json.RawMessage `json:"inputSchema"`
					Annotations struct {
						ReadOnly bool `json:"readOnlyHint"`
					} `json:"annotations"`
				} `json:"tools"`
				NextCursor string `json:"nextCursor"`
			}
			operationErr = json.Unmarshal(toolListResponse, &toolList)
			if operationErr != nil {
				return toolDescriptors, operationErr
			}
			for _, remoteToolConfig := range toolList.Tools {
				toolName := toolNameCleaner.ReplaceAllString("mcp_"+mcpConfig.Name+"_"+remoteToolConfig.Name, "_")
				if seenToolNames[toolName] {
					return toolDescriptors, fmt.Errorf("duplicate MCP tool name %q", toolName)
				}
				seenToolNames[toolName] = true
				var parameters jsonschema.Schema
				operationErr = json.Unmarshal(remoteToolConfig.InputSchema, &parameters)
				if operationErr != nil {
					return toolDescriptors, operationErr
				}
				toolDescriptors = append(toolDescriptors, agentmodel.ToolDescriptor{
					Tool:     &mcpTool{client: mcpClient, remoteName: remoteToolConfig.Name, info: &schema.ToolInfo{Name: toolName, Desc: remoteToolConfig.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&parameters)}},
					ReadOnly: remoteToolConfig.Annotations.ReadOnly, RequiresApproval: !remoteToolConfig.Annotations.ReadOnly,
				})
			}
			cursor = toolList.NextCursor
			if cursor == "" {
				break
			}
			if seenCursors[cursor] {
				return toolDescriptors, fmt.Errorf("MCP tools/list repeated pagination cursor")
			}
			seenCursors[cursor] = true
		}
	}
	return toolDescriptors, nil
}
func CloseMCP(toolDescriptors []agentmodel.ToolDescriptor) {
	for _, toolDescriptor := range toolDescriptors {
		loadedTool, ok := toolDescriptor.Tool.(*mcpTool)
		if ok {
			loadedTool.client.close()
		}
	}
}
