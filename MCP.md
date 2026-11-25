# ClickHouse MCP Server Documentation

**Housekeeper runs as an MCP server by default** - no flags needed! It serves MCP over HTTP (streamable HTTP transport); connect a client such as Claude Desktop via [mcp-remote](https://github.com/geelen/mcp-remote). This document covers the complete MCP implementation that exposes tools for:
1. Read‑only queries against configurable ClickHouse databases
2. Querying Prometheus metrics for monitoring and correlation
3. (Optional) An in-account, Bedrock-backed diagnosis agent (`clickhouse_diagnose`, plus the async `clickhouse_diagnose_async` / `clickhouse_diagnose_result` pair)

> **Looking for investigation guidance?** See [INVESTIGATION_PLAYBOOK.md](./INVESTIGATION_PLAYBOOK.md) for a methodology and `system.*` query patterns for diagnosing ClickHouse + ZooKeeper issues using these tools.

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
  - The DB user should be read‑only; queries are restricted to the configured `clickhouse.allowed_databases` (defaults to `["system"]`).
- Required keys for Prometheus: `prometheus.host`, `prometheus.port`.
- Optional `clickhouse.password_file` / `analyst_clickhouse.password_file`: a file holding a rotating token (e.g. a projected ServiceAccount token), re-read on every connect. An unreadable or empty file falls back to the static `password`, as does an expired token when a `password` is set.

### Victoria Metrics from Kubernetes

If you need to expose Victoria Metrics from Kubernetes locally:

```bash
kubectl port-forward --namespace=monitoring svc/vmcluster-victoria-metrics-cluster-vmselect  8481:8481
```


## Running (HTTP)

The server uses the official go-sdk and serves MCP over HTTP using the streamable HTTP transport. It listens on `http.addr` (default `:8080`), exposes the MCP endpoint at `POST /`, and a `GET /health` check for liveness probes (no auth; returns a static `ok`). Connect stdio-only clients (e.g. Claude Desktop) via [mcp-remote](https://github.com/geelen/mcp-remote).

- Run `./housekeeper` (flags or `--config`); there is no other mode.
- Optional bearer auth: set `http.auth_token` (or `--http-auth-token`) and clients must present it.
- The server rejects cross-origin requests from browsers. A browser-based client must be served from the same origin as the server, or connect through a local proxy such as mcp-remote.
- `POST /` requires `Content-Type: application/json`. Other content types get `415 Unsupported Media Type`.
- Methods implemented:
  - `initialize`
  - `tools/list`
  - `tools/call`

## Tools

### Tool: clickhouse_query

- Name: `clickhouse_query`
- Description: Query ClickHouse tables (read‑only) from configured allowed databases.
- Arguments (two modes):
  - Structured: `table` (required, must be from allowed databases), `columns`[], `where`, `order_by`, `limit`.
  - Free-form: `sql` (string) — must be a single SELECT/WITH statement referencing only tables from allowed databases. Semicolons, write/DDL, table functions, comma joins, comments, backtick identifiers, `SETTINGS`, `FORMAT` and `INTO OUTFILE` are rejected. Structured queries are built and then run through the same check.
- Limits: at most `clickhouse.max_result_rows` rows (default 10000; the result carries `truncated: true` when cut), and `clickhouse.query_timeout_seconds` (default 90) per call.
- Allowed databases: Configured via `--ch-allowed-databases` flag or `clickhouse.allowed_databases` in config (defaults to ["system"])

**IMPORTANT Usage Guidelines:**
- **Structured mode auto-wraps `system.*`**: the tool converts `system.query_log` → `clusterAllReplicas(cluster, system.query_log)` for cluster-wide data. Non-system tables are queried directly. Note: on deployments with column REVOKEs on query-log tables, this forced wrap fails (a distributed read demands the whole-table grant) — use free-form `sql` with a plain single-node read for those tables.
- **Free-form mode wraps nothing** — choose per table: replicated tables (same data everywhere) → query directly to avoid duplicate rows; sharded tables and per-node `system.*` → wrap in `clusterAllReplicas('<cluster>', db.table)` to see everything. Check `system.tables.engine` if unsure.
- Only `db.table` and `clusterAllReplicas('cluster', db.table)` references are accepted; `cluster()` and `remote()` are blocked.

### Tool: prometheus_query

- Name: `prometheus_query`
- Description: Execute PromQL queries against Prometheus metrics.
- Arguments:
  - `query` (required): PromQL query string
  - `start` (optional): Start time in RFC3339 format or relative time (e.g. "-1h"); defaults to an hour before `end`
  - `end` (optional): End time in RFC3339 format or relative time (e.g. "-1h"); defaults to now
  - `step` (optional): Step duration (e.g. "15s", "1m", "1h"); defaults to the window / 50, at least 15s. A step ≤ 0, or one that gives more than 11000 points, is rejected.
- Results are capped at 100 series, 2000 raw samples and about 60 KB of JSON, with `truncated` and `series_total` in the result. Each call is bounded by `prometheus.query_timeout_seconds` (default 30).

### Tool: clickhouse_diagnose (optional)

- Name: `clickhouse_diagnose`
- Description: Ask a natural-language question about ClickHouse health; a server-side Bedrock agent (credentials from the default AWS chain) investigates with a guarded `run_sql` tool on the elevated `analyst_clickhouse.*` connection and returns only a text summary — raw query text never leaves the account.
- Registration: only exposed when both `bedrock.region` and `bedrock.model_id` are set.
- Arguments: `question` (required), `cluster` (optional focus hint), `budget_seconds` (optional wall-clock budget; default `bedrock.default_seconds`, clamped to `bedrock.max_seconds`).
- Budgets: at most `bedrock.max_iterations` run_sql round-trips, and the resolved `budget_seconds` of wall-clock time; when the time runs out the agent stops investigating and summarizes findings so far. The MCP client's tool timeout must exceed the budget (e.g. `MCP_TOOL_TIMEOUT` in Claude Code) or the client gives up while the server keeps working. The server sends an MCP progress notification per model turn when the client supplies a progress token, so clients that reset their timeout on progress can use longer budgets.

### Tool: clickhouse_diagnose_async / clickhouse_diagnose_result (optional)

- Registered together with `clickhouse_diagnose`.
- `clickhouse_diagnose_async` takes the same arguments and returns a `job_id` immediately. Its `budget_seconds` defaults to `bedrock.async_default_seconds`, clamped to `bedrock.max_seconds`. At most `bedrock.max_concurrent_jobs` diagnoses run at once, sync and async together; further starts are rejected until one finishes.
- `clickhouse_diagnose_result` takes `job_id` and returns `running` (elapsed seconds, model turn), `done` (the answer) or `error`. Poll every ~30s. Finished jobs expire after 30 minutes.
- Jobs live in server memory, so a restart (or a different replica behind a load balancer) loses them. On SIGTERM the server waits up to `http.shutdown_timeout_seconds` (default 25) for running jobs, then cancels them.

### Diagnose budget keys

| Key | Default | Meaning |
|---|---|---|
| `bedrock.max_iterations` | 8 | `run_sql` round-trips per diagnosis |
| `bedrock.max_seconds` | 25 | Wall-clock cap; every `budget_seconds` is clamped to it (0 = no cap) |
| `bedrock.default_seconds` | 35 | `clickhouse_diagnose` budget when `budget_seconds` is omitted |
| `bedrock.async_default_seconds` | 120 | `clickhouse_diagnose_async` budget when `budget_seconds` is omitted |
| `bedrock.max_concurrent_jobs` | 3 | Concurrent diagnoses, sync and async together |
| `bedrock.max_query_seconds` | 60 | Deadline per `run_sql` query, also bounded by the time left. The driver sends it + 5s as `max_execution_time`, so keep it at least 5s below the analyst profile's cap |

With the defaults, both default budgets are clamped to the 25s cap; raise `bedrock.max_seconds` to allow longer investigations.

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

Housekeeper serves MCP over HTTP, so Claude Desktop connects to it through [mcp-remote](https://github.com/geelen/mcp-remote) rather than spawning the binary directly.

### 1. Start the housekeeper server

Run it locally with flags:

```bash
housekeeper \
  --ch-host "127.0.0.1" \
  --ch-port 9000 \
  --ch-user "default" \
  --ch-password "your-password" \
  --ch-database "default" \
  --ch-cluster "default" \
  --prom-host "localhost" \
  --prom-port 8481
```

…or with a config file:

```bash
housekeeper --config /path/to/your/config.yml
```

This listens on `http.addr` (default `:8080`). It can also be a remote/deployed instance reachable over your network.

### 2. Point Claude Desktop at it

Find your Claude Desktop config file:
- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`
- Linux: `~/.config/claude/claude_desktop_config.json`

Add an `mcp-remote` entry pointing at the server's URL:

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

> **Note:** mcp-remote requires Node.js v20+. If you manage Node with nvm, hardcode the path so Claude Desktop doesn't pick up an older version. If the server has `http.auth_token` set, pass it via mcp-remote's `--header` argument (e.g. `"--header", "Authorization: Bearer <token>"`).

### 3. Restart Claude Desktop for the changes to take effect.

## Notes

- Queries are restricted to the configured `allowed_databases` (defaults to `system`) and reject multi‑statement inputs.
- For `system.*` tables the server uses `clusterAllReplicas(<cluster>, <system.table>)` for cluster‑wide visibility; non-system tables are queried directly.
- If building fails initially, run `go mod tidy` to fetch `github.com/modelcontextprotocol/go-sdk`.
- The DB user should be read‑only for security.
