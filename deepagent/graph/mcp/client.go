package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
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
	getSession := resp.Header.Get("Mcp-Session-Id")
	if getSession != "" {
		c.session = getSession
	}
	if notification {
		return nil, nil
	}
	decode := func(data []byte) (json.RawMessage, bool, error) {
		var e rpcEnvelope
		err := json.Unmarshal(data, &e)
		if err != nil {
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
			result, matched, err := decode(event.Bytes())
			if err != nil || matched {
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

func (c *mcpClient) close() {
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
		resp, doErr := c.client.Do(req)
		if doErr == nil {
			_ = resp.Body.Close()
		}
	})
}
