package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/breakfix/breakfix/internal/adapter/mcpconnector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	configPath := flag.String("config", mcpconnector.DefaultConfigPath(), "Local MCP connector config file path")
	flag.Parse()

	if err := run(*configPath); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) && !errors.Is(err, mcp.ErrConnectionClosed) {
		// stdout is reserved for the MCP protocol.
		fmt.Fprintln(os.Stderr, "breakfix-mcp:", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	config, err := mcpconnector.LoadConfig(configPath)
	if err != nil {
		return err
	}
	client, err := config.NewClient(nil)
	if err != nil {
		return err
	}
	projector, err := mcpconnector.NewReviewProjector("")
	if err != nil {
		return err
	}
	connector, err := mcpconnector.NewConnector(client, projector, mcpconnector.ConnectorConfig{})
	if err != nil {
		return err
	}
	server, err := connector.NewMCPServer()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx, &mcp.StdioTransport{})
}
