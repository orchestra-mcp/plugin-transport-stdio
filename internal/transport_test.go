package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/protocol"
	"google.golang.org/protobuf/types/known/structpb"
)

// mockSender implements the Sender interface for testing without QUIC.
type mockSender struct {
	sendFunc func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error)
}

func (m *mockSender) Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
	if m.sendFunc != nil {
		return m.sendFunc(ctx, req)
	}
	return nil, fmt.Errorf("mockSender: no sendFunc configured")
}

// runSingleRequest sends a single JSON-RPC line through a StdioTransport and
// returns the response line. If the response is a notification (no output), the
// output string will be empty.
func runSingleRequest(t *testing.T, sender Sender, requestJSON string) string {
	t.Helper()
	in := strings.NewReader(requestJSON + "\n")
	var out bytes.Buffer
	transport := NewStdioTransport(sender, in, &out)
	if err := transport.Run(context.Background()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	return strings.TrimSpace(out.String())
}

// parseJSONRPCResponse parses a JSON-RPC response string.
func parseJSONRPCResponse(t *testing.T, raw string) protocol.JSONRPCResponse {
	t.Helper()
	var resp protocol.JSONRPCResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("parse response: %v\nraw: %s", err, raw)
	}
	return resp
}

// --- Tests ---

func TestInitialize(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"claude","version":"1.0.0"}}}`

	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc: got %q, want %q", resp.JSONRPC, "2.0")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	// The result should be an MCPInitializeResult.
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var initResult protocol.MCPInitializeResult
	if err := json.Unmarshal(resultBytes, &initResult); err != nil {
		t.Fatalf("unmarshal init result: %v", err)
	}

	if initResult.ProtocolVersion != "2024-11-05" {
		t.Errorf("protocolVersion: got %q, want %q", initResult.ProtocolVersion, "2024-11-05")
	}
	if initResult.ServerInfo.Name != "orchestra" {
		t.Errorf("serverInfo.name: got %q, want %q", initResult.ServerInfo.Name, "orchestra")
	}
	if initResult.ServerInfo.Version != "1.0.0" {
		t.Errorf("serverInfo.version: got %q, want %q", initResult.ServerInfo.Version, "1.0.0")
	}
	if initResult.Capabilities.Tools == nil {
		t.Error("expected capabilities.tools to be set")
	}
}

func TestPing(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":42,"method":"ping"}`
	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	// Pong should be an empty object.
	resultBytes, _ := json.Marshal(resp.Result)
	if string(resultBytes) != "{}" {
		t.Errorf("ping result: got %s, want {}", string(resultBytes))
	}
}

func TestToolsList(t *testing.T) {
	schema, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project_id": map[string]any{"type": "string"},
			"title":      map[string]any{"type": "string"},
		},
	})

	sender := &mockSender{
		sendFunc: func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
			if req.GetListTools() == nil {
				t.Error("expected ListTools request")
			}
			return &pluginv1.PluginResponse{
				RequestId: req.RequestId,
				Response: &pluginv1.PluginResponse_ListTools{
					ListTools: &pluginv1.ListToolsResponse{
						Tools: []*pluginv1.ToolDefinition{
							{
								Name:        "create_feature",
								Description: "Create a new feature",
								InputSchema: schema,
							},
						},
					},
				},
			}, nil
		},
	}

	reqJSON := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	raw := runSingleRequest(t, sender, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	// Parse the result.
	resultBytes, _ := json.Marshal(resp.Result)
	var listResult toolsListResult
	if err := json.Unmarshal(resultBytes, &listResult); err != nil {
		t.Fatalf("unmarshal tools list result: %v", err)
	}

	if len(listResult.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(listResult.Tools))
	}
	tool := listResult.Tools[0]
	if tool.Name != "create_feature" {
		t.Errorf("tool name: got %q, want %q", tool.Name, "create_feature")
	}
	if tool.Description != "Create a new feature" {
		t.Errorf("tool description: got %q, want %q", tool.Description, "Create a new feature")
	}

	// Verify inputSchema is a proper object.
	schemaBytes, _ := json.Marshal(tool.InputSchema)
	var schemaMap map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
		t.Fatalf("parse inputSchema: %v", err)
	}
	if schemaMap["type"] != "object" {
		t.Errorf("inputSchema.type: got %v, want %q", schemaMap["type"], "object")
	}
	props, ok := schemaMap["properties"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema.properties should be object, got %T", schemaMap["properties"])
	}
	if _, ok := props["project_id"]; !ok {
		t.Error("expected project_id in properties")
	}
	if _, ok := props["title"]; !ok {
		t.Error("expected title in properties")
	}
}

