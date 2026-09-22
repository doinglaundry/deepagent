package distributed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// MCPConfig connects a Streamable HTTP MCP endpoint. Stdio commands are not run
// implicitly; use an explicit HTTP bridge for local stdio servers.
type MCPConfig struct {
	Name           string            `yaml:"name"`
	URL            string            `yaml:"url"`
	Headers        map[string]string `yaml:"headers"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	HTTPClient     *http.Client      `yaml:"-"`
}
type mcpClient struct {
	config           MCPConfig
	client           *http.Client
	mu               sync.Mutex
	session, version string
	nextID           int64
	closeOnce        sync.Once
}
type rpcEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *mcpClient) request(ctx context.Context, method string, params any, notification bool) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if !notification {
		body["id"] = id
	}
	if params != nil {
		body["params"] = params
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.URL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	for k, v := range c.config.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	if c.version != "" {
		req.Header.Set("MCP-Protocol-Version", c.version)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP %s: HTTP %d", method, resp.StatusCode)
	}
	if session := resp.Header.Get("Mcp-Session-Id"); session != "" {
		c.session = session
	}
	if notification {
		return nil, nil
	}
	decode := func(data []byte) (json.RawMessage, bool, error) {
		var e rpcEnvelope
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, false, err
		}
		var got int64
		if json.Unmarshal(e.ID, &got) != nil || got != id {
			return nil, false, nil
		}
		if e.Error != nil {
			return nil, true, fmt.Errorf("MCP %s (%d): %s", method, e.Error.Code, e.Error.Message)
		}
		return e.Result, true, nil
	}
	reader := io.LimitReader(resp.Body, 16<<20)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 16<<20)
		var event bytes.Buffer
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if event.Len() > 0 {
					result, matched, err := decode(event.Bytes())
					event.Reset()
					if err != nil || matched {
						return result, err
					}
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				if event.Len() > 0 {
					event.WriteByte('\n')
				}
				event.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if scanner.Err() != nil {
			return nil, scanner.Err()
		}
		if event.Len() > 0 {
			if result, matched, err := decode(event.Bytes()); err != nil || matched {
				return result, err
			}
		}
		return nil, fmt.Errorf("MCP stream ended without response")
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	result, matched, err := decode(payload)
	if err == nil && !matched {
		return nil, fmt.Errorf("MCP response identifier mismatch")
	}
	return result, err
}

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
	if err := json.Unmarshal([]byte(args), &arguments); err != nil {
		return "", err
	}
	result, err := t.client.request(ctx, "tools/call", map[string]any{"name": t.remoteName, "arguments": arguments}, false)
	if err != nil {
		return "", err
	}
	var status struct {
		IsError bool `json:"isError"`
	}
	if err = json.Unmarshal(result, &status); err != nil {
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
	defer func() {
		if err != nil {
			CloseMCP(result)
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
		init, e := client.request(ctx, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "deepagent-worker", "version": "1.0"}}, false)
		if e != nil {
			return result, e
		}
		var initialized struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if e = json.Unmarshal(init, &initialized); e != nil {
			return result, e
		}
		client.version = initialized.ProtocolVersion
		if _, e = client.request(ctx, "notifications/initialized", nil, true); e != nil {
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
			if e = json.Unmarshal(data, &list); e != nil {
				return result, e
			}
			for _, remote := range list.Tools {
				name := toolNameCleaner.ReplaceAllString("mcp_"+config.Name+"_"+remote.Name, "_")
				if names[name] {
					return result, fmt.Errorf("duplicate MCP tool name %q", name)
				}
				names[name] = true
				var params jsonschema.Schema
				if e = json.Unmarshal(remote.InputSchema, &params); e != nil {
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
		if !ok {
			continue
		}
		c := t.client
		c.closeOnce.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.session == "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.config.URL, nil)
			if err != nil {
				return
			}
			for k, v := range c.config.Headers {
				req.Header.Set(k, v)
			}
			req.Header.Set("Mcp-Session-Id", c.session)
			if c.version != "" {
				req.Header.Set("MCP-Protocol-Version", c.version)
			}
			if resp, err := c.client.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		})
	}
}
