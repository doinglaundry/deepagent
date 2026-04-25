package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"eino-cli/internal/config"
	"eino-cli/internal/tools"
)

type Gateway struct {
	cfg    config.PluginGatewayConfig
	client *http.Client
}

func New(cfg config.PluginGatewayConfig) *Gateway {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Gateway{cfg: cfg, client: &http.Client{Timeout: timeout}}
}

func (g *Gateway) Enabled() bool {
	return g != nil && g.cfg.Enabled
}

func (g *Gateway) Check(ctx context.Context) error {
	if !g.Enabled() {
		return nil
	}
	if strings.TrimSpace(g.cfg.Endpoint) == "" {
		return fmt.Errorf("plugin gateway endpoint is required when enabled")
	}
	_, err := g.getJSON(ctx, []string{"/health", "/v1/health", ""})
	if err != nil {
		return fmt.Errorf("plugin gateway check failed: %w", err)
	}
	return nil
}

func (g *Gateway) ListTools(ctx context.Context) ([]tools.Tool, error) {
	if !g.Enabled() {
		return []tools.Tool{}, nil
	}
	if strings.TrimSpace(g.cfg.Endpoint) == "" {
		return nil, fmt.Errorf("plugin gateway endpoint is required when enabled")
	}

	body, err := g.getJSON(ctx, []string{"/tools", "/v1/tools"})
	if err != nil {
		return nil, fmt.Errorf("list plugin tools: %w", err)
	}

	type response struct {
		Tools []tools.Tool `json:"tools"`
	}

	var direct []tools.Tool
	if err := json.Unmarshal(body, &direct); err == nil {
		return normalizeTools(direct), nil
	}

	var wrapped response
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, fmt.Errorf("decode tools response: %w", err)
	}
	return normalizeTools(wrapped.Tools), nil
}

func (g *Gateway) InvokeTool(ctx context.Context, name string, args []string) (tools.Result, error) {
	if !g.Enabled() {
		return tools.Result{}, fmt.Errorf("plugin gateway disabled")
	}
	if strings.TrimSpace(g.cfg.Endpoint) == "" {
		return tools.Result{}, fmt.Errorf("plugin gateway endpoint is required when enabled")
	}

	payload, err := json.Marshal(map[string]any{"arguments": args})
	if err != nil {
		return tools.Result{}, err
	}

	paths := []string{fmt.Sprintf("/tools/%s/invoke", strings.TrimSpace(name)), "/invoke"}
	for i, path := range paths {
		result, statusCode, callErr := g.postJSON(ctx, path, payload)
		if callErr == nil {
			if parsed, ok := parseInvokeResult(result); ok {
				return parsed, nil
			}
			return tools.Result{Output: strings.TrimSpace(string(result))}, nil
		}
		if statusCode == http.StatusNotFound && i < len(paths)-1 {
			continue
		}
		return tools.Result{}, callErr
	}

	return tools.Result{}, fmt.Errorf("invoke plugin tool %q failed", name)
}

func (g *Gateway) endpoint(path string) string {
	base := strings.TrimRight(strings.TrimSpace(g.cfg.Endpoint), "/")
	if path == "" {
		return base
	}
	return base + path
}

func (g *Gateway) getJSON(ctx context.Context, paths []string) ([]byte, error) {
	var lastErr error
	for _, path := range paths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.endpoint(path), nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := g.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return data, nil
		}
		lastErr = fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("gateway request failed")
	}
	return nil, lastErr
}

func (g *Gateway) postJSON(ctx context.Context, path string, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint(path), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, resp.StatusCode, nil
}

func normalizeTools(in []tools.Tool) []tools.Tool {
	out := make([]tools.Tool, 0, len(in))
	for _, item := range in {
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" {
			continue
		}
		if strings.TrimSpace(item.Source) == "" {
			item.Source = "plugin"
		}
		switch item.RiskLevel {
		case tools.RiskLevelLow, tools.RiskLevelMedium, tools.RiskLevelHigh:
		default:
			item.RiskLevel = tools.RiskLevelLow
		}
		out = append(out, item)
	}
	return out
}

func parseInvokeResult(body []byte) (tools.Result, bool) {
	type wrapped struct {
		Output string `json:"output"`
	}
	var direct wrapped
	if err := json.Unmarshal(body, &direct); err == nil && strings.TrimSpace(direct.Output) != "" {
		return tools.Result{Output: direct.Output}, true
	}

	type nested struct {
		Result wrapped `json:"result"`
	}
	var n nested
	if err := json.Unmarshal(body, &n); err == nil && strings.TrimSpace(n.Result.Output) != "" {
		return tools.Result{Output: n.Result.Output}, true
	}
	return tools.Result{}, false
}
