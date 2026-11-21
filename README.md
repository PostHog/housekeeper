# Housekeeper - MCP Server for ClickHouse & Prometheus

**An MCP (Model Context Protocol) server that provides AI assistants with direct access to ClickHouse system tables and Prometheus metrics.**

Housekeeper runs as an HTTP MCP server, providing AI assistants like Claude with the ability to query and analyze your ClickHouse clusters and Prometheus metrics in real-time. It exposes read-only access to system tables and metrics, enabling sophisticated analysis, troubleshooting, and monitoring directly through AI conversations.

## 🎯 What it provides

- **ClickHouse Queries**: Read-only access to configurable databases (defaults to `system.*` tables)
- **Prometheus/Victoria Metrics**: Execute PromQL queries for metrics correlation and analysis
- **In-account diagnosis (optional)**: Bedrock-backed `clickhouse_diagnose` tools (synchronous, or async start/poll) that investigate server-side on an elevated connection and return only a summary
- **Smart Cluster Querying**: Automatic use of `clusterAllReplicas()` for system tables only (non-system tables are queried directly)

---

## 🚀 Quick Start with Claude Desktop

Housekeeper runs as an HTTP server. Connect Claude Desktop to it via [mcp-remote](https://github.com/geelen/mcp-remote).

### 1. Start Housekeeper

No public image is published (CI builds images for internal deployments), so build one locally first:

```bash
docker build -t housekeeper .

# With a config file (recommended — see configs/config.yml.sample)
docker run -p 8080:8080 \
  -v $(pwd)/configs/config.yml:/etc/housekeeper/config.yml \
  housekeeper --config /etc/housekeeper/config.yml

# Or with flags
docker run -p 8080:8080 housekeeper \
  --ch-host your-clickhouse-host \
  --ch-password your-password
```

Or skip Docker: `go install github.com/PostHog/housekeeper@latest`.

### 2. Configure Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json`:

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

### 3. Restart Claude Desktop and start querying!

---

## 📚 MCP Tools Available

### `clickhouse_query`
Query ClickHouse tables from allowed databases with two modes:
- **Structured**: Specify table, columns, filters, ordering, and limits
- **Free-form SQL**: Write custom queries (restricted to allowed databases)

Both modes go through the same validator: one SELECT/WITH statement, table references only as `db.table` in an allowed database or `clusterAllReplicas(cluster, db.table)`, and no table functions, comma joins, comments, backtick identifiers, `SETTINGS`, `FORMAT` or `INTO OUTFILE`. Results stop at `clickhouse.max_result_rows` (default 10000) with `truncated: true`, and each call is bounded by `clickhouse.query_timeout_seconds`.

Example questions you can ask Claude:
- "Show me the slowest queries from the last hour"
- "What tables are using the most disk space?"
- "Find all failed queries with their error messages"
- "Show me the current running queries across all nodes"

### `prometheus_query`
Execute PromQL queries for metrics analysis:
- Range queries with customizable time windows (`start` defaults to an hour before `end`, `step` to the window / 50)
- Capped at 11000 points per series, 500 series and 10000 raw samples (`truncated: true` when cut)
- Support for Victoria Metrics cluster mode

Example requests:
- "What's the current query rate per second?"
- "Show me memory usage trends for the past hour"
- "Find nodes with high CPU usage"

### `clickhouse_diagnose` (optional)
Ask a natural-language question ("why is X slow", "what's driving load") and a server-side Bedrock agent investigates via a guarded `run_sql` tool on a separate, elevated ClickHouse connection, returning only an attributed summary. Registered when `bedrock.region` + `bedrock.model_id` are set; credentials come from the default AWS chain. Bounded by `bedrock.max_iterations` round-trips and a wall-clock budget: callers pass `budget_seconds` (default `bedrock.default_seconds`, capped at `bedrock.max_seconds`), and when it runs out the agent summarizes what it found. Each `run_sql` query gets its own deadline, `bedrock.max_query_seconds` (default 60) or the time left, whichever is less. Query results reach the model wrapped as untrusted data. Make sure your MCP client's tool timeout exceeds the budget. See [MCP.md](./MCP.md) for details.

### `clickhouse_diagnose_async` / `clickhouse_diagnose_result` (optional)
The same investigation as a background job, for questions that need longer than your client's tool timeout. `clickhouse_diagnose_async` returns a `job_id` straight away (budget defaults to `bedrock.async_default_seconds`, capped at `bedrock.max_seconds`); poll `clickhouse_diagnose_result` with it every ~30s. At most `bedrock.max_concurrent_jobs` run at once, and finished results stay available for 30 minutes. Registered alongside `clickhouse_diagnose`.

---

## 🔍 Investigation Playbook

For diagnosing ClickHouse + ZooKeeper issues (replication anomalies, counter spikes, session expirations, readonly replicas) using the MCP tools above, see **[INVESTIGATION_PLAYBOOK.md](./INVESTIGATION_PLAYBOOK.md)**.

It captures a triage methodology and a set of `system.*` query patterns that resolve most common dead ends — particularly the "source code says impossible but data shows otherwise" trap.

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

The health check endpoint is available at `GET /health`.

## ⚙️ Configuration

### Command-Line Flags
```bash
housekeeper \
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
  # Victoria Metrics cluster mode (optional)
  vm_cluster_mode: false
  vm_tenant_id: "0"
  vm_path_prefix: ""
http:
  addr: ":8080"
  auth_token: "your-secret-token"
```

See [`configs/config.yml.sample`](configs/config.yml.sample) for the full set of options, including `logging`, the optional `mcp.extra_tool_description`, the `bedrock.*` diagnose settings and the `analyst_clickhouse.*` connection.

#### Rotating password file
`clickhouse.password_file` (and `analyst_clickhouse.password_file`) names a file holding a rotating token, such as a projected Kubernetes ServiceAccount token. It is re-read on every connect, so rotation needs no restart. An unreadable or empty file falls back to the static `password`, and so does an expired token when a `password` is set.

#### Diagnose budgets
| Key | Default | Meaning |
|---|---|---|
| `bedrock.max_iterations` | 8 | `run_sql` round-trips per diagnosis |
| `bedrock.max_seconds` | 25 | Wall-clock cap for any diagnosis; every `budget_seconds` is clamped to it (0 = no cap) |
| `bedrock.default_seconds` | 35 | `clickhouse_diagnose` budget when the caller omits `budget_seconds` |
| `bedrock.async_default_seconds` | 120 | `clickhouse_diagnose_async` budget when the caller omits `budget_seconds` |
| `bedrock.max_concurrent_jobs` | 3 | Async investigations allowed at once |

The defaults clamp both default budgets to the 25s cap. Raise `bedrock.max_seconds` to use longer budgets.

Then run:
```bash
docker run -p 8080:8080 \
  -v $(pwd)/configs/config.yml:/etc/housekeeper/config.yml \
  housekeeper --config /etc/housekeeper/config.yml
```

### Victoria Metrics Cluster Mode
```bash
docker run -p 8080:8080 housekeeper \
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

## 🔒 Security Notes

- **Read-Only**: SELECT-only at the SQL layer; DDL and writes are blocked. Server-side role/profile is the real boundary.
- **Authentication**: Set `--http-auth-token` (or `HOUSEKEEPER_HTTP_AUTH_TOKEN`) for bearer auth, or leave unset and front housekeeper with a network-level identity gate.
- **Limits**: give the ClickHouse users a profile that caps `max_execution_time`, `max_memory_usage` and `max_bytes_to_read` with a `max` and `<disallowed>0</disallowed>`. With `readonly=2` a query can change settings, and 0 lifts the limit.
- **TLS**: `clickhouse.secure` (default true) and `clickhouse.tls_skip_verify` (default true, the historic behavior). Set `ca_file` and `tls_skip_verify: false` to verify the server.
- **Credentials**: `configs/config*.yml` are gitignored. Only `*.example`/`*.sample` templates are tracked.

---

## 📁 Project Structure

```
.
├── main.go                  # Entry point, flag definitions
├── sdk_mcp.go               # HTTP MCP server, tool registration, middlewares
├── clickhouse_mcp.go        # ClickHouse query validation and execution
├── prometheus_mcp.go        # Prometheus/Victoria Metrics client
├── diagnose_mcp.go          # clickhouse_diagnose tool + analyst connection
├── diagnose_async_mcp.go    # clickhouse_diagnose_async / _result job tools
├── bedrock.go               # Bedrock Converse tool-use loop (default AWS credential chain)
├── clickhouse.go            # ClickHouse connection for clickhouse_query
├── clickhouse_auth.go       # Password resolution (rotating password_file + static fallback)
├── config.go                # Config loading and logging setup
├── *_test.go                # Tests (SQL validator, query building, prometheus, auth, driver decode)
├── Dockerfile               # Multi-stage build → distroless runtime
├── docker-compose.yml       # Local ClickHouse for development
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
