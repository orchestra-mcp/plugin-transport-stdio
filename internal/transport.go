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

// OnDisconnect is called when the transport's Run loop exits (client
// disconnected). It receives the session ID so the caller can clean up
// session-scoped resources like feature locks.
type OnDisconnect func(sessionID string)

// StdioTransport reads JSON-RPC from an input reader, dispatches each message
// through the orchestrator, and writes JSON-RPC responses to an output writer.
type StdioTransport struct {
	sender       Sender
	reader       *bufio.Scanner
	writer       io.Writer
	mu           sync.Mutex // protects writer
	sessionID    string
	onDisconnect OnDisconnect
}

// NewStdioTransport creates a new StdioTransport that reads from in and writes
// to out. The sender is used to communicate with the orchestrator.
// The optional onDisconnect callback is invoked when the Run loop exits.
func NewStdioTransport(sender Sender, in io.Reader, out io.Writer, opts ...func(*StdioTransport)) *StdioTransport {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, maxScannerBuffer), maxScannerBuffer)
	t := &StdioTransport{
		sender: sender,
		reader: scanner,
		writer: out,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// WithOnDisconnect sets a callback invoked when the transport exits.
func WithOnDisconnect(fn OnDisconnect) func(*StdioTransport) {
	return func(t *StdioTransport) {
		t.onDisconnect = fn
	}
}

// Run reads lines from the input until EOF or the context is cancelled. Each
// line is parsed as a JSON-RPC 2.0 request and dispatched to the appropriate
// handler. Responses are written as single JSON lines to the output.
//
// tools/call requests are dispatched in goroutines so that long-running tool
// calls (e.g. send_message with wait=true) don't block subsequent requests
// (e.g. get_pending_permission polls). The writer is mutex-protected so
// concurrent response writes are safe. A WaitGroup ensures all in-flight
// requests complete before Run returns.
func (t *StdioTransport) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		if t.onDisconnect != nil && t.sessionID != "" {
			t.onDisconnect(t.sessionID)
		}
	}()

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

		// Dispatch tools/call concurrently so long-running calls don't block
		// the read loop. Other methods (initialize, ping, list) are fast and
		// handled inline to preserve ordering where it matters.
		if req.Method == "tools/call" {
			wg.Add(1)
			go func(r protocol.JSONRPCRequest) {
				defer wg.Done()
				resp := t.dispatch(ctx, &r)
				if resp != nil {
					_ = t.writeResponse(resp)
				}
			}(req)
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
