package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/protocol"
	"google.golang.org/protobuf/types/known/structpb"
)

// handleInitialize responds to the MCP initialize handshake with the server's
// protocol version and capabilities. No orchestrator communication is needed.
func (t *StdioTransport) handleInitialize(req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	// Generate a unique session ID for this connection.
	t.sessionID = uuid.New().String()

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: protocol.MCPInitializeResult{
			ProtocolVersion: protocol.MCPProtocolVersion,
			Capabilities: protocol.MCPServerCapabilities{
				Tools:     &protocol.MCPToolsCapability{ListChanged: true},
				Prompts:   &protocol.MCPPromptsCapability{},
				Logging:   &protocol.MCPLoggingCapability{},
				Resources: &protocol.MCPResourcesCapability{},
			},
			ServerInfo: protocol.MCPServerInfo{
				Name:    "orchestra",
				Version: "1.0.0",
			},
			SessionID: t.sessionID,
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
				SessionId:    t.sessionID,
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

// promptsListResult is the JSON shape for a prompts/list response.
type promptsListResult struct {
	Prompts []protocol.MCPPromptDefinition `json:"prompts"`
}

// handlePromptsList queries the orchestrator for all registered prompts and
// converts them to MCP format.
func (t *StdioTransport) handlePromptsList(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	resp, err := t.sender.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("stdio-lp-%v", req.ID),
		Request: &pluginv1.PluginRequest_ListPrompts{
			ListPrompts: &pluginv1.ListPromptsRequest{},
		},
	})
	if err != nil {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InternalError,
				Message: fmt.Sprintf("orchestrator list_prompts failed: %v", err),
			},
		}
	}

	lp := resp.GetListPrompts()
	if lp == nil {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InternalError,
				Message: "unexpected response type from orchestrator",
			},
		}
	}

	mcpPrompts := make([]protocol.MCPPromptDefinition, 0, len(lp.Prompts))
	for _, pd := range lp.Prompts {
		mcpPrompts = append(mcpPrompts, PromptDefinitionToMCP(pd))
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  promptsListResult{Prompts: mcpPrompts},
	}
}

// promptGetParams is the expected shape of params for a prompts/get request.
type promptGetParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// handlePromptsGet parses the prompt name and arguments from the JSON-RPC
// request, sends a PromptGetRequest to the orchestrator, and converts the
// response to MCP format.
func (t *StdioTransport) handlePromptsGet(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	var params promptGetParams
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

	resp, err := t.sender.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("stdio-pg-%v", req.ID),
		Request: &pluginv1.PluginRequest_PromptGet{
			PromptGet: &pluginv1.PromptGetRequest{
				PromptName: params.Name,
				Arguments:  params.Arguments,
			},
		},
	})
	if err != nil {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InternalError,
				Message: fmt.Sprintf("orchestrator prompt_get failed: %v", err),
			},
		}
	}

	pg := resp.GetPromptGet()
	if pg == nil {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InternalError,
				Message: "unexpected response type from orchestrator",
			},
		}
	}

	mcpResult := PromptGetResponseToMCP(pg)

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  mcpResult,
	}
}

// --- Resources handlers ---

// resourcePrefixes are the storage prefixes exposed as MCP resources.
var resourcePrefixes = []struct {
	prefix   string // storage prefix (e.g. "features/")
	scheme   string // URI scheme segment (e.g. "features")
	name     string // human-readable name for templates
	mimeType string
}{
	{"features/", "features", "Project Features", "text/markdown"},
	{"notes/", "notes", "Project Notes", "text/markdown"},
	{"docs/", "docs", "Project Documentation", "text/markdown"},
}

// resourcesListResult is the JSON shape for a resources/list response.
type resourcesListResult struct {
	Resources []protocol.MCPResource `json:"resources"`
}

