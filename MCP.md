# ClickHouse MCP Server

This repository includes a minimal MCP (Model Context Protocol) server that exposes a single tool for read‑only queries against ClickHouse system tables.

## Build

```bash
# Build integrated binary
go build -o housekeeper

```

## Configuration

- Uses `configs/config.yml` (Viper) — copy and edit `configs/config.yml.sample`.
- You can point to a custom path with `-config /path/to/config.yml` or env `HOUSEKEEPER_CONFIG=/path/to/config.yml`.
- Required keys: `clickhouse.host`, `clickhouse.port`, `clickhouse.user`, `clickhouse.password`, `clickhouse.database`, `clickhouse.cluster`.
- The DB user should be read‑only; server enforces queries to `system.*` tables only.

## Running (stdio)

The server uses the official go-sdk and speaks MCP over stdio (JSON-RPC framed with Content-Length), suitable for clients like Claude Desktop.

- Command (integrated): `./housekeeper -mcp`
- Methods implemented:
  - `initialize`
  - `tools/list`
  - `tools/call`

## Tool: clickhouse_query

- Name: `clickhouse_query`
- Description: Query ClickHouse system tables via `clusterAllReplicas` (read‑only).
- Arguments (two modes):
  - Structured: `table` (required, system.*), `columns`[], `where`, `order_by`, `limit`.
  - Free-form: `sql` (string) — must be a single SELECT/WITH statement referencing only `system.*` tables. Semicolons and write/DDL are rejected.

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

### Free-form example

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

## Claude Desktop integration (example)

Add to your Claude Desktop config:
```json
{
  "mcpServers": {
    "housekeeper-clickhouse": {
      "command": "/absolute/path/to/housekeeper",
      "args": ["-mcp"]
    },
    "housekeeper-clickhouse-standalone": {
      "command": "/absolute/path/to/clickhouse-mcp",
      "args": []
    }
  }
}
```

Notes:
- Queries are restricted to `system.*` tables and reject multi‑statement inputs.
- The server uses `clusterAllReplicas(<cluster>, <system.table>)` for cluster‑wide visibility.
- If building fails initially, run `go mod tidy` to fetch `github.com/modelcontextprotocol/go-sdk`.
