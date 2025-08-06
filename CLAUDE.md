# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Go application that monitors and analyzes ClickHouse database errors using Google's Gemini AI. The application queries system errors from the past hour across all cluster replicas and generates AI-powered summaries suitable for Slack notifications.

## Development Commands

### Running the Application
```bash
# Run the application
go run .

# Build the application
go build -o chore

# Run with custom config
go run . -c configs/config.yml
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
1. **main.go** - Orchestrates the workflow: loads config → connects to ClickHouse → queries errors → analyzes with Gemini → outputs summary
2. **config.go** - Manages configuration loading via Viper, expects YAML config at `configs/config.yml`
3. **clickhouse.go** - Handles ClickHouse connections and error queries across cluster replicas
4. **gemini.go** - Integrates with Google Gemini AI for error analysis

### Configuration Structure
The application expects a `configs/config.yml` file (copy from `configs/config.yml.sample`):
- `gemini_api_key`: Google Gemini API key
- `clickhouse`: Connection parameters including host, port, user, password, database, and cluster name

### Key Design Patterns
- **Cluster-aware querying**: Queries all replicas using `clusterAllReplicas()` function
- **Time-based filtering**: Only analyzes errors from the last hour to keep summaries relevant
- **AI prompt engineering**: Uses a specific prompt template to generate Slack-friendly summaries

## Important Notes

1. **No tests exist** - When adding features, consider creating a test suite
2. **Configuration security** - Ensure `configs/config.yml` remains in .gitignore
3. **ClickHouse connection** - Uses native protocol on port 9000 by default
4. **Error handling** - The application uses `log.Fatal()` for errors, which exits the program
5. **Local development** - Docker Compose provides a local ClickHouse instance with default credentials