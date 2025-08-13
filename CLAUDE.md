# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Housekeeper is an MCP-first application** that runs as a Model Context Protocol server by default, providing AI assistants with direct access to ClickHouse system tables and Prometheus metrics.

### Primary Mode: MCP Server (Default)
When run without flags, Housekeeper starts an MCP server that exposes:
- **clickhouse_query**: Read-only access to ClickHouse system tables
- **prometheus_query**: Execute PromQL queries against Prometheus/Victoria Metrics

### Analysis Mode (Optional)
With the `--analyze` flag, it monitors and analyzes ClickHouse database errors using Google's Gemini AI, generating summaries suitable for Slack notifications.

## Development Commands

### Running the Application
```bash
# Run as MCP server (default)
go run .

# Run in analysis mode (Gemini AI error analysis)
go run . --analyze

# Run performance analysis
go run . --analyze --performance

# Build the application
go build -o housekeeper

# Run with custom config
go run . --config configs/config.yml
```

### Development Environment
```bash
# Start local ClickHouse instance
docker-compose up -d

# Stop ClickHouse
docker-compose down

# View ClickHouse logs
docker-compose logs clickhouse
```

### Dependency Management
```bash
# Download dependencies
go mod download

# Update dependencies
go mod tidy

# Verify dependencies
go mod verify
```

## Architecture

### Core Components
1. **main.go** - Application entry point, defaults to MCP server mode
2. **mcp_server.go** - MCP server implementation with clickhouse_query and prometheus_query tools
3. **config.go** - Manages configuration via command-line flags or YAML config
4. **clickhouse.go** - Handles ClickHouse connections and queries across cluster replicas
5. **prometheus.go** - Prometheus/Victoria Metrics client for metrics queries
6. **gemini.go** - Integrates with Google Gemini AI for error analysis (analysis mode only)

### Configuration Structure
The application expects a `configs/config.yml` file (copy from `configs/config.yml.sample`):
- `gemini_api_key`: Google Gemini API key
- `clickhouse`: Connection parameters including host, port, user, password, database, and cluster name

### Key Design Patterns
- **MCP-first architecture**: Runs as MCP server by default for AI assistant integration
- **Cluster-aware querying**: Queries all replicas using `clusterAllReplicas()` function
- **Read-only enforcement**: MCP mode restricts queries to system tables only
- **Flexible configuration**: Supports both command-line flags and YAML config
- **Time-based filtering**: Analysis mode focuses on errors from the last hour
- **AI prompt engineering**: Uses specific prompts for Slack-friendly summaries

## Important Notes

1. **MCP is default mode** - Application runs as MCP server unless `--analyze` flag is used
2. **No tests exist** - When adding features, consider creating a test suite
3. **Configuration security** - Ensure `configs/config.yml` remains in .gitignore
4. **ClickHouse connection** - Uses native protocol on port 9000 by default
5. **MCP restrictions** - Server enforces read-only access to `system.*` tables only
6. **Error handling** - The application uses `log.Fatal()` for errors, which exits the program
7. **Local development** - Docker Compose provides a local ClickHouse instance with default credentials
