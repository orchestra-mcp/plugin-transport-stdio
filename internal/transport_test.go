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
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude","version":"1.0.0"}}}`

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

	if initResult.ProtocolVersion != protocol.MCPProtocolVersion {
		t.Errorf("protocolVersion: got %q, want %q", initResult.ProtocolVersion, protocol.MCPProtocolVersion)
	}
	if initResult.ServerInfo.Name != "orchestra" {
		t.Errorf("serverInfo.name: got %q, want %q", initResult.ServerInfo.Name, "orchestra")
	}
	if initResult.ServerInfo.Version != "dev" {
		t.Errorf("serverInfo.version: got %q, want %q", initResult.ServerInfo.Version, "dev")
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

// --- Prompts tests ---

func TestInitializeHasPromptsCapability(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}`

	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var initResult protocol.MCPInitializeResult
	if err := json.Unmarshal(resultBytes, &initResult); err != nil {
		t.Fatalf("unmarshal init result: %v", err)
	}

	if initResult.Capabilities.Prompts == nil {
		t.Error("expected capabilities.prompts to be set")
	}
}

func TestPromptsList(t *testing.T) {
	sender := &mockSender{
		sendFunc: func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
			if req.GetListPrompts() == nil {
				t.Error("expected ListPrompts request")
			}
			return &pluginv1.PluginResponse{
				RequestId: req.RequestId,
				Response: &pluginv1.PluginResponse_ListPrompts{
					ListPrompts: &pluginv1.ListPromptsResponse{
						Prompts: []*pluginv1.PromptDefinition{
							{
								Name:        "setup-project",
								Description: "Guide setting up a new project",
								Arguments: []*pluginv1.PromptArgument{
									{Name: "project_name", Description: "Name of the project", Required: true},
								},
							},
							{
								Name:        "audit-packs",
								Description: "Audit installed packs",
							},
						},
					},
				},
			}, nil
		},
	}

	reqJSON := `{"jsonrpc":"2.0","id":2,"method":"prompts/list"}`
	raw := runSingleRequest(t, sender, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var listResult promptsListResult
	if err := json.Unmarshal(resultBytes, &listResult); err != nil {
		t.Fatalf("unmarshal prompts list result: %v", err)
	}

	if len(listResult.Prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(listResult.Prompts))
	}

	p := listResult.Prompts[0]
	if p.Name != "setup-project" {
		t.Errorf("prompt name: got %q, want %q", p.Name, "setup-project")
	}
	if p.Description != "Guide setting up a new project" {
		t.Errorf("prompt description: got %q", p.Description)
	}
	if len(p.Arguments) != 1 {
		t.Fatalf("expected 1 argument, got %d", len(p.Arguments))
	}
	if p.Arguments[0].Name != "project_name" {
		t.Errorf("argument name: got %q", p.Arguments[0].Name)
	}
	if !p.Arguments[0].Required {
		t.Error("expected argument to be required")
	}
}

func TestPromptsGet(t *testing.T) {
	sender := &mockSender{
		sendFunc: func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
			pg := req.GetPromptGet()
			if pg == nil {
				t.Error("expected PromptGet request")
				return nil, fmt.Errorf("expected PromptGet request")
			}
			if pg.PromptName != "setup-project" {
				t.Errorf("prompt name: got %q, want %q", pg.PromptName, "setup-project")
			}
			if pg.Arguments["project_name"] != "demo" {
				t.Errorf("argument project_name: got %q, want %q", pg.Arguments["project_name"], "demo")
			}

			return &pluginv1.PluginResponse{
				RequestId: req.RequestId,
				Response: &pluginv1.PluginResponse_PromptGet{
					PromptGet: &pluginv1.PromptGetResponse{
						Description: "Set up a new project with recommended packs",
						Messages: []*pluginv1.PromptMessage{
							{
								Role: "user",
								Content: &pluginv1.ContentBlock{
									Type: "text",
									Text: "Set up project 'demo'.",
								},
							},
						},
					},
				},
			}, nil
		},
	}

	reqJSON := `{"jsonrpc":"2.0","id":3,"method":"prompts/get","params":{"name":"setup-project","arguments":{"project_name":"demo"}}}`
	raw := runSingleRequest(t, sender, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var promptResult protocol.MCPPromptResult
	if err := json.Unmarshal(resultBytes, &promptResult); err != nil {
		t.Fatalf("unmarshal prompt result: %v", err)
	}

	if promptResult.Description != "Set up a new project with recommended packs" {
		t.Errorf("description: got %q", promptResult.Description)
	}
	if len(promptResult.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(promptResult.Messages))
	}
	msg := promptResult.Messages[0]
	if msg.Role != "user" {
		t.Errorf("role: got %q, want %q", msg.Role, "user")
	}
	if msg.Content.Type != "text" {
		t.Errorf("content type: got %q, want %q", msg.Content.Type, "text")
	}
	if msg.Content.Text != "Set up project 'demo'." {
		t.Errorf("content text: got %q", msg.Content.Text)
	}
}

func TestPromptsGetMissingName(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":6,"method":"prompts/get","params":{"arguments":{}}}`
	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected error for missing prompt name")
	}
	if resp.Error.Code != protocol.InvalidParams {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InvalidParams)
	}
	if !strings.Contains(resp.Error.Message, "name") {
		t.Errorf("error message should mention 'name', got: %s", resp.Error.Message)
	}
}

