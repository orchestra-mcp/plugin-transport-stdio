// Package internal implements the stdio-to-QUIC bridge for the transport.stdio
// plugin. It reads newline-delimited JSON-RPC 2.0 messages from stdin, translates
// them to Protobuf PluginRequests, sends them to the orchestrator over QUIC, and
// writes the JSON-RPC responses to stdout.
package internal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/protocol"
)

// maxScannerBuffer is 10 MB, large enough for big JSON-RPC tool responses.
const maxScannerBuffer = 10 * 1024 * 1024

// Sender abstracts the QUIC client so StdioTransport can be tested without a
// real network connection. In production this is backed by
// plugin.OrchestratorClient.
type Sender interface {
	Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error)
}

// StdioTransport reads JSON-RPC from an input reader, dispatches each message
// through the orchestrator, and writes JSON-RPC responses to an output writer.
type StdioTransport struct {
	sender Sender
	reader *bufio.Scanner
	writer io.Writer
	mu     sync.Mutex // protects writer
}

// NewStdioTransport creates a new StdioTransport that reads from in and writes
// to out. The sender is used to communicate with the orchestrator.
func NewStdioTransport(sender Sender, in io.Reader, out io.Writer) *StdioTransport {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, maxScannerBuffer), maxScannerBuffer)
	return &StdioTransport{
		sender: sender,
		reader: scanner,
		writer: out,
	}
}

// Run reads lines from the input until EOF or the context is cancelled. Each
// line is parsed as a JSON-RPC 2.0 request and dispatched to the appropriate
// handler. Responses are written as single JSON lines to the output.
func (t *StdioTransport) Run(ctx context.Context) error {
	for t.reader.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		line := strings.TrimSpace(t.reader.Text())
		if line == "" {
			continue
		}

		var req protocol.JSONRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			resp := &protocol.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error: &protocol.JSONRPCError{
					Code:    protocol.ParseError,
					Message: fmt.Sprintf("parse error: %v", err),
				},
			}
			if writeErr := t.writeResponse(resp); writeErr != nil {
				return fmt.Errorf("write parse error response: %w", writeErr)
			}
			continue
		}

		resp := t.dispatch(ctx, &req)

		// Notifications (no ID) get no response.
		if resp == nil {
			continue
		}

		if err := t.writeResponse(resp); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}

	if err := t.reader.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	return nil
}

// dispatch routes a JSON-RPC request to the appropriate handler based on the
// method field. Notifications (methods starting with "notifications/") return
// nil to indicate no response should be written.
func (t *StdioTransport) dispatch(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	switch req.Method {
	case "initialize":
		return t.handleInitialize(req)
	case "ping":
		return t.handlePing(req)
	case "tools/list":
		return t.handleToolsList(ctx, req)
	case "tools/call":
		return t.handleToolsCall(ctx, req)
	case "prompts/list":
		return t.handlePromptsList(ctx, req)
	case "prompts/get":
		return t.handlePromptsGet(ctx, req)
	default:
		// Notifications get no response.
		if strings.HasPrefix(req.Method, "notifications/") {
			log.Printf("notification: %s", req.Method)
			return nil
		}
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.MethodNotFound,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
		}
	}
}

// writeResponse serializes a JSON-RPC response as a single JSON line and writes
// it to the output. Access to the writer is serialized with a mutex.
func (t *StdioTransport) writeResponse(resp *protocol.JSONRPCResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	data = append(data, '\n')
	_, err = t.writer.Write(data)
	return err
}
