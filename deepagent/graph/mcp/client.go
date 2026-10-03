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

func (mcpClient *mcpClient) sendRequest(ctx context.Context, method string, params any, notification bool) (json.RawMessage, error) {
	mcpClient.mu.Lock()
	defer mcpClient.mu.Unlock()
	mcpClient.nextID++
	requestID := mcpClient.nextID
	requestBody := map[string]any{"jsonrpc": "2.0", "method": method}
	if !notification {
		requestBody["id"] = requestID
	}
	if params != nil {
		requestBody["params"] = params
	}
	encodedJSON, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpClient.config.URL, bytes.NewReader(encodedJSON))
	if err != nil {
		return nil, err
	}
	for headerName, headerValue := range mcpClient.config.Headers {
		httpRequest.Header.Set(headerName, headerValue)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json, text/event-stream")
	if mcpClient.session != "" {
		httpRequest.Header.Set("Mcp-Session-Id", mcpClient.session)
	}
	if mcpClient.version != "" {
		httpRequest.Header.Set("MCP-Protocol-Version", mcpClient.version)
	}
	httpResponse, err := mcpClient.client.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP %s: HTTP %d", method, httpResponse.StatusCode)
	}
	sessionID := httpResponse.Header.Get("Mcp-Session-Id")
	if sessionID != "" {
		mcpClient.session = sessionID
	}
	if notification {
		return nil, nil
	}
	decodeResponse := func(encodedJSON []byte) (json.RawMessage, bool, error) {
		var envelope rpcEnvelope
		err := json.Unmarshal(encodedJSON, &envelope)
		if err != nil {
			return nil, false, err
		}
		var responseID int64
		if json.Unmarshal(envelope.ID, &responseID) != nil || responseID != requestID {
			return nil, false, nil
		}
		if envelope.Error != nil {
			return nil, true, fmt.Errorf("MCP %s (%d): %s", method, envelope.Error.Code, envelope.Error.Message)
		}
		return envelope.Result, true, nil
	}
	responseReader := io.LimitReader(httpResponse.Body, 16<<20)
	if strings.HasPrefix(httpResponse.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(responseReader)
		scanner.Buffer(make([]byte, 4096), 16<<20)
		var eventData bytes.Buffer
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if eventData.Len() > 0 {
					responseResult, matched, err := decodeResponse(eventData.Bytes())
					eventData.Reset()
					if err != nil || matched {
						return responseResult, err
					}
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				if eventData.Len() > 0 {
					eventData.WriteByte('\n')
				}
				eventData.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if scanner.Err() != nil {
			return nil, scanner.Err()
		}
		if eventData.Len() > 0 {
			responseResult, matched, err := decodeResponse(eventData.Bytes())
			if err != nil || matched {
				return responseResult, err
			}
		}
		return nil, fmt.Errorf("MCP stream ended without response")
	}
	responsePayload, err := io.ReadAll(responseReader)
	if err != nil {
		return nil, err
	}
	responseResult, matched, err := decodeResponse(responsePayload)
	if err == nil && !matched {
		return nil, fmt.Errorf("MCP response identifier mismatch")
	}
	return responseResult, err
}

func (mcpClient *mcpClient) close() {
	mcpClient.closeOnce.Do(func() {
		mcpClient.mu.Lock()
		defer mcpClient.mu.Unlock()
		if mcpClient.session == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodDelete, mcpClient.config.URL, nil)
		if err != nil {
			return
		}
		for headerName, headerValue := range mcpClient.config.Headers {
			httpRequest.Header.Set(headerName, headerValue)
		}
		httpRequest.Header.Set("Mcp-Session-Id", mcpClient.session)
		if mcpClient.version != "" {
			httpRequest.Header.Set("MCP-Protocol-Version", mcpClient.version)
		}
		httpResponse, doErr := mcpClient.client.Do(httpRequest)
		if doErr == nil {
			_ = httpResponse.Body.Close()
		}
	})
}
