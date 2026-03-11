# Housekeeper - MCP Server for ClickHouse & Prometheus

**An MCP (Model Context Protocol) server that provides AI assistants with direct access to ClickHouse system tables and Prometheus metrics.**

Housekeeper is an MCP-first tool designed to empower AI assistants like Claude with the ability to query and analyze your ClickHouse clusters and Prometheus metrics in real-time. It exposes read-only access to system tables and metrics, enabling sophisticated analysis, troubleshooting, and monitoring directly through AI conversations.

## 🎯 Primary Use Case: MCP Server

Housekeeper runs as an MCP server by default, providing tools for:
- **ClickHouse Queries**: Read-only access to configurable databases (defaults to `system.*` tables)
- **Prometheus/Victoria Metrics**: Execute PromQL queries for metrics correlation and analysis
- **Smart Cluster Querying**: Automatic use of `clusterAllReplicas()` for system tables only (non-system tables are queried directly)

---

## 🚀 Quick Start with Claude Desktop

There are two ways to connect Housekeeper to Claude Desktop.

### Option A — Local process (stdio, simplest)

Claude Desktop launches Housekeeper directly as a child process. No network exposure needed.

1. **Install:**
```bash
go install github.com/PostHog/housekeeper@latest
# or build from source: go build -o housekeeper
```

2. **Configure** (`~/Library/Application Support/Claude/claude_desktop_config.json`):
```json
{
  "mcpServers": {
    "housekeeper": {
      "command": "housekeeper",
      "args": [
        "--ch-host", "your-clickhouse-host",
        "--ch-port", "9000",
        "--ch-user", "default",
        "--ch-password", "your-password",
        "--ch-database", "default",
        "--ch-cluster", "default",
        "--ch-allowed-databases", "system,models",
        "--prom-host", "localhost",
        "--prom-port", "8481"
      ]
    }
  }
}
```

3. **Restart Claude Desktop** and start querying.

---

### Option B — HTTP server + mcp-remote (Docker / Kubernetes)

