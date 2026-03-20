// Package transportstdio provides in-process embedding for the transport.stdio plugin.
package transportstdio

import (
	"context"
	"io"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/plugin-transport-stdio/internal"
	"github.com/orchestra-mcp/sdk-go/protocol"
)

// Sender abstracts the request dispatcher. In production this is the
// in-process router or a QUIC client.
type Sender interface {
	Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error)
}

// TransportOption configures optional behavior on the Transport.
type TransportOption func(*internal.StdioTransport)

// WithOnDisconnect sets a callback invoked when the transport exits (client
// disconnects). The callback receives the session ID so the caller can clean
// up session-scoped resources like feature locks.
func WithOnDisconnect(fn func(sessionID string)) TransportOption {
	return func(t *internal.StdioTransport) {
		internal.WithOnDisconnect(fn)(t)
	}
}

// WithEventChannel sets a channel of EventDelivery messages that the transport
// pushes as JSON-RPC notifications to the output. Used for real-time event
// streaming to connected IDE clients.
func WithEventChannel(ch <-chan *pluginv1.EventDelivery) TransportOption {
	return func(t *internal.StdioTransport) {
		internal.WithEventChannel(ch)(t)
	}
}

// WithServerInfo sets the server name and version returned in the MCP
// initialize response.
func WithServerInfo(info protocol.MCPServerInfo) TransportOption {
	return func(t *internal.StdioTransport) {
		internal.WithServerInfo(info)(t)
	}
}

// Transport wraps the internal StdioTransport for public use.
type Transport struct {
	t *internal.StdioTransport
}

// NewTransport creates a new stdio JSON-RPC transport that reads from in and
// writes to out. The sender dispatches requests to the router.
func NewTransport(sender Sender, in io.Reader, out io.Writer, opts ...TransportOption) *Transport {
	internalOpts := make([]func(*internal.StdioTransport), len(opts))
	for i, opt := range opts {
		internalOpts[i] = func(t *internal.StdioTransport) { opt(t) }
	}
	return &Transport{t: internal.NewStdioTransport(sender, in, out, internalOpts...)}
}

// Run reads JSON-RPC requests from stdin and dispatches them via the sender
// until EOF or context cancellation.
func (t *Transport) Run(ctx context.Context) error {
	return t.t.Run(ctx)
}

// SendToolsListChanged sends a notifications/tools/list_changed notification
// to the connected client, prompting it to re-fetch the tool list.
func (t *Transport) SendToolsListChanged() {
	t.t.SendToolsListChanged()
}
