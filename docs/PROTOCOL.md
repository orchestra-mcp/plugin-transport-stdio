# Protocol

## Overview

The `transport.stdio` plugin bridges the MCP (Model Context Protocol) JSON-RPC 2.0 interface with the Orchestra QUIC + Protobuf backend. It reads newline-delimited JSON-RPC from stdin, translates each request to a Protobuf `PluginRequest`, sends it to the orchestrator over QUIC, translates the response back to JSON-RPC, and writes it to stdout.

This plugin does **not** run its own QUIC server. It is a pure client that connects to the orchestrator.

## Supported MCP Methods

| Method | Description | Uses Orchestrator |
|---|---|---|
| `initialize` | MCP handshake -- returns protocol version and capabilities | No (local) |
| `ping` | Liveness check -- returns empty object | No (local) |
| `tools/list` | List all available tools | Yes (ListTools) |
| `tools/call` | Invoke a tool by name with arguments | Yes (ToolCall) |
| `notifications/*` | MCP notifications -- acknowledged silently | No (ignored) |

## Message Format

### Input (stdin)

One JSON-RPC 2.0 request per line (newline-delimited):

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_project","arguments":{"name":"My App"}}}
```

### Output (stdout)

One JSON-RPC 2.0 response per line:

```json
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"orchestra","version":"1.0.0"}}}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"create_project","description":"Create a new project workspace","inputSchema":{...}}]}}
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"Created project: My App (slug: my-app)"}]}}
```

## Translation: JSON-RPC to Protobuf

### `initialize`

Handled locally. Returns:

```json
{
  "protocolVersion": "2024-11-05",
  "capabilities": { "tools": {} },
  "serverInfo": { "name": "orchestra", "version": "1.0.0" }
}
```

### `tools/list`

Sends a `ListToolsRequest` to the orchestrator. Each `ToolDefinition` is converted to MCP format:

| Protobuf Field | MCP Field |
|---|---|
| `ToolDefinition.name` | `name` |
| `ToolDefinition.description` | `description` |
| `ToolDefinition.input_schema` (Struct) | `inputSchema` (JSON object) |

### `tools/call`

Parses the JSON-RPC params:

```json
{
  "name": "create_feature",
  "arguments": {
    "project_id": "my-app",
    "title": "Add login page"
  }
}
```

Converts to a `ToolRequest`:
- `tool_name` = params.name
- `arguments` = params.arguments converted to `structpb.Struct`
- `caller_plugin` = `"transport.stdio"`

The `ToolResponse` is converted to MCP format:

**Success:**
```json
{
  "content": [{"type": "text", "text": "Created FEAT-ABC: Add login page"}]
}
```

**Error:**
```json
{
  "content": [{"type": "text", "text": "tool error: validation_error"}],
  "isError": true
}
```

The text content is extracted from the `ToolResponse.result` Struct. If a `text` field exists, it is used directly. Otherwise, the entire result is JSON-serialized.

### `ping`

Returns an empty JSON object:

```json
{"jsonrpc":"2.0","id":1,"result":{}}
```

### Notifications

Any method starting with `notifications/` is logged and produces no response (per JSON-RPC 2.0 notification semantics).

### Unknown Methods

Returns a JSON-RPC error:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found: some/unknown"}}
```

## Error Codes

| Code | Constant | Meaning |
|---|---|---|
| `-32700` | ParseError | Malformed JSON |
| `-32600` | InvalidRequest | Not a valid JSON-RPC request |
| `-32601` | MethodNotFound | Unknown method |
| `-32602` | InvalidParams | Missing or invalid parameters |
| `-32603` | InternalError | Orchestrator communication failure |

## Connection Flow

```
1. Parse --orchestrator-addr and --certs-dir flags
2. Generate/load mTLS client certificate
3. QUIC dial to orchestrator
4. Print connection confirmation to stderr
5. Start stdin read loop:
   a. Read one JSON line
   b. Parse as JSONRPCRequest
   c. Dispatch to handler
   d. Write JSONRPCResponse to stdout
6. On stdin EOF or SIGINT/SIGTERM: close QUIC connection, exit
```

## Scanner Buffer

The stdin scanner uses a 10 MB buffer (`maxScannerBuffer = 10 * 1024 * 1024`) to accommodate large JSON-RPC messages, such as tool responses containing extensive Markdown content.