func TestPromptsListNetworkError(t *testing.T) {
	sender := &mockSender{
		sendFunc: func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
			return nil, fmt.Errorf("connection timeout")
		},
	}

	reqJSON := `{"jsonrpc":"2.0","id":7,"method":"prompts/list"}`
	raw := runSingleRequest(t, sender, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error for network failure")
	}
	if resp.Error.Code != protocol.InternalError {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InternalError)
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

func TestPromptDefinitionToMCP(t *testing.T) {
	pd := &pluginv1.PromptDefinition{
		Name:        "test-prompt",
		Description: "A test prompt",
		Arguments: []*pluginv1.PromptArgument{
			{Name: "arg1", Description: "First arg", Required: true},
			{Name: "arg2", Description: "Second arg", Required: false},
		},
	}

	mcp := PromptDefinitionToMCP(pd)
	if mcp.Name != "test-prompt" {
		t.Errorf("name: got %q", mcp.Name)
	}
	if mcp.Description != "A test prompt" {
		t.Errorf("description: got %q", mcp.Description)
	}
	if len(mcp.Arguments) != 2 {
		t.Fatalf("expected 2 arguments, got %d", len(mcp.Arguments))
	}
	if mcp.Arguments[0].Name != "arg1" || !mcp.Arguments[0].Required {
		t.Errorf("arg1: got %+v", mcp.Arguments[0])
	}
	if mcp.Arguments[1].Name != "arg2" || mcp.Arguments[1].Required {
		t.Errorf("arg2: got %+v", mcp.Arguments[1])
	}
}

func TestPromptGetResponseToMCP(t *testing.T) {
	resp := &pluginv1.PromptGetResponse{
		Description: "Test prompt result",
		Messages: []*pluginv1.PromptMessage{
			{
				Role:    "user",
				Content: &pluginv1.ContentBlock{Type: "text", Text: "Hello world"},
			},
			{
				Role:    "assistant",
				Content: &pluginv1.ContentBlock{Type: "text", Text: "Hi there"},
			},
		},
	}

	mcp := PromptGetResponseToMCP(resp)
	if mcp.Description != "Test prompt result" {
		t.Errorf("description: got %q", mcp.Description)
	}
	if len(mcp.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(mcp.Messages))
	}
	if mcp.Messages[0].Role != "user" || mcp.Messages[0].Content.Text != "Hello world" {
		t.Errorf("message 0: got %+v", mcp.Messages[0])
	}
	if mcp.Messages[1].Role != "assistant" || mcp.Messages[1].Content.Text != "Hi there" {
		t.Errorf("message 1: got %+v", mcp.Messages[1])
	}
}

// --- Logging tests ---

