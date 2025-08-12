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

## Claude Desktop integration (examples)

Option 1: stdio transport (spawns server process)
```json
{
  "mcpServers": {
    "housekeeper-clickhouse": {
      "command": "/absolute/path/to/housekeeper",
      "args": ["-mcp", "-config", "/absolute/path/to/config.yml"]
    }
  }
}
```

Option 2: SSE transport (connects to running server)
```json
{
  "mcpServers": {
    "housekeeper-clickhouse": {
      "transport": "sse",
      "url": "http://localhost:3333/sse"
    }
  }
}
```

Notes:
- Queries are restricted to `system.*` tables and reject multi‑statement inputs.
- The server uses `clusterAllReplicas(<cluster>, <system.table>)` for cluster‑wide visibility.
- If building fails initially, run `go mod tidy` to fetch `github.com/modelcontextprotocol/go-sdk`.

## Running (SSE)

Starts an HTTP server that implements the MCP SSE transport using the go-sdk.

- Start: `./housekeeper -sse -config /absolute/path/to/config.yml`
- Port: configured via `sse.port` (default `3333`).
- HTTPS: enable via `sse.tls.enabled: true`; by default a self-signed cert is generated and served on `sse.tls.port` (default `3443`). Provide `sse.tls.cert_file` and `sse.tls.key_file` to use your own certificate.
- Endpoints:
  - `GET /sse`: establishes the SSE stream; first event is `endpoint` with the per-session POST URL.
  - `POST <endpoint>`: send client→server JSON-RPC messages.
  - `GET /healthz`: health check (200 OK).

Quick test (manual):

```bash
# 1) Open SSE stream
curl -N -H 'Accept: text/event-stream' http://localhost:3333/sse
# You’ll receive an "endpoint" event like: data: /sse?sessionid=abc123

# 2) In another terminal, POST a JSON-RPC message to the session endpoint
curl -X POST http://localhost:3333/sse?sessionid=abc123 \
  -H 'Content-Type: application/json' \
  -d '{
        "jsonrpc":"2.0",
        "id":1,
        "method":"initialize",
        "params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0.0.1"}}
      }'

# HTTPS (self-signed): add -k
curl -k -N -H 'Accept: text/event-stream' https://localhost:3443/sse
curl -k -X POST https://localhost:3443/sse?sessionid=abc123 \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}'
```
