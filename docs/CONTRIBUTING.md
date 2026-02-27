# Contributing to plugin-transport-stdio

## Prerequisites

- Go 1.23+
- `gofmt`, `go vet`

## Development Setup

```bash
git clone https://github.com/orchestra-mcp/plugin-transport-stdio.git
cd plugin-transport-stdio
go mod download
go build ./cmd/...
```

## Running Locally

The transport plugin requires a running orchestrator:

```bash
go build -o transport-stdio ./cmd/
./transport-stdio --orchestrator-addr=localhost:50100 --certs-dir=~/.orchestra/certs
```

It reads JSON-RPC from stdin and writes responses to stdout. You can test manually:

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize"}' | ./transport-stdio --orchestrator-addr=localhost:50100
```

## Running Tests

```bash
go test ./...
```

Tests use a mock `Sender` interface so they run without a real QUIC connection or orchestrator.

## Code Organization

```
plugin-transport-stdio/
  cmd/main.go                   # Entry point: parse flags, QUIC connect, start transport
  internal/
    transport.go                # StdioTransport: stdin/stdout read/write loop, dispatch
    handler.go                  # MCP method handlers (initialize, ping, tools/list, tools/call)
    translator.go               # Protobuf <-> MCP type conversions
    transport_test.go           # Tests with mock sender
```

## Code Style

- Run `gofmt` on all files.
- Run `go vet ./...` before committing.
- All exported functions and types must have doc comments.
- The `Sender` interface must remain the only abstraction boundary for testability.
- Keep the translation layer (translator.go) pure -- no side effects, no I/O.

## Pull Request Process

1. Fork the repository and create a feature branch from `main`.
2. Write or update tests for your changes.
3. Run `go test ./...` and `go vet ./...`.
4. Update `docs/PROTOCOL.md` if adding new MCP method support or changing message formats.

## Related Repositories

- [orchestra-mcp/proto](https://github.com/orchestra-mcp/proto) -- Protobuf schema
- [orchestra-mcp/sdk-go](https://github.com/orchestra-mcp/sdk-go) -- Go Plugin SDK
- [orchestra-mcp/orchestrator](https://github.com/orchestra-mcp/orchestrator) -- Central hub
- [orchestra-mcp](https://github.com/orchestra-mcp) -- Organization home
