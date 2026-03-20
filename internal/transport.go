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
	"log/slog"
	"strings"
	"sync"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/protocol"
	"google.golang.org/protobuf/encoding/protojson"
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
	logLevel     protocol.MCPLogLevel // minimum level for log notifications (default: warning)
	onDisconnect OnDisconnect
	eventCh      <-chan *pluginv1.EventDelivery
	serverInfo   protocol.MCPServerInfo // injected via WithServerInfo
}

// NewStdioTransport creates a new StdioTransport that reads from in and writes
// to out. The sender is used to communicate with the orchestrator.
// The optional onDisconnect callback is invoked when the Run loop exits.
func NewStdioTransport(sender Sender, in io.Reader, out io.Writer, opts ...func(*StdioTransport)) *StdioTransport {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, maxScannerBuffer), maxScannerBuffer)
	t := &StdioTransport{
		sender:   sender,
		reader:   scanner,
		writer:   out,
		logLevel: protocol.LogLevelWarning,
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

// WithEventChannel sets a channel of EventDelivery messages that the transport
// pushes as JSON-RPC notifications to the output. Used for real-time event
// streaming to connected IDE clients (Claude Code, Cursor, etc.).
func WithEventChannel(ch <-chan *pluginv1.EventDelivery) func(*StdioTransport) {
	return func(t *StdioTransport) {
		t.eventCh = ch
	}
}

// WithServerInfo sets the server name and version returned in the MCP
// initialize response. If not set, defaults to "orchestra" / "dev".
func WithServerInfo(info protocol.MCPServerInfo) func(*StdioTransport) {
	return func(t *StdioTransport) {
		t.serverInfo = info
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

	// Event push goroutine: reads EventDelivery from the channel and writes
	// JSON-RPC notifications to the output. IDE clients that don't understand
	// these notifications safely ignore them per the JSON-RPC spec.
	if t.eventCh != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ev := range t.eventCh {
				payloadMap := map[string]any{
					"topic":      ev.GetTopic(),
					"event_type": ev.GetEventType(),
					"source":     ev.GetSourcePlugin(),
				}
				if ev.GetPayload() != nil {
					raw, err := protojson.Marshal(ev.GetPayload())
					if err == nil {
						payloadMap["payload"] = json.RawMessage(raw)
					}
				}
				notif := map[string]any{
					"jsonrpc": "2.0",
					"method":  "notifications/event",
					"params":  payloadMap,
				}
				data, err := json.Marshal(notif)
				if err != nil {
					continue
				}
				data = append(data, '\n')
				t.mu.Lock()
				_, writeErr := t.writer.Write(data)
				t.mu.Unlock()
				if writeErr != nil {
					return
				}
			}
		}()
	}

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
					if err := t.writeResponse(resp); err != nil {
						slog.Error("failed writing async response", "method", r.Method, "error", err)
					}
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
	case "logging/setLevel":
		return t.handleLoggingSetLevel(req)
	case "resources/list":
		return t.handleResourcesList(ctx, req)
	case "resources/read":
		return t.handleResourcesRead(ctx, req)
	case "resources/templates/list":
		return t.handleResourceTemplatesList(req)
	default:
		// Notifications get no response.
		if strings.HasPrefix(req.Method, "notifications/") {
			slog.Debug("notification received", "method", req.Method)
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

// SendToolsListChanged sends a notifications/tools/list_changed JSON-RPC
// notification to inform the client that the tool list has been updated.
func (t *StdioTransport) SendToolsListChanged() {
	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/tools/list_changed",
	}
	raw, err := json.Marshal(notif)
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	t.mu.Lock()
	t.writer.Write(raw)
	t.mu.Unlock()
}

// SendLogNotification sends a notifications/message JSON-RPC notification to
// the client if the message's level meets or exceeds the configured threshold.
func (t *StdioTransport) SendLogNotification(level protocol.MCPLogLevel, logger, data string) {
	if protocol.LogLevelSeverity(level) < protocol.LogLevelSeverity(t.logLevel) {
		return
	}

	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/message",
		"params": map[string]any{
			"level":  string(level),
			"logger": logger,
			"data":   data,
		},
	}
	raw, err := json.Marshal(notif)
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	t.mu.Lock()
	t.writer.Write(raw)
	t.mu.Unlock()
}
