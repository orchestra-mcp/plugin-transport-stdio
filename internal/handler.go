package internal

import (
	"context"
	"encoding/json"
	"fmt"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/protocol"
	"google.golang.org/protobuf/types/known/structpb"
)

// handleInitialize responds to the MCP initialize handshake with the server's
// protocol version and capabilities. No orchestrator communication is needed.
func (t *StdioTransport) handleInitialize(req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: protocol.MCPInitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities: protocol.MCPServerCapabilities{
				Tools: &protocol.MCPToolsCapability{},
			},
			ServerInfo: protocol.MCPServerInfo{
				Name:    "orchestra",
				Version: "1.0.0",
			},
		},
	}
}

// handlePing responds with an empty result object (pong).
func (t *StdioTransport) handlePing(req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{},
	}
}

// toolsListResult is the JSON shape for a tools/list response.
type toolsListResult struct {
	Tools []protocol.MCPToolDefinition `json:"tools"`
}

// handleToolsList queries the orchestrator for all registered tools and converts
// them to MCP format.
func (t *StdioTransport) handleToolsList(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	resp, err := t.sender.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("stdio-lt-%v", req.ID),
		Request: &pluginv1.PluginRequest_ListTools{
			ListTools: &pluginv1.ListToolsRequest{},
		},
	})
	if err != nil {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InternalError,
				Message: fmt.Sprintf("orchestrator list_tools failed: %v", err),
			},
		}
	}

	lt := resp.GetListTools()
	if lt == nil {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InternalError,
				Message: "unexpected response type from orchestrator",
			},
		}
	}

	mcpTools := make([]protocol.MCPToolDefinition, 0, len(lt.Tools))
	for _, td := range lt.Tools {
		mcpTools = append(mcpTools, ToolDefinitionToMCP(td))
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  toolsListResult{Tools: mcpTools},
	}
}

// toolCallParams is the expected shape of params for a tools/call request.
type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// handleToolsCall parses the tool name and arguments from the JSON-RPC request,
// sends a ToolRequest to the orchestrator, and converts the response to MCP format.
func (t *StdioTransport) handleToolsCall(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	var params toolCallParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return &protocol.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &protocol.JSONRPCError{
					Code:    protocol.InvalidParams,
					Message: fmt.Sprintf("invalid params: %v", err),
				},
			}
		}
	}

	if params.Name == "" {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InvalidParams,
				Message: "missing required parameter: name",
			},
		}
	}

	// Convert arguments map to protobuf Struct.
	var args *structpb.Struct
	if params.Arguments != nil {
		var err error
		args, err = structpb.NewStruct(params.Arguments)
		if err != nil {
			return &protocol.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &protocol.JSONRPCError{
					Code:    protocol.InvalidParams,
					Message: fmt.Sprintf("invalid arguments: %v", err),
				},
			}
		}
	}

	resp, err := t.sender.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("stdio-tc-%v", req.ID),
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: &pluginv1.ToolRequest{
				ToolName:     params.Name,
				Arguments:    args,
				CallerPlugin: "transport.stdio",
			},
		},
	})
	if err != nil {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InternalError,
				Message: fmt.Sprintf("orchestrator tool_call failed: %v", err),
			},
		}
	}

	tc := resp.GetToolCall()
	if tc == nil {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InternalError,
				Message: "unexpected response type from orchestrator",
			},
		}
	}

	mcpResult := ToolResponseToMCP(tc)

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  mcpResult,
	}
}
