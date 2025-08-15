# ClickHouse MCP Server Documentation

**Housekeeper runs as an MCP server by default** - no flags needed! This document covers the complete MCP implementation that exposes tools for:
1. Read‑only queries against configurable ClickHouse databases
2. Querying Prometheus metrics for monitoring and correlation

## Installation

### Option 1: Install via go install (Recommended)

```bash
# Install the latest version directly from GitHub
go install github.com/PostHog/housekeeper@latest

# The binary will be installed to $GOPATH/bin/housekeeper
# Make sure $GOPATH/bin is in your PATH
```

### Option 2: Build from source

```bash
# Clone the repository
git clone https://github.com/PostHog/housekeeper.git
cd housekeeper

# Build integrated binary
go build -o housekeeper
```

## Configuration

### Option 1: Command-line flags (Recommended for MCP)

You can configure the server entirely via command-line flags, making it easy to use without config files:

```bash
# MCP mode is the default - just run with your connection parameters
housekeeper \
  --ch-host "127.0.0.1" \
  --ch-port 9000 \
  --ch-user "default" \
  --ch-password "your-password" \
  --ch-database "default" \
  --ch-cluster "default" \
  --ch-allowed-databases "system,models" \
  --prom-host "localhost" \
  --prom-port 8481
```

Available flags:
- `--ch-host`: ClickHouse host (default: "127.0.0.1")
- `--ch-allowed-databases`: Comma-separated list of databases to allow (default: ["system"])
- `--ch-port`: ClickHouse port (default: 9000)
- `--ch-user`: ClickHouse user (default: "default")
- `--ch-password`: ClickHouse password (default: "")
- `--ch-database`: ClickHouse database (default: "default")
- `--ch-cluster`: ClickHouse cluster name (default: "default")
- `--prom-host`: Prometheus/Victoria Metrics host (default: "localhost")
- `--prom-port`: Prometheus/Victoria Metrics port (default: 8481)
- `--prom-vm-cluster`: Enable Victoria Metrics cluster mode (default: false)
- `--prom-vm-tenant`: Victoria Metrics tenant ID (default: "0")
- `--prom-vm-prefix`: Victoria Metrics path prefix (default: "")

### Option 2: Configuration file

- Uses `configs/config.yml` (Viper) — copy and edit `configs/config.yml.sample`.
- You can point to a custom path with `-config /path/to/config.yml` or env `HOUSEKEEPER_CONFIG=/path/to/config.yml`.
- Required keys for ClickHouse: `clickhouse.host`, `clickhouse.port`, `clickhouse.user`, `clickhouse.password`, `clickhouse.database`, `clickhouse.cluster`.
  - The DB user should be read‑only; server enforces queries to `system.*` tables only.
- Required keys for Prometheus: `prometheus.host`, `prometheus.port`.

### Victoria Metrics from Kubernetes

If you need to expose Victoria Metrics from Kubernetes locally:

```bash
kubectl port-forward --namespace=monitoring svc/vmcluster-victoria-metrics-cluster-vmselect  8481:8481
```


## Running (stdio)

The server uses the official go-sdk and speaks MCP over stdio (JSON-RPC framed with Content-Length), suitable for clients like Claude Desktop.

- **Default mode**: `./housekeeper` (runs as MCP server)
- **Analysis mode**: `./housekeeper --analyze` (runs Gemini AI analysis)
- Methods implemented:
  - `initialize`
  - `tools/list`
  - `tools/call`

## Tools

### Tool: clickhouse_query

- Name: `clickhouse_query`
- Description: Query ClickHouse tables via `clusterAllReplicas` (read‑only) from configured allowed databases.
- Arguments (two modes):
  - Structured: `table` (required, must be from allowed databases), `columns`[], `where`, `order_by`, `limit`.
  - Free-form: `sql` (string) — must be a single SELECT/WITH statement referencing only tables from allowed databases. Semicolons and write/DDL are rejected.
- Allowed databases: Configured via `--ch-allowed-databases` flag or `clickhouse.allowed_databases` in config (defaults to ["system"])

### Tool: prometheus_query

- Name: `prometheus_query`
- Description: Execute PromQL queries against Prometheus metrics.
- Arguments:
  - `query` (required): PromQL query string
  - `start` (optional): Start time in RFC3339 format or relative time (e.g. "-1h")
  - `end` (optional): End time in RFC3339 format or relative time (e.g. "-1h")
  - `step` (optional): Step duration (e.g. "15s", "1m", "1h") (default: "1m")

## Example tools/call

Request:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "clickhouse_query",
    "arguments": {
      "table": "system.query_log",
      "columns": ["query", "query_duration_ms", "memory_usage"],
      "where": "event_time > subtractHours(now(), 1) AND query_duration_ms > 1000",
      "order_by": "query_duration_ms DESC",
      "limit": 10
    }
  }
}
```

Response (truncated):
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [
      {"type": "json", "data": {"results": [{"query": "..."}], "count": 10}}
    ]
  }
}
```

### Free-form example (ClickHouse)

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "clickhouse_query",
    "arguments": {
      "sql": "WITH slow AS (SELECT event_time, query_duration_ms FROM clusterAllReplicas(default, system.query_log) WHERE event_time > subtractHours(now(),1)) SELECT count() AS cnt, quantileExact(0.95)(query_duration_ms) AS p95 FROM slow"
    }
  }
}
```

### Prometheus example (range query)

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "tools/call",
  "params": {
    "name": "prometheus_query",
    "arguments": {
      "query": "rate(clickhouse_query_duration_ms_sum[5m])",
      "start": "-1h",
      "end": "-10m",
      "step": "1m"
    }
  }
}
```

## Claude Desktop Integration

### Quick Setup (After go install)

1. Install the housekeeper binary:
```bash
go install github.com/PostHog/housekeeper@latest
```

2. Find your Claude Desktop config file:
   - macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
   - Windows: `%APPDATA%\Claude\claude_desktop_config.json`
   - Linux: `~/.config/claude/claude_desktop_config.json`

3. Add to your Claude Desktop config:

```json
{
  "mcpServers": {
    "clickhouse-local": {
      "command": "housekeeper",
      "args": [
        "--ch-host", "127.0.0.1",
        "--ch-port", "9000",
        "--ch-user", "default",
        "--ch-password", "your-password",
        "--ch-database", "default",
        "--ch-cluster", "default",
        "--prom-host", "localhost",
        "--prom-port", "8481"
      ]
    }
  }
}
```

### Alternative: Using absolute path

If `housekeeper` is not in your PATH, use the absolute path:

```json
{
  "mcpServers": {
    "clickhouse-local": {
      "command": "/Users/yourusername/go/bin/housekeeper",
      "args": [
        "--ch-host", "127.0.0.1",
        "--ch-port", "9000",
        "--ch-user", "default",
        "--ch-password", "your-password",
        "--ch-database", "default",
        "--ch-cluster", "default"
      ]
    }
  }
}
```

### Using with config file

If you prefer using a config file:

```json
{
  "mcpServers": {
    "clickhouse-prod": {
      "command": "housekeeper",
      "args": ["--config", "/path/to/your/config.yml"]
    }
  }
}
```

4. Restart Claude Desktop for the changes to take effect.

## Notes

- Queries are restricted to `system.*` tables and reject multi‑statement inputs.
- The server uses `clusterAllReplicas(<cluster>, <system.table>)` for cluster‑wide visibility.
- If building fails initially, run `go mod tidy` to fetch `github.com/modelcontextprotocol/go-sdk`.
- The DB user should be read‑only for security.