Run Housekeeper as an HTTP MCP server and connect Claude Desktop to it via [mcp-remote](https://github.com/geelen/mcp-remote). This is the recommended approach when running in Docker or Kubernetes.

1. **Start Housekeeper** (pick one):

```bash
# Config file (an example can be found at configs/config.yml.sample)
docker run -p 8080:8080 \
  -v $(pwd)/configs/config.yml:/etc/housekeeper/config.yml \
  ghcr.io/posthog/housekeeper:latest

# Or directly with Go
housekeeper --http --ch-host your-clickhouse-host --ch-password your-password
```

2. **Configure Claude Desktop** (`~/Library/Application Support/Claude/claude_desktop_config.json`):
```json
{
  "mcpServers": {
    "housekeeper": {
      "command": "npx",
      "args": ["mcp-remote", "http://localhost:8080"],
      "env": {
        "PATH": "/path/to/node/v20+/bin:/usr/local/bin:/usr/bin:/bin"
      }
    }
  }
}
```

> **Note:** mcp-remote requires Node.js v20+. If you manage Node with nvm, hardcode the path to avoid Claude Desktop picking up an older version (e.g. `/Users/you/.nvm/versions/node/v24.x.x/bin`). The `PATH` env override ensures all child processes also use the right version.

3. **Restart Claude Desktop** and start querying.

---

## 📚 MCP Tools Available

### `clickhouse_query`
Query ClickHouse tables from allowed databases with two modes:
- **Structured**: Specify table, columns, filters, ordering, and limits
- **Free-form SQL**: Write custom queries (restricted to allowed databases)

Example questions you can ask Claude:
- "Show me the slowest queries from the last hour"
- "What tables are using the most disk space?"
- "Find all failed queries with their error messages"
- "Show me the current running queries across all nodes"

### `prometheus_query`
Execute PromQL queries for metrics analysis:
- Range queries with customizable time windows
- Instant queries for current values
- Support for Victoria Metrics cluster mode

Example requests:
- "What's the current query rate per second?"
- "Show me memory usage trends for the past hour"
- "Find nodes with high CPU usage"

---

## 🐳 Running with Docker

```bash
# Build the image
docker build -t housekeeper .

# Run with a config file
docker run -p 8080:8080 \
  -v $(pwd)/configs/config.yml:/etc/housekeeper/config.yml \
  housekeeper --config /etc/housekeeper/config.yml
```

The container starts in HTTP mode by default (`ENTRYPOINT ["/housekeeper", "--http"]`). The health check endpoint is available at `GET /health`.

## ⚙️ Configuration

### Command-Line Flags
```bash
housekeeper \
  --http \
  --http-addr ":8080" \
  --http-auth-token "your-secret-token" \
  --ch-host "127.0.0.1" \
  --ch-port 9000 \
  --ch-user "default" \
  --ch-password "password" \
  --ch-database "default" \
  --ch-cluster "cluster_name" \
  --ch-allowed-databases "system,models" \
  --prom-host "localhost" \
  --prom-port 8481
```

### Configuration File
Copy `configs/config.yml.sample` to `configs/config.yml` and fill in your values:

```yaml
clickhouse:
  host: "127.0.0.1"
  port: 9000
  user: "default"
  password: "password"
  database: "default"
  cluster: "cluster_name"
  allowed_databases:
    - "system"
    - "models"
prometheus:
  host: "localhost"
  port: 8481
http:
  enabled: true
  addr: ":8080"
  auth_token: "your-secret-token"
```

Then run:
```bash
housekeeper --config configs/config.yml
```

## 🚀 Advanced Features

### Victoria Metrics Cluster Mode
```bash
housekeeper \
  --prom-vm-cluster \
  --prom-vm-tenant "0" \
  --prom-vm-prefix "select/0/prometheus"
```

### Kubernetes Port Forwarding
```bash
# Forward Victoria Metrics from K8s
kubectl port-forward --namespace=monitoring \
  svc/vmcluster-victoria-metrics-cluster-vmselect 8481:8481
```

Now configure the Prometheus host and port in your `config.yml`:

```yaml
prometheus:
  host: "localhost"
  port: 8481
```

## 📊 Alternative: Analysis Mode

Housekeeper also includes an AI-powered analysis mode for automated monitoring and alerting:

```bash
# Run error analysis with Gemini AI
housekeeper --analyze

# Run performance analysis
housekeeper --analyze --performance
```

This mode:
- Queries recent errors from ClickHouse
- Analyzes patterns using Google Gemini AI
- Generates Slack-ready summaries
- Requires `gemini_key` in config

### Analysis Mode Configuration
```yaml
gemini_key: "your-gemini-api-key"
clickhouse:
  # ... same as above
```

## 🔒 Security Notes

- **Read-Only Access**: MCP server enforces read-only queries to configured databases
- **No DDL Operations**: Write operations and DDL statements are blocked
- **Bearer Auth**: Protect the HTTP endpoint with `--http-auth-token` in production
- **Credential Safety**: Never commit `configs/config.yml` (it's in `.gitignore`)

---

## 📁 Project Structure

```
.
├── main.go                  # Entry point, flag definitions
├── sdk_mcp.go               # HTTP MCP server, middlewares
├── clickhouse_mcp.go        # ClickHouse query validation and execution
├── prometheus_mcp.go        # Prometheus/Victoria Metrics client
├── clickhouse.go            # ClickHouse connection (analysis mode)
├── agent.go                 # Gemini AI integration (analysis mode)
├── slack.go                 # Slack notifications (analysis mode)
├── config.go                # Config loading and logging setup
├── Dockerfile               # Multi-stage build → distroless runtime
├── docker-compose.yml       # Local ClickHouse for development
├── chart/                   # Helm chart for Kubernetes
└── configs/
    └── config.yml.sample    # Configuration template
```

## 🤝 Contributing

We welcome contributions! Key areas:
- Additional MCP tools for ClickHouse operations
- Enhanced Prometheus/Victoria Metrics support
- Improved error handling and validation
- Documentation and examples

## 📄 License

MIT License - see [LICENSE.md](LICENSE.md) for details
