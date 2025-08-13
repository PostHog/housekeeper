# Housekeeper - MCP Server for ClickHouse & Prometheus

**An MCP (Model Context Protocol) server that provides AI assistants with direct access to ClickHouse system tables and Prometheus metrics.**

Housekeeper is an MCP-first tool designed to empower AI assistants like Claude with the ability to query and analyze your ClickHouse clusters and Prometheus metrics in real-time. It exposes read-only access to system tables and metrics, enabling sophisticated analysis, troubleshooting, and monitoring directly through AI conversations.

## 🎯 Primary Use Case: MCP Server

Housekeeper runs as an MCP server by default, providing tools for:
- **ClickHouse System Queries**: Read-only access to all `system.*` tables across your entire cluster
- **Prometheus/Victoria Metrics**: Execute PromQL queries for metrics correlation and analysis
- **Cluster-Wide Visibility**: Automatic use of `clusterAllReplicas()` for comprehensive insights

### Quick Start with Claude Desktop

1. **Install via Go:**
```bash
go install github.com/PostHog/housekeeper@latest
```

2. **Configure Claude Desktop** (`~/Library/Application Support/Claude/claude_desktop_config.json`):
```json
{
  "mcpServers": {
    "clickhouse": {
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

3. **Restart Claude Desktop** and start querying!

## 📚 MCP Tools Available

### `clickhouse_query`
Query ClickHouse system tables with two modes:
- **Structured**: Specify table, columns, filters, ordering, and limits
- **Free-form SQL**: Write custom queries (restricted to `system.*` tables)

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

## 🔧 Installation Options

### Via Go Install (Recommended)
```bash
go install github.com/PostHog/housekeeper@latest
```

### Build from Source
```bash
git clone https://github.com/PostHog/housekeeper.git
cd housekeeper
go build -o housekeeper
```

## ⚙️ Configuration

### Command-Line Flags (Recommended for MCP)
```bash
housekeeper \
  --ch-host "127.0.0.1" \
  --ch-port 9000 \
  --ch-user "default" \
  --ch-password "password" \
  --ch-database "default" \
  --ch-cluster "cluster_name" \
  --prom-host "localhost" \
  --prom-port 8481
```

### Configuration File
Create `configs/config.yml`:
```yaml
clickhouse:
  host: "127.0.0.1"
  port: 9000
  user: "default"
  password: "password"
  database: "default"
  cluster: "cluster_name"
prometheus:
  host: "localhost"
  port: 8481
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

# Run housekeeper
housekeeper --prom-host localhost --prom-port 8481
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
- Requires `gemini_api_key` in config

### Analysis Mode Configuration
```yaml
gemini_api_key: "your-gemini-api-key"
clickhouse:
  # ... same as above
```

## 🔒 Security Notes

- **Read-Only Access**: MCP server enforces read-only queries to `system.*` tables
- **No DDL Operations**: Write operations and DDL statements are blocked
- **Credential Safety**: Never commit configs with passwords
- **Use Environment Variables**: For production deployments

## 📁 Project Structure

```
.
├── main.go              # Application entry point
├── mcp_server.go        # MCP server implementation
├── clickhouse.go        # ClickHouse connection logic
├── gemini.go            # Gemini AI integration (analysis mode)
├── prometheus.go        # Prometheus/Victoria Metrics client
├── MCP.md               # Detailed MCP documentation
└── configs/
    └── config.yml.sample  # Configuration template
```

## 🤝 Contributing

We welcome contributions! Key areas:
- Additional MCP tools for ClickHouse operations
- Enhanced Prometheus/Victoria Metrics support
- Improved error handling and validation
- Documentation and examples

## 📖 Documentation

- **[MCP.md](MCP.md)**: Complete MCP server documentation
- **[CLAUDE.md](CLAUDE.md)**: Project context for AI assistants

## 📄 License

MIT License - see [LICENSE.md](LICENSE.md) for details