# Repository Guidelines

## Project Structure & Module Organization
- Root Go module; entrypoint: `main.go` (MCP server by default; `--analyze` runs the legacy Gemini mode).
- MCP server: `sdk_mcp.go` (server, transport, middleware), `clickhouse_mcp.go` (validation + query execution), `prometheus_mcp.go` (PromQL tools), `diagnose_mcp.go` + `bedrock.go` (optional in-account diagnose agent).
- Legacy analysis mode: `agent.go` (Gemini), `clickhouse.go` (error queries), `slack.go` (webhook).
- Configs in `configs/` (`config.yml.sample` template; user `config.yml` gitignored).
- Local infra: `docker-compose.yml` (ClickHouse). PostHog's Kubernetes deployment config lives in private internal repos.

## Build, Test, and Development Commands
- Build: `go build -o housekeeper`.
- Run MCP server (default): `go run .` — serves streamable HTTP MCP on `http.addr` (default `:8080`), health at `/health`.
- Run with config: `go run . --config configs/config.yml`.
- Legacy analysis: `go run . --analyze` (errors) / `--analyze --performance` (slow queries); requires `gemini_key`.
- Local ClickHouse: `docker-compose up -d` / `docker-compose down`.

## Coding Style & Naming Conventions
- Go 1.23+ module; format with `gofmt -s -w .` and vet with `go vet ./...`.
- Naming: exported identifiers use `CamelCase`; unexported use `lowerCamel`.
- Files group by responsibility (MCP tools, data access, config, integrations). Keep functions small and composable.
- Configuration keys mirror `configs/config.yml.sample` (e.g., `clickhouse.host`, `bedrock.model_id`, `mcp.extra_tool_description`). Env vars use the `HOUSEKEEPER_` prefix with dots as underscores.

## Testing Guidelines
- Table-driven tests live alongside source: `clickhouse_mcp_test.go` (SQL validator — extend this for ANY validator change), `clickhouse_test.go`, `prometheus_mcp_test.go`.
- Run tests: `go test ./...` (add `-v -race` when relevant).
- The free-form SQL validator is security-relevant defense-in-depth; add a test for every accepted/rejected shape you change. Server-side grants/REVOKEs remain the real boundary.

## Commit & Pull Request Guidelines
- Commits: imperative, concise, present tense (e.g., "add performance agent hints"). Group logical changes; avoid noise.
- PRs include: clear description, rationale, test plan/steps, config changes, and any screenshots/log snippets of output.
- Link related issues; call out backward-incompatible changes and ops impacts (tool descriptions in the charts repo are operator-facing surface).

## Security & Configuration Tips
- Do not commit secrets. Copy `configs/config.yml.sample` to `configs/config.yml` and keep it local (gitignored).
- MCP mode needs `clickhouse.*` (+ `prometheus.*`); the diagnose tool additionally needs `bedrock.region`, `bedrock.model_id`, and ideally a dedicated `analyst_clickhouse.*` user. `gemini_key`/`slack.webhook_url` are only for the legacy `--analyze` mode.
- Prefer least-privileged DB credentials; raw query text and customer tables belong only on the analyst connection.
