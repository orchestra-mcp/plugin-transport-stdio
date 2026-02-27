# Orchestra Transport Stdio Plugin

MCP JSON-RPC bridge that translates between stdin/stdout and QUIC + Protobuf.

## Install

```bash
go get github.com/orchestra-mcp/plugin-transport-stdio
```

## Usage

```bash
# Build
go build -o bin/transport-stdio ./cmd/

# Run (started automatically by the orchestrator)
bin/transport-stdio --orchestrator-addr localhost:9100
```

## How It Works

This plugin bridges the [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) to the Orchestra plugin system:

1. Reads JSON-RPC messages from stdin
2. Translates them into `PluginRequest` Protobuf messages
3. Forwards them to the orchestrator over QUIC
4. Receives `PluginResponse` messages back
5. Writes JSON-RPC responses to stdout

## Supported MCP Methods

| Method | Description |
|--------|-------------|
| `initialize` | MCP handshake and capability negotiation |
| `tools/list` | List all available tools across all plugins |
| `tools/call` | Execute a tool by name with arguments |
| `ping` | Health check |

## IDE Integration

Configure your IDE to run the Orchestra CLI as an MCP server. The CLI starts the orchestrator, which loads this transport plugin along with all other plugins:

```json
{
  "mcpServers": {
    "orchestra": {
      "command": "orchestra-mcp",
      "args": ["serve", "--workspace", "."]
    }
  }
}
```

## Related Packages

| Package | Description |
|---------|-------------|
| [sdk-go](https://github.com/orchestra-mcp/sdk-go) | Plugin SDK this plugin is built on |
| [orchestrator](https://github.com/orchestra-mcp/orchestrator) | Central hub that loads this plugin |
| [cli](https://github.com/orchestra-mcp/cli) | CLI that ties everything together |

## License

[MIT](LICENSE)