func TestToolsCall(t *testing.T) {
	sender := &mockSender{
		sendFunc: func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
			tc := req.GetToolCall()
			if tc == nil {
				t.Error("expected ToolCall request")
				return nil, fmt.Errorf("expected ToolCall request")
			}
			if tc.ToolName != "create_feature" {
				t.Errorf("tool name: got %q, want %q", tc.ToolName, "create_feature")
			}
			if tc.CallerPlugin != "transport.stdio" {
				t.Errorf("caller_plugin: got %q, want %q", tc.CallerPlugin, "transport.stdio")
			}

			// Verify arguments were forwarded.
			if tc.Arguments == nil {
				t.Error("expected arguments to be set")
			} else {
				pid := tc.Arguments.GetFields()["project_id"].GetStringValue()
				if pid != "my-project" {
					t.Errorf("argument project_id: got %q, want %q", pid, "my-project")
				}
			}

			result, _ := structpb.NewStruct(map[string]any{
				"text": "Created feature FEAT-ABC in project my-project",
			})
			return &pluginv1.PluginResponse{
				RequestId: req.RequestId,
				Response: &pluginv1.PluginResponse_ToolCall{
					ToolCall: &pluginv1.ToolResponse{
						Success: true,
						Result:  result,
					},
				},
			}, nil
		},
	}

	reqJSON := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_feature","arguments":{"project_id":"my-project","title":"Add login"}}}`
	raw := runSingleRequest(t, sender, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var mcpResult protocol.MCPToolResult
	if err := json.Unmarshal(resultBytes, &mcpResult); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}

	if mcpResult.IsError {
		t.Error("expected isError=false")
	}
	if len(mcpResult.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(mcpResult.Content))
	}
	if mcpResult.Content[0].Type != "text" {
		t.Errorf("content type: got %q, want %q", mcpResult.Content[0].Type, "text")
	}
	if mcpResult.Content[0].Text != "Created feature FEAT-ABC in project my-project" {
		t.Errorf("content text: got %q", mcpResult.Content[0].Text)
	}
}

func TestToolsCallError(t *testing.T) {
	sender := &mockSender{
		sendFunc: func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
			return &pluginv1.PluginResponse{
				RequestId: req.RequestId,
				Response: &pluginv1.PluginResponse_ToolCall{
					ToolCall: &pluginv1.ToolResponse{
						Success:      false,
						ErrorCode:    "tool_not_found",
						ErrorMessage: `tool "nonexistent" not found`,
					},
				},
			}, nil
		},
	}

	reqJSON := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nonexistent","arguments":{}}}`
	raw := runSingleRequest(t, sender, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var mcpResult protocol.MCPToolResult
	if err := json.Unmarshal(resultBytes, &mcpResult); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}

	if !mcpResult.IsError {
		t.Error("expected isError=true")
	}
	if len(mcpResult.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(mcpResult.Content))
	}
	if mcpResult.Content[0].Text != `tool "nonexistent" not found` {
		t.Errorf("error text: got %q", mcpResult.Content[0].Text)
	}
}

func TestToolsCallNetworkError(t *testing.T) {
	sender := &mockSender{
		sendFunc: func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	reqJSON := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"create_feature","arguments":{}}}`
	raw := runSingleRequest(t, sender, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error for network failure")
	}
	if resp.Error.Code != protocol.InternalError {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InternalError)
	}
	if !strings.Contains(resp.Error.Message, "connection refused") {
		t.Errorf("error message should contain 'connection refused', got: %s", resp.Error.Message)
	}
}

func TestNotification(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	raw := runSingleRequest(t, &mockSender{}, reqJSON)

	// Notifications should produce no output.
	if raw != "" {
		t.Errorf("expected empty output for notification, got: %s", raw)
	}
}

func TestMethodNotFound(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":10,"method":"unknown/method"}`
	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error for unknown method")
	}
	if resp.Error.Code != protocol.MethodNotFound {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.MethodNotFound)
	}
	if !strings.Contains(resp.Error.Message, "unknown/method") {
		t.Errorf("error message should mention the method, got: %s", resp.Error.Message)
	}
}

func TestParseError(t *testing.T) {
	raw := runSingleRequest(t, &mockSender{}, `{invalid json}`)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected JSON-RPC parse error")
	}
	if resp.Error.Code != protocol.ParseError {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.ParseError)
	}
}

func TestMultipleRequests(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	}, "\n") + "\n"

	in := strings.NewReader(input)
	var out bytes.Buffer
	transport := NewStdioTransport(&mockSender{}, in, &out)
	if err := transport.Run(context.Background()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// Expect 2 responses (initialize + ping); notification produces no output.
	if len(lines) != 2 {
		t.Fatalf("expected 2 response lines, got %d: %v", len(lines), lines)
	}

	// First response should be initialize.
	resp1 := parseJSONRPCResponse(t, lines[0])
	if resp1.Error != nil {
		t.Errorf("initialize error: %+v", resp1.Error)
	}

	// Second response should be ping.
	resp2 := parseJSONRPCResponse(t, lines[1])
	if resp2.Error != nil {
		t.Errorf("ping error: %+v", resp2.Error)
	}
}

