// Package transportstdio provides in-process embedding for the transport.stdio plugin.
package transportstdio

import (
	"context"
	"io"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/plugin-transport-stdio/internal"
)

// Sender abstracts the request dispatcher. In production this is the
// in-process router or a QUIC client.
type Sender interface {
	Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error)
}

// Transport wraps the internal StdioTransport for public use.
type Transport struct {
	t *internal.StdioTransport
}

// NewTransport creates a new stdio JSON-RPC transport that reads from in and
// writes to out. The sender dispatches requests to the router.
func NewTransport(sender Sender, in io.Reader, out io.Writer) *Transport {
	return &Transport{t: internal.NewStdioTransport(sender, in, out)}
}

// Run reads JSON-RPC requests from stdin and dispatches them via the sender
// until EOF or context cancellation.
func (t *Transport) Run(ctx context.Context) error {
	return t.t.Run(ctx)
}