func TestInitializeHasLoggingCapability(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}`

	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var initResult protocol.MCPInitializeResult
	if err := json.Unmarshal(resultBytes, &initResult); err != nil {
		t.Fatalf("unmarshal init result: %v", err)
	}

	if initResult.Capabilities.Logging == nil {
		t.Error("expected capabilities.logging to be set")
	}
}

func TestLoggingSetLevelValid(t *testing.T) {
	levels := []string{"debug", "info", "notice", "warning", "error", "critical", "alert", "emergency"}
	for _, level := range levels {
		reqJSON := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"logging/setLevel","params":{"level":"%s"}}`, level)
		raw := runSingleRequest(t, &mockSender{}, reqJSON)
		resp := parseJSONRPCResponse(t, raw)

		if resp.Error != nil {
			t.Errorf("level %q: unexpected error: %+v", level, resp.Error)
		}
		resultBytes, _ := json.Marshal(resp.Result)
		if string(resultBytes) != "{}" {
			t.Errorf("level %q: result: got %s, want {}", level, string(resultBytes))
		}
	}
}

func TestLoggingSetLevelInvalid(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"logging/setLevel","params":{"level":"verbose"}}`
	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected error for invalid log level")
	}
	if resp.Error.Code != protocol.InvalidParams {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InvalidParams)
	}
	if !strings.Contains(resp.Error.Message, "verbose") {
		t.Errorf("error message should mention 'verbose', got: %s", resp.Error.Message)
	}
}

func TestLoggingSetLevelPersists(t *testing.T) {
	// Send two requests through the same transport: setLevel then a second
	// setLevel. The transport should accept both and the second should
	// overwrite the first. We verify by checking no errors.
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"logging/setLevel","params":{"level":"debug"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"logging/setLevel","params":{"level":"error"}}`,
	}, "\n") + "\n"

	in := strings.NewReader(input)
	var out bytes.Buffer
	transport := NewStdioTransport(&mockSender{}, in, &out)
	if err := transport.Run(context.Background()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 response lines, got %d", len(lines))
	}

	for i, line := range lines {
		resp := parseJSONRPCResponse(t, line)
		if resp.Error != nil {
			t.Errorf("response %d: unexpected error: %+v", i, resp.Error)
		}
	}

	// Verify the transport's log level was set to the last value.
	if transport.logLevel != protocol.LogLevelError {
		t.Errorf("logLevel: got %q, want %q", transport.logLevel, protocol.LogLevelError)
	}
}

func TestSendLogNotificationAboveThreshold(t *testing.T) {
	var out bytes.Buffer
	transport := &StdioTransport{
		writer:   &out,
		logLevel: protocol.LogLevelWarning, // threshold = warning (3)
	}

	// Send an error notification (severity 4 >= warning severity 3).
	transport.SendLogNotification(protocol.LogLevelError, "test-logger", "something broke")

	raw := strings.TrimSpace(out.String())
	if raw == "" {
		t.Fatal("expected log notification output, got empty")
	}

	var notif map[string]any
	if err := json.Unmarshal([]byte(raw), &notif); err != nil {
		t.Fatalf("parse notification: %v", err)
	}

	if notif["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc: got %v", notif["jsonrpc"])
	}
	if notif["method"] != "notifications/message" {
		t.Errorf("method: got %v", notif["method"])
	}

	params, ok := notif["params"].(map[string]any)
	if !ok {
		t.Fatalf("params should be object, got %T", notif["params"])
	}
	if params["level"] != "error" {
		t.Errorf("level: got %v", params["level"])
	}
	if params["logger"] != "test-logger" {
		t.Errorf("logger: got %v", params["logger"])
	}
	if params["data"] != "something broke" {
		t.Errorf("data: got %v", params["data"])
	}
}

func TestSendLogNotificationBelowThreshold(t *testing.T) {
	var out bytes.Buffer
	transport := &StdioTransport{
		writer:   &out,
		logLevel: protocol.LogLevelError, // threshold = error (4)
	}

	// Send a warning notification (severity 3 < error severity 4).
	transport.SendLogNotification(protocol.LogLevelWarning, "test-logger", "just a warning")

	if out.Len() != 0 {
		t.Errorf("expected no output for below-threshold notification, got: %s", out.String())
	}
}

