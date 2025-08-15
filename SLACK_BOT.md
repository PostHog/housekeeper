# Housekeeper Slack Bot

Transform your Slack workspace into a powerful ClickHouse and Prometheus query interface powered by AI. The Housekeeper Slack Bot uses natural language processing to convert your questions into database queries, making complex system analysis as simple as asking a question.

## Overview

The Slack bot mode (`--slack-bot`) creates an interactive bot that:
- Listens for mentions and direct messages in Slack
- Uses AI (Gemini, Claude, or GPT-4) to understand natural language queries
- Connects to the MCP server to execute ClickHouse and Prometheus queries
- Returns formatted, readable results directly in Slack threads

## Architecture

```
User in Slack → Bot receives message → LLM interprets query → 
MCP Client calls housekeeper → Results formatted by LLM → Reply in Slack thread
```

## Quick Start

### 1. Create a Slack App

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and click "Create New App"
2. Choose "From scratch"
3. Name your app (e.g., "Housekeeper") and select your workspace

### 2. Configure OAuth & Permissions

Navigate to "OAuth & Permissions" and add these Bot Token Scopes:
- `app_mentions:read` - React to @mentions
- `channels:history` - Read messages in public channels
- `chat:write` - Send messages
- `groups:history` - Read messages in private channels (optional)
- `im:history` - Read direct messages (optional)
- `mpim:history` - Read group DMs (optional)

Install the app to your workspace and copy the **Bot User OAuth Token** (starts with `xoxb-`).

### 3. Enable Socket Mode

1. Navigate to "Socket Mode" and enable it
2. Generate an **App-Level Token** with `connections:write` scope
3. Copy the token (starts with `xapp-`)

### 4. Subscribe to Events

Navigate to "Event Subscriptions" and subscribe to:
- `app_mention` - When someone mentions your bot
- `message.channels` - Messages in public channels (optional)
- `message.groups` - Messages in private channels (optional)
- `message.im` - Direct messages (optional)

### 5. Configure Housekeeper

Update your `configs/config.yml`:

```yaml
# Slack bot configuration
slack:
  bot_token: "xoxb-your-bot-token"
  app_token: "xapp-your-app-token"
  debug: false

# Choose your LLM provider
llm:
  provider: "gemini"  # or "claude" or "openai"
  
  gemini:
    api_key: "YOUR_GEMINI_KEY"
    model: "gemini-1.5-flash"
  
  claude:
    api_key: "YOUR_CLAUDE_KEY"
    model: "claude-3-5-sonnet-20241022"
  
  openai:
    api_key: "YOUR_OPENAI_KEY"
    model: "gpt-4-turbo-preview"

# ClickHouse configuration
clickhouse:
  host: "127.0.0.1"
  port: 9000
  user: "default"
  password: "your-password"
  database: "default"
  cluster: "default"

# Prometheus configuration
prometheus:
  host: "localhost"
  port: 8481
```

### 6. Run the Bot

```bash
# With config file
housekeeper --slack-bot --config configs/config.yml

# Or with command-line flags
housekeeper --slack-bot \
  --ch-host "127.0.0.1" \
  --ch-port 9000 \
  --ch-user "default" \
  --ch-password "password"
```

## Usage Examples

Once the bot is running, you can interact with it in Slack:

### Mention the Bot
```
@housekeeper what are the slowest queries from the last hour?
@housekeeper show me tables using the most disk space
@housekeeper find all failed queries with error messages
```

### Continue in Threads
The bot tracks conversations, so you can follow up in the same thread:
```
User: @housekeeper show me query performance
Bot: [Returns performance metrics]
User: what about memory usage for those queries?
Bot: [Provides memory analysis for the same queries]
```

### Slash Commands (Optional)
If you configure slash commands:
```
/clickhouse show running queries
/prometheus cpu usage last hour
```

## Supported Query Types

### ClickHouse Queries
- System performance metrics
- Query logs and analysis
- Table statistics and disk usage
- Error logs and debugging
- Cluster health monitoring

### Prometheus/Victoria Metrics Queries
- Real-time metrics
- Rate calculations
- Time-series analysis
- Alert condition checks
- Resource utilization

## LLM Provider Comparison

| Provider | Best For | Speed | Cost | Quality |
|----------|----------|-------|------|---------|
| **Gemini** | General use, fast responses | Fast | Low | Good |
| **Claude** | Complex queries, nuanced understanding | Medium | Medium | Excellent |
| **GPT-4** | Sophisticated analysis, formatting | Medium | High | Excellent |

## Advanced Configuration

### Custom System Prompts
You can customize how the LLM interprets queries by modifying the prompts in `llm.go`.

### Rate Limiting
Consider implementing rate limiting to prevent abuse:
- Per-user query limits
- Cooldown periods
- Query complexity limits

### Security Considerations
- The bot has read-only access to system tables
- Store API keys securely (use environment variables in production)
- Limit bot access to specific channels
- Consider implementing user allowlists

## Troubleshooting

### Bot Not Responding
1. Check Socket Mode is enabled in your Slack app
2. Verify both tokens are correctly configured
3. Check logs for connection errors: `housekeeper --slack-bot --config configs/config.yml`

### Query Errors
1. Verify MCP server can connect to ClickHouse/Prometheus
2. Check the bot has appropriate permissions
3. Review LLM provider API limits

### Formatting Issues
- Some LLM providers handle markdown better than others
- Adjust the formatting prompts in `llm.go` if needed

## Environment Variables

You can use environment variables instead of config file:

```bash
export SLACK_BOT_TOKEN="xoxb-..."
export SLACK_APP_TOKEN="xapp-..."
export LLM_PROVIDER="gemini"
export GEMINI_API_KEY="..."
export CLICKHOUSE_HOST="127.0.0.1"
export CLICKHOUSE_PASSWORD="..."

housekeeper --slack-bot
```

## Development Tips

### Testing Locally
1. Use ngrok for local development (not needed with Socket Mode)
2. Enable debug mode: `slack.debug: true`
3. Test with direct messages first

### Adding Custom Tools
The MCP server exposes tools that the bot can use. To add new tools:
1. Implement them in `mcp_server.go`
2. Update the tool descriptions
3. The LLM will automatically discover and use them

### Monitoring
- Track query latency
- Monitor API usage for cost management
- Log failed queries for improvement

## Example Conversations

### Performance Analysis
```
User: @housekeeper what queries are taking the longest?
Bot: I found 5 slow queries in the last hour:
1. SELECT count() FROM events... (3,245ms)
2. INSERT INTO analytics... (2,890ms)
...

User: why is the first one so slow?
Bot: The SELECT count() query is scanning 45M rows without using any indexes...
```

### System Health
```
User: @housekeeper is the cluster healthy?
Bot: Cluster health summary:
✅ All 3 nodes are responsive
📊 Current load: 45% CPU, 62% Memory
⚠️ Node 2 has higher than average query queue (15 pending)
```

## Contributing

Improvements welcome! Key areas:
- Additional LLM providers (Ollama, Anthropic, etc.)
- Rich Slack formatting (buttons, dropdowns)
- Query result caching
- Query history and analytics
- Multi-workspace support