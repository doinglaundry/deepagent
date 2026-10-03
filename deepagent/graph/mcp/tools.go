package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

type mcpTool struct {
	client     *mcpClient
	remoteName string
	info       *schema.ToolInfo
	readOnly   bool
}

func (t *mcpTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }
func (t *mcpTool) ReadOnly() bool                                 { return t.readOnly }
func (t *mcpTool) RequiresApproval() bool                         { return !t.readOnly }
func (t *mcpTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var arguments map[string]any
	decodeErr := json.Unmarshal([]byte(args), &arguments)
	if decodeErr != nil {
		return "", decodeErr
	}
	result, err := t.client.request(ctx, "tools/call", map[string]any{"name": t.remoteName, "arguments": arguments}, false)
	if err != nil {
		return "", err
	}
	var status struct {
		IsError bool `json:"isError"`
	}
	err = json.Unmarshal(result, &status)
	if err != nil {
		return "", err
	}
	if status.IsError {
		return "", fmt.Errorf("MCP tool error: %s", result)
	}
	return string(result), nil
}

var toolNameCleaner = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func LoadMCP(ctx context.Context, configs []MCPConfig) (result []tool.BaseTool, err error) {
	names := map[string]bool{}

	var clients []*mcpClient
	defer func() {
		retained := make(map[*mcpClient]bool)
		if err == nil {
			for _, base := range result {
				t, ok := base.(*mcpTool)
				if ok {
					retained[t.client] = true
				}
			}
		}
		for _, client := range clients {
			if !retained[client] {
				client.close()
			}
		}
	}()
	for _, config := range configs {
		endpoint, e := url.Parse(config.URL)
		if e != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || config.Name == "" {
			return result, fmt.Errorf("MCP name and HTTP(S) URL required")
		}
		timeout := time.Duration(config.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		httpClient := config.HTTPClient
		if httpClient == nil {
			httpClient = &http.Client{Timeout: timeout}
		}
		client := &mcpClient{config: config, client: httpClient}
		clients = append(clients, client)
		init, e := client.request(ctx, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "deepagent-worker", "version": "1.0"}}, false)
		if e != nil {
			return result, e
		}
		var initialized struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		e = json.Unmarshal(init, &initialized)
		if e != nil {
			return result, e
		}
		client.version = initialized.ProtocolVersion
		_, e = client.request(ctx, "notifications/initialized", nil, true)
		if e != nil {
			return result, e
		}
		cursor := ""
		seenCursors := map[string]bool{}
		for {
			params := map[string]any{}
			if cursor != "" {
				params["cursor"] = cursor
			}
			data, e := client.request(ctx, "tools/list", params, false)
			if e != nil {
				return result, e
			}
			var list struct {
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
			e = json.Unmarshal(data, &list)
			if e != nil {
				return result, e
			}
			for _, remote := range list.Tools {
				name := toolNameCleaner.ReplaceAllString("mcp_"+config.Name+"_"+remote.Name, "_")
				if names[name] {
					return result, fmt.Errorf("duplicate MCP tool name %q", name)
				}
				names[name] = true
				var params jsonschema.Schema
				e = json.Unmarshal(remote.InputSchema, &params)
				if e != nil {
					return result, e
				}
				result = append(result, &mcpTool{client: client, remoteName: remote.Name, info: &schema.ToolInfo{Name: name, Desc: remote.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&params)}, readOnly: remote.Annotations.ReadOnly})
			}
			cursor = list.NextCursor
			if cursor == "" {
				break
			}
			if seenCursors[cursor] {
				return result, fmt.Errorf("MCP tools/list repeated pagination cursor")
			}
			seenCursors[cursor] = true
		}
	}
	return result, nil
}
func CloseMCP(tools []tool.BaseTool) {
	for _, base := range tools {
		t, ok := base.(*mcpTool)
		if ok {
			t.client.close()
		}
	}
}
