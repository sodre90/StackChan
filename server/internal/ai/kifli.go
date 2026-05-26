/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const kifliMCPEndpoint = "https://mcp.kifli.hu/mcp"

// kifliCuratedTools is the subset of kifli MCP tools we expose to the LLM.
// The full server offers ~50 tools; exposing all of them bloats the prompt and
// confuses model tool selection. This list covers the voice-assistant use cases
// (cart, orders, discounts, basic product search) and intentionally omits
// shopping-list, recipe, claim, checkout-flow, and karma/feedback tools.
var kifliCuratedTools = map[string]bool{
	"batch_search_products": true,
	"get_product_details":   true,
	"get_discounted_items":  true,
	"get_announcements":     true,
	"get_cart":              true,
	"add_items_to_cart":     true,
	"update_cart_item":      true,
	"remove_cart_item":      true,
	"clear_cart":            true,
	"fetch_orders":          true,
	"get_typical_order":     true,
	"repeat_order":          true,
	"cancel_order":          true,
	"get_user_info":         true,
}

// kifliClient speaks JSON-RPC 2.0 over the MCP streamable-HTTP transport to
// https://mcp.kifli.hu/mcp. The server accepts a single POST per request and
// returns either application/json or text/event-stream; we handle both.
type kifliClient struct {
	endpoint string
	email    string
	password string
	http     *http.Client
	nextID   atomic.Int64
}

func newKifliClient(email, password string) *kifliClient {
	return &kifliClient{
		endpoint: kifliMCPEndpoint,
		email:    email,
		password: password,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// rpc sends a single JSON-RPC request and returns the parsed result field.
// Notifications (no id) return (nil, nil) on success.
func (k *kifliClient) rpc(ctx context.Context, method string, params interface{}, notify bool) (json.RawMessage, error) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if !notify {
		req["id"] = k.nextID.Add(1)
	}
	if params != nil {
		req["params"] = params
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal rpc: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", k.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set("rhl-email", k.email)
	httpReq.Header.Set("rhl-pass", k.password)

	resp, err := k.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("kifli rpc %s: %w", method, err)
	}
	defer resp.Body.Close()

	if notify {
		// Notifications get an HTTP 202 with no body; drain and return.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("kifli rpc %s: HTTP %d: %s", method, resp.StatusCode, string(b))
	}

	payload, err := readMCPResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("kifli %s error %d: %s", method, env.Error.Code, env.Error.Message)
	}
	return env.Result, nil
}

// readMCPResponse handles both application/json and text/event-stream replies.
// SSE frames look like:
//
//	event: message
//	data: {"jsonrpc":"2.0",...}
//
// We concatenate consecutive data: lines belonging to the first message frame
// and return that JSON payload.
func readMCPResponse(resp *http.Response) ([]byte, error) {
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		return io.ReadAll(resp.Body)
	}
	if !strings.HasPrefix(ct, "text/event-stream") {
		// Best-effort: try to parse raw body as JSON anyway.
		return io.ReadAll(resp.Body)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				return []byte(data.String()), nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if data.Len() == 0 {
		return nil, fmt.Errorf("empty SSE response")
	}
	return []byte(data.String()), nil
}

// initialize performs the MCP handshake and sends notifications/initialized.
func (k *kifliClient) initialize(ctx context.Context) error {
	params := map[string]interface{}{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "stackchan",
			"version": "1.0",
		},
	}
	if _, err := k.rpc(ctx, "initialize", params, false); err != nil {
		return err
	}
	if _, err := k.rpc(ctx, "notifications/initialized", nil, true); err != nil {
		// Notification errors are non-fatal; the server may not require it.
		return nil
	}
	return nil
}

// listTools fetches the tool catalog from the kifli MCP server.
func (k *kifliClient) listTools(ctx context.Context) ([]kifliToolDef, error) {
	raw, err := k.rpc(ctx, "tools/list", map[string]interface{}{}, false)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []kifliToolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return out.Tools, nil
}

type kifliToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// callTool invokes a kifli MCP tool and returns the textual result, ready to
// hand back to the LLM as a tool message.
func (k *kifliClient) callTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	if args == nil {
		args = map[string]interface{}{}
	}
	raw, err := k.rpc(ctx, "tools/call", map[string]interface{}{
		"name":      name,
		"arguments": args,
	}, false)
	if err != nil {
		return "", err
	}

	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode tool result: %w", err)
	}

	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" && c.Text != "" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(c.Text)
		}
	}
	if out.IsError {
		return "", fmt.Errorf("kifli tool error: %s", sb.String())
	}
	if sb.Len() == 0 {
		return string(raw), nil
	}
	return sb.String(), nil
}

// registerKifliTools initializes the kifli client, lists tools, and registers
// the curated subset as MCPTools. Failures are logged but never fatal — the
// server must still come up even if kifli is unreachable.
func (m *MCPManager) registerKifliTools(cfg Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := newKifliClient(cfg.KifliEmail, cfg.KifliPassword)
	if err := client.initialize(ctx); err != nil {
		logger.Warningf(ctx, "kifli MCP initialize failed, skipping kifli tools: %v", err)
		return
	}

	tools, err := client.listTools(ctx)
	if err != nil {
		logger.Warningf(ctx, "kifli tools/list failed, skipping kifli tools: %v", err)
		return
	}

	m.kifli = client
	registered := 0
	for _, t := range tools {
		if !kifliCuratedTools[t.Name] {
			continue
		}
		params := t.InputSchema
		if params == nil {
			params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		// Capture loop var for the handler closure.
		toolName := t.Name
		desc := t.Description
		if desc == "" {
			desc = "Kifli.hu tool: " + toolName
		} else {
			desc = "[kifli.hu] " + desc
		}
		m.RegisterTool(MCPTool{
			Name:        "kifli." + toolName,
			Description: desc,
			Parameters:  params,
			Handler: func(ctx context.Context, _ *AIClient, args map[string]interface{}) (string, error) {
				return m.kifli.callTool(ctx, toolName, args)
			},
		})
		registered++
	}
	logger.Infof(ctx, "kifli MCP tools registered: %d/%d (from %d available)", registered, len(kifliCuratedTools), len(tools))
}