func TestSendLogNotificationAtThreshold(t *testing.T) {
	var out bytes.Buffer
	transport := &StdioTransport{
		writer:   &out,
		logLevel: protocol.LogLevelInfo, // threshold = info (1)
	}

	// Send an info notification (severity 1 == info severity 1).
	transport.SendLogNotification(protocol.LogLevelInfo, "server", "started")

	raw := strings.TrimSpace(out.String())
	if raw == "" {
		t.Fatal("expected log notification at exact threshold level")
	}

	var notif map[string]any
	if err := json.Unmarshal([]byte(raw), &notif); err != nil {
		t.Fatalf("parse notification: %v", err)
	}
	if notif["method"] != "notifications/message" {
		t.Errorf("method: got %v", notif["method"])
	}
}

func TestLogLevelSeverity(t *testing.T) {
	cases := []struct {
		level    protocol.MCPLogLevel
		severity int
	}{
		{protocol.LogLevelDebug, 0},
		{protocol.LogLevelInfo, 1},
		{protocol.LogLevelNotice, 2},
		{protocol.LogLevelWarning, 3},
		{protocol.LogLevelError, 4},
		{protocol.LogLevelCritical, 5},
		{protocol.LogLevelAlert, 6},
		{protocol.LogLevelEmergency, 7},
		{"unknown", -1},
	}

	for _, tc := range cases {
		got := protocol.LogLevelSeverity(tc.level)
		if got != tc.severity {
			t.Errorf("LogLevelSeverity(%q): got %d, want %d", tc.level, got, tc.severity)
		}
	}
}

// --- Resources tests ---

func TestInitializeHasResourcesCapability(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}`

	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var initResult protocol.MCPInitializeResult
	if err := json.Unmarshal(resultBytes, &initResult); err != nil {
		t.Fatalf("unmarshal init result: %v", err)
	}

	if initResult.Capabilities.Resources == nil {
		t.Error("expected capabilities.resources to be set")
	}
}

func TestResourcesList(t *testing.T) {
	sender := &mockSender{
		sendFunc: func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
			sl := req.GetStorageList()
			if sl == nil {
				return &pluginv1.PluginResponse{
					Response: &pluginv1.PluginResponse_StorageList{
						StorageList: &pluginv1.StorageListResponse{},
					},
				}, nil
			}
			// Return entries only for features/ prefix.
			if sl.Prefix == "features/" {
				return &pluginv1.PluginResponse{
					Response: &pluginv1.PluginResponse_StorageList{
						StorageList: &pluginv1.StorageListResponse{
							Entries: []*pluginv1.StorageEntry{
								{Path: "features/FEAT-001.md"},
								{Path: "features/FEAT-002.md"},
							},
						},
					},
				}, nil
			}
			return &pluginv1.PluginResponse{
				Response: &pluginv1.PluginResponse_StorageList{
					StorageList: &pluginv1.StorageListResponse{},
				},
			}, nil
		},
	}

	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`
	raw := runSingleRequest(t, sender, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result struct {
		Resources []protocol.MCPResource `json:"resources"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal resources list result: %v", err)
	}

	if len(result.Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(result.Resources))
	}
	if result.Resources[0].URI != "orchestra://features/FEAT-001" {
		t.Errorf("resource 0 URI: got %q", result.Resources[0].URI)
	}
	if result.Resources[0].Name != "FEAT-001" {
		t.Errorf("resource 0 name: got %q", result.Resources[0].Name)
	}
	if result.Resources[0].MimeType != "text/markdown" {
		t.Errorf("resource 0 mimeType: got %q", result.Resources[0].MimeType)
	}
	if result.Resources[1].URI != "orchestra://features/FEAT-002" {
		t.Errorf("resource 1 URI: got %q", result.Resources[1].URI)
	}
}

func TestResourcesRead(t *testing.T) {
	sender := &mockSender{
		sendFunc: func(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
			sr := req.GetStorageRead()
			if sr == nil {
				return nil, fmt.Errorf("expected StorageRead request")
			}
			if sr.Path != "features/FEAT-001.md" {
				t.Errorf("storage read path: got %q, want %q", sr.Path, "features/FEAT-001.md")
			}
			return &pluginv1.PluginResponse{
				Response: &pluginv1.PluginResponse_StorageRead{
					StorageRead: &pluginv1.StorageReadResponse{
						Content: []byte("# Feature FEAT-001\n\nDescription here."),
					},
				},
			}, nil
		},
	}

	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"orchestra://features/FEAT-001"}}`
	raw := runSingleRequest(t, sender, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result struct {
		Contents []protocol.MCPResourceContent `json:"contents"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal resources read result: %v", err)
	}

	if len(result.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(result.Contents))
	}
	c := result.Contents[0]
	if c.URI != "orchestra://features/FEAT-001" {
		t.Errorf("content URI: got %q", c.URI)
	}
	if c.MimeType != "text/markdown" {
		t.Errorf("content mimeType: got %q", c.MimeType)
	}
	if c.Text != "# Feature FEAT-001\n\nDescription here." {
		t.Errorf("content text: got %q", c.Text)
	}
}

func TestResourcesReadMissingURI(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{}}`
	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected error for missing URI")
	}
	if resp.Error.Code != protocol.InvalidParams {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InvalidParams)
	}
}

func TestResourcesReadBadScheme(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"https://example.com/foo"}}`
	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected error for bad URI scheme")
	}
	if resp.Error.Code != protocol.InvalidParams {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InvalidParams)
	}
}

func TestResourcesReadUnknownType(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"orchestra://unknown/foo"}}`
	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error == nil {
		t.Fatal("expected error for unknown resource type")
	}
	if resp.Error.Code != protocol.InvalidParams {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InvalidParams)
	}
}