// handleResourcesList lists all available resources by querying storage for
// each known prefix.
func (t *StdioTransport) handleResourcesList(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	var resources []protocol.MCPResource

	for _, rp := range resourcePrefixes {
		resp, err := t.sender.Send(ctx, &pluginv1.PluginRequest{
			RequestId: fmt.Sprintf("stdio-rl-%v-%s", req.ID, rp.scheme),
			Request: &pluginv1.PluginRequest_StorageList{
				StorageList: &pluginv1.StorageListRequest{
					Prefix: rp.prefix,
				},
			},
		})
		if err != nil {
			continue
		}
		sl := resp.GetStorageList()
		if sl == nil {
			continue
		}
		for _, entry := range sl.GetEntries() {
			// Extract the ID from the path (e.g. "features/FEAT-ABC.md" -> "FEAT-ABC")
			name := entry.GetPath()
			// Remove prefix
			name = strings.TrimPrefix(name, rp.prefix)
			// Remove .md extension if present
			name = strings.TrimSuffix(name, ".md")

			resources = append(resources, protocol.MCPResource{
				URI:      fmt.Sprintf("orchestra://%s/%s", rp.scheme, name),
				Name:     name,
				MimeType: rp.mimeType,
			})
		}
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  resourcesListResult{Resources: resources},
	}
}

// resourcesReadParams is the expected shape of params for resources/read.
type resourcesReadParams struct {
	URI string `json:"uri"`
}

// resourcesReadResult is the JSON shape for a resources/read response.
type resourcesReadResult struct {
	Contents []protocol.MCPResourceContent `json:"contents"`
}

// handleResourcesRead reads a single resource by URI.
func (t *StdioTransport) handleResourcesRead(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	var params resourcesReadParams
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

	if params.URI == "" {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InvalidParams,
				Message: "missing required parameter: uri",
			},
		}
	}

	// Parse URI: "orchestra://features/FEAT-ABC" -> prefix "features/", id "FEAT-ABC"
	const uriPrefix = "orchestra://"
	if !strings.HasPrefix(params.URI, uriPrefix) {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InvalidParams,
				Message: fmt.Sprintf("unsupported URI scheme: %q (expected orchestra://)", params.URI),
			},
		}
	}

	rest := strings.TrimPrefix(params.URI, uriPrefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InvalidParams,
				Message: fmt.Sprintf("invalid resource URI: %q", params.URI),
			},
		}
	}

	scheme := parts[0]
	id := parts[1]

	// Find the matching prefix.
	var storagePath string
	var mimeType string
	for _, rp := range resourcePrefixes {
		if rp.scheme == scheme {
			storagePath = rp.prefix + id + ".md"
			mimeType = rp.mimeType
			break
		}
	}
	if storagePath == "" {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InvalidParams,
				Message: fmt.Sprintf("unknown resource type: %q", scheme),
			},
		}
	}

	resp, err := t.sender.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("stdio-rr-%v", req.ID),
		Request: &pluginv1.PluginRequest_StorageRead{
			StorageRead: &pluginv1.StorageReadRequest{
				Path: storagePath,
			},
		},
	})
	if err != nil {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InternalError,
				Message: fmt.Sprintf("storage read failed: %v", err),
			},
		}
	}

	sr := resp.GetStorageRead()
	if sr == nil {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InternalError,
				Message: "unexpected response type from storage",
			},
		}
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: resourcesReadResult{
			Contents: []protocol.MCPResourceContent{
				{
					URI:      params.URI,
					MimeType: mimeType,
					Text:     string(sr.GetContent()),
				},
			},
		},
	}
}

// resourceTemplatesListResult is the JSON shape for a resources/templates/list response.
type resourceTemplatesListResult struct {
	ResourceTemplates []protocol.MCPResourceTemplate `json:"resourceTemplates"`
}

// handleResourceTemplatesList returns the static resource URI templates.
func (t *StdioTransport) handleResourceTemplatesList(req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	templates := make([]protocol.MCPResourceTemplate, 0, len(resourcePrefixes))
	for _, rp := range resourcePrefixes {
		templates = append(templates, protocol.MCPResourceTemplate{
			URITemplate: fmt.Sprintf("orchestra://%s/{id}", rp.scheme),
			Name:        rp.name,
			Description: fmt.Sprintf("Access %s by ID", rp.name),
			MimeType:    rp.mimeType,
		})
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  resourceTemplatesListResult{ResourceTemplates: templates},
	}
}

// loggingSetLevelParams is the expected shape of params for logging/setLevel.
type loggingSetLevelParams struct {
	Level string `json:"level"`
}

// handleLoggingSetLevel sets the minimum log level for notifications/message.
func (t *StdioTransport) handleLoggingSetLevel(req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	var params loggingSetLevelParams
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

	level := protocol.MCPLogLevel(params.Level)
	if protocol.LogLevelSeverity(level) < 0 {
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.InvalidParams,
				Message: fmt.Sprintf("invalid log level: %q", params.Level),
			},
		}
	}

	t.logLevel = level

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{},
	}
}
