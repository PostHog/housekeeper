# Repository Guidelines

## Project Structure & Module Organization
- Root Go module; entrypoint: `main.go`.
- Core packages: `agent.go` (Gemini analysis + tools), `clickhouse.go` (CH queries), `config.go` (Viper config), `slack.go` (Slack webhook).
- Configs in `configs/` (`config.yml.sample`, user `config.yml`).
- Local infra: `docker-compose.yml`, `docker/` (ClickHouse volumes).

## Build, Test, and Development Commands
- Build: `go build -o housekeeper` — compiles the CLI binary.
- Run (errors mode): `./housekeeper` — analyzes recent CH errors and prints a Slack-formatted summary.
- Run (performance mode): `./housekeeper -performance` — analyzes recent slow queries.
- Dev run: `go run .` (supports `-performance`).
- Local ClickHouse: `docker-compose up -d` / teardown: `docker-compose down`.

## Coding Style & Naming Conventions
- Go 1.23+ module; format with `gofmt -s -w .` and vet with `go vet ./...`.
- Naming: exported identifiers use `CamelCase`; unexported use `lowerCamel`.
- Files group by responsibility (agent, data access, config, integrations). Keep functions small and composable.
- Configuration keys mirror `configs/config.yml` (e.g., `clickhouse.host`, `gemini_key`, `slack.webhook_url`).

## Testing Guidelines
- Current repo has no `_test.go` files. Add table-driven tests alongside source (e.g., `agent_test.go`).
- Run tests: `go test ./...` (add `-v -race` when relevant). Aim for coverage on parsing, formatting, and query builders.
- Prefer dependency seams for external calls (ClickHouse, Slack, Gemini) to allow mocking.

## Commit & Pull Request Guidelines
- Commits: imperative, concise, present tense (e.g., "add performance agent hints"). Group logical changes; avoid noise.
- PRs include: clear description, rationale, test plan/steps, config changes, and any screenshots/log snippets of output.
- Link related issues; call out backward-incompatible changes and ops impacts.

## Security & Configuration Tips
- Do not commit secrets. Copy `configs/config.yml.sample` to `configs/config.yml` and keep it local (gitignored).
- Required fields: `gemini_key`, `slack.webhook_url`, and `clickhouse.*` (including `cluster`).
- Validate connectivity before large queries; prefer least-privileged DB credentials.