func TestResourceTemplatesList(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"resources/templates/list"}`
	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result struct {
		ResourceTemplates []protocol.MCPResourceTemplate `json:"resourceTemplates"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal templates list result: %v", err)
	}

	if len(result.ResourceTemplates) != 3 {
		t.Fatalf("expected 3 templates, got %d", len(result.ResourceTemplates))
	}

	expected := []string{
		"orchestra://features/{id}",
		"orchestra://notes/{id}",
		"orchestra://docs/{id}",
	}
	for i, tmpl := range result.ResourceTemplates {
		if tmpl.URITemplate != expected[i] {
			t.Errorf("template %d URI: got %q, want %q", i, tmpl.URITemplate, expected[i])
		}
		if tmpl.MimeType != "text/markdown" {
			t.Errorf("template %d mimeType: got %q", i, tmpl.MimeType)
		}
	}
}

// --- listChanged tests ---

func TestInitializeHasListChangedCapability(t *testing.T) {
	reqJSON := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}`

	raw := runSingleRequest(t, &mockSender{}, reqJSON)
	resp := parseJSONRPCResponse(t, raw)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var initResult protocol.MCPInitializeResult
	if err := json.Unmarshal(resultBytes, &initResult); err != nil {
		t.Fatalf("unmarshal init result: %v", err)
	}

	if initResult.Capabilities.Tools == nil {
		t.Fatal("expected capabilities.tools to be set")
	}
	if !initResult.Capabilities.Tools.ListChanged {
		t.Error("expected capabilities.tools.listChanged to be true")
	}
}

func TestSendToolsListChanged(t *testing.T) {
	var out bytes.Buffer
	transport := &StdioTransport{
		writer: &out,
	}

	transport.SendToolsListChanged()

	raw := strings.TrimSpace(out.String())
	if raw == "" {
		t.Fatal("expected tools list changed notification output, got empty")
	}

	var notif map[string]any
	if err := json.Unmarshal([]byte(raw), &notif); err != nil {
		t.Fatalf("parse notification: %v", err)
	}

	if notif["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc: got %v", notif["jsonrpc"])
	}
	if notif["method"] != "notifications/tools/list_changed" {
		t.Errorf("method: got %v", notif["method"])
	}
	// Should have no id (it's a notification).
	if _, hasID := notif["id"]; hasID {
		t.Error("notification should not have an id field")
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
