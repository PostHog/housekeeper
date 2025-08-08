package main

import (
    "context"
    "fmt"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunMCPServer starts an MCP stdio server using the official go-sdk.
func RunMCPServer() error {
    impl := &mcp.Implementation{Name: "housekeeper-clickhouse-mcp", Title: "Housekeeper ClickHouse", Version: "0.3.0"}
    srv := mcp.NewServer(impl, &mcp.ServerOptions{})

    // Register tool with inferred input schema (from queryArgs)
    mcp.AddTool[queryArgs, map[string]any](
        srv,
        &mcp.Tool{
            Name:        "clickhouse.query",
            Title:       "Query ClickHouse system tables",
            Description: "Read-only queries against ClickHouse system.* via clusterAllReplicas",
            Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
        },
        func(ctx context.Context, ss *mcp.ServerSession, req *mcp.CallToolParamsFor[queryArgs]) (*mcp.CallToolResultFor[map[string]any], error) {
            qa := req.Arguments
            if qa.OrderBy == "" { /* tolerate orderBy alias via schema inference not possible here */ }
            if err := validateQueryArgs(qa); err != nil {
                return nil, err
            }
            rows, err := runClickhouseQuery(qa)
            if err != nil {
                return nil, err
            }
            data := map[string]any{"results": rows, "count": len(rows)}
            return &mcp.CallToolResultFor[map[string]any]{
                Content:           []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("rows: %d", len(rows))}},
                StructuredContent: data,
            }, nil
        },
    )

    return srv.Run(context.Background(), mcp.NewStdioTransport())
}
