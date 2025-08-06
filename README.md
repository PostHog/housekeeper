# Housekeeper

A Go application that monitors and analyzes ClickHouse database errors using Google's Gemini AI to generate intelligent summaries for Slack notifications.

## Overview

Housekeeper automatically queries system errors from ClickHouse clusters, analyzes patterns using AI, and produces concise, actionable summaries suitable for team notifications. It focuses on recent errors (past hour) to keep alerts relevant and timely.

## Features

- **Cluster-aware monitoring**: Queries all replicas in a ClickHouse cluster
- **AI-powered analysis**: Uses Google Gemini to identify patterns and severity
- **Time-based filtering**: Focuses on errors from the last hour
- **Slack-ready output**: Generates formatted summaries ideal for team notifications
- **Configurable**: Flexible YAML-based configuration

## Installation

### Prerequisites

- Go 1.19 or higher
- Access to a ClickHouse database
- Google Gemini API key

### Build from source

```bash
git clone https://github.com/fuziontech/housekeeper.git
cd housekeeper
go mod download
go build -o housekeeper
```

## Configuration

1. Copy the sample configuration:
```bash
cp configs/config.yml.sample configs/config.yml
```

2. Edit `configs/config.yml` with your settings:
```yaml
gemini_api_key: "your-gemini-api-key"
clickhouse:
  host: "localhost"
  port: 9000
  user: "default"
  password: ""
  database: "default"
  cluster: "your-cluster-name"
```

## Usage

### Run the application

```bash
# Using default config location
./housekeeper

# With custom config
./housekeeper -c /path/to/config.yml
```

### Development

```bash
# Run directly with Go
go run .

# Start local ClickHouse for testing
docker-compose up -d

# View ClickHouse logs
docker-compose logs clickhouse

# Stop local ClickHouse
docker-compose down
```

## How It Works

1. **Connect**: Establishes connection to ClickHouse cluster
2. **Query**: Retrieves system errors from all replicas for the past hour
3. **Analyze**: Sends error data to Gemini AI for pattern recognition
4. **Summarize**: Generates a concise summary with severity assessment
5. **Output**: Displays Slack-formatted message ready for posting

## Output Example

The application generates summaries like:

```
🔍 *ClickHouse Error Summary*
• Found 15 authentication failures from IP 192.168.1.100
• Detected 3 query timeout errors in analytics queries
• Overall severity: Medium
• Recommended action: Review authentication logs and optimize slow queries
```

## Project Structure

```
.
├── main.go          # Application entry point and orchestration
├── config.go        # Configuration management
├── clickhouse.go    # ClickHouse connection and queries
├── gemini.go        # AI integration for error analysis
├── configs/
│   └── config.yml.sample  # Configuration template
└── docker-compose.yml     # Local ClickHouse setup
```

## Security Notes

- Never commit `configs/config.yml` (it's in `.gitignore`)
- Store API keys securely
- Use environment variables for sensitive data in production

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

MIT License - see [LICENSE.md](LICENSE.md) for details
