// Command transport-stdio is the entry point for the transport.stdio plugin.
// It bridges MCP JSON-RPC on stdin/stdout to the Orchestra orchestrator over
// QUIC. This plugin does not serve incoming QUIC requests -- it is purely a
// client that translates between the two protocols.
//
// Usage:
//
//	transport-stdio --orchestrator-addr localhost:9100 --certs-dir ~/.orchestra/certs
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/orchestra-mcp/sdk-go/plugin"
	"github.com/orchestra-mcp/plugin-transport-stdio/internal"
)

func main() {
	orchestratorAddr := flag.String("orchestrator-addr", "localhost:9100", "Address of the orchestrator")
	certsDir := flag.String("certs-dir", plugin.DefaultCertsDir, "Directory for mTLS certificates")
	flag.Parse()

	if *orchestratorAddr == "" {
		log.Fatal("--orchestrator-addr is required")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Resolve the certs directory (expand ~ if present).
	resolvedCertsDir := plugin.ResolveCertsDir(*certsDir)

	// Set up mTLS client configuration.
	clientTLS, err := plugin.ClientTLSConfig(resolvedCertsDir, "transport.stdio-client")
	if err != nil {
		log.Fatalf("client TLS config: %v", err)
	}

	// Connect to the orchestrator over QUIC.
	client, err := plugin.NewOrchestratorClient(ctx, *orchestratorAddr, clientTLS)
	if err != nil {
		log.Fatalf("connect to orchestrator at %s: %v", *orchestratorAddr, err)
	}
	defer client.Close()

	fmt.Fprintf(os.Stderr, "transport.stdio: connected to orchestrator at %s\n", *orchestratorAddr)

	// Start the stdio read/write loop.
	transport := internal.NewStdioTransport(client, os.Stdin, os.Stdout)
	if err := transport.Run(ctx); err != nil {
		if ctx.Err() != nil {
			// Graceful shutdown.
			fmt.Fprintf(os.Stderr, "transport.stdio: shutting down\n")
			return
		}
		log.Fatalf("transport.stdio: %v", err)
	}
}