func TestToolsCallMissingName(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"arguments":{}}}`
	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected error for missing tool name")
	}
	if resp.Error.Code != protocol.InvalidParams {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InvalidParams)
	}
	if !strings.Contains(resp.Error.Message, "name") {
		t.Errorf("error message should mention 'name', got: %s", resp.Error.Message)
	}
}

func TestEmptyLines(t *testing.T) {
	// Empty lines should be skipped without error.
	input := "\n\n" + `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n\n"
	in := strings.NewReader(input)
	var out bytes.Buffer
	transport := NewStdioTransport(&mockSender{}, in, &out)
	if err := transport.Run(context.Background()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 response, got %d", len(lines))
	}
}

// --- Translator tests ---

func TestStructToMap(t *testing.T) {
	s, _ := structpb.NewStruct(map[string]any{
		"name":   "test",
		"count":  42.0,
		"active": true,
		"tags":   []any{"a", "b"},
		"nested": map[string]any{"key": "val"},
	})

	m := StructToMap(s)

	if m["name"] != "test" {
		t.Errorf("name: got %v", m["name"])
	}
	if m["count"] != 42.0 {
		t.Errorf("count: got %v", m["count"])
	}
	if m["active"] != true {
		t.Errorf("active: got %v", m["active"])
	}
	tags, ok := m["tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Errorf("tags: got %v", m["tags"])
	}
	nested, ok := m["nested"].(map[string]any)
	if !ok || nested["key"] != "val" {
		t.Errorf("nested: got %v", m["nested"])
	}
}

func TestStructToMapNil(t *testing.T) {
	m := StructToMap(nil)
	if m != nil {
		t.Errorf("expected nil, got %v", m)
	}
}

func TestMapToStruct(t *testing.T) {
	m := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string"},
		},
	}

	s, err := MapToStruct(m)
	if err != nil {
		t.Fatalf("MapToStruct: %v", err)
	}

	// Round-trip back to map.
	m2 := StructToMap(s)
	if m2["type"] != "object" {
		t.Errorf("type: got %v", m2["type"])
	}
}

func TestToolDefinitionToMCP(t *testing.T) {
	schema, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string"},
		},
	})

	td := &pluginv1.ToolDefinition{
		Name:        "my_tool",
		Description: "Does things",
		InputSchema: schema,
	}

	mcp := ToolDefinitionToMCP(td)
	if mcp.Name != "my_tool" {
		t.Errorf("name: got %q", mcp.Name)
	}
	if mcp.Description != "Does things" {
		t.Errorf("description: got %q", mcp.Description)
	}

	schemaMap, ok := mcp.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("inputSchema should be map, got %T", mcp.InputSchema)
	}
	if schemaMap["type"] != "object" {
		t.Errorf("schema type: got %v", schemaMap["type"])
	}
}

func TestToolResponseToMCPSuccess(t *testing.T) {
	result, _ := structpb.NewStruct(map[string]any{
		"text": "operation completed",
	})
	resp := &pluginv1.ToolResponse{
		Success: true,
		Result:  result,
	}

	mcp := ToolResponseToMCP(resp)
	if mcp.IsError {
		t.Error("expected IsError=false")
	}
	if len(mcp.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(mcp.Content))
	}
	if mcp.Content[0].Text != "operation completed" {
		t.Errorf("text: got %q", mcp.Content[0].Text)
	}
}

func TestToolResponseToMCPError(t *testing.T) {
	resp := &pluginv1.ToolResponse{
		Success:      false,
		ErrorCode:    "validation_error",
		ErrorMessage: "title is required",
	}

	mcp := ToolResponseToMCP(resp)
	if !mcp.IsError {
		t.Error("expected IsError=true")
	}
	if len(mcp.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(mcp.Content))
	}
	if mcp.Content[0].Text != "title is required" {
		t.Errorf("text: got %q", mcp.Content[0].Text)
	}
}

func TestToolResponseToMCPFallback(t *testing.T) {
	// When result has no "text" field, fall back to JSON.
	result, _ := structpb.NewStruct(map[string]any{
		"id":     "abc-123",
		"status": "created",
	})
	resp := &pluginv1.ToolResponse{
		Success: true,
		Result:  result,
	}

	mcp := ToolResponseToMCP(resp)
	if mcp.IsError {
		t.Error("expected IsError=false")
	}
	if len(mcp.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(mcp.Content))
	}

	// The fallback should be valid JSON.
	var m map[string]any
	if err := json.Unmarshal([]byte(mcp.Content[0].Text), &m); err != nil {
		t.Fatalf("fallback text is not valid JSON: %v", err)
	}
	if m["id"] != "abc-123" {
		t.Errorf("fallback id: got %v", m["id"])
	}
}

func TestToolsListNetworkError(t *testing.T) {
	sender := &mockSender{
		sendFunc: func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
			return nil, fmt.Errorf("connection timeout")
		},
	}

	reqJSON := `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`
	raw := runSingleRequest(t, sender, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error for network failure")
	}
	if resp.Error.Code != protocol.InternalError {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InternalError)
	}
}
