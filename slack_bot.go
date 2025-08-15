package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/spf13/viper"
)

// SlackBot represents the Slack bot that interfaces with MCP
type SlackBot struct {
	client       *slack.Client
	socketClient *socketmode.Client
	mcpClient    *MCPClient
	llmProvider  LLMProvider
	
	// Track active conversations
	conversations sync.Map
}

// ConversationState tracks the state of a conversation thread
type ConversationState struct {
	ThreadTS string
	UserID   string
	LastQuery string
}

// NewSlackBot creates a new Slack bot instance
func NewSlackBot() (*SlackBot, error) {
	// Get Slack credentials
	botToken := viper.GetString("slack.bot_token")
	appToken := viper.GetString("slack.app_token")
	
	if botToken == "" || appToken == "" {
		return nil, fmt.Errorf("slack bot_token and app_token must be configured")
	}
	
	// Create Slack clients
	api := slack.New(
		botToken,
		slack.OptionDebug(viper.GetBool("slack.debug")),
		slack.OptionAppLevelToken(appToken),
	)
	
	socketClient := socketmode.New(
		api,
		socketmode.OptionDebug(viper.GetBool("slack.debug")),
	)
	
	// Create MCP client with connection parameters
	mcpArgs := buildMCPArgs()
	mcpClient, err := NewMCPClient(mcpArgs)
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP client: %w", err)
	}
	
	// Create LLM provider
	llmProvider, err := NewLLMProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM provider: %w", err)
	}
	
	return &SlackBot{
		client:       api,
		socketClient: socketClient,
		mcpClient:    mcpClient,
		llmProvider:  llmProvider,
	}, nil
}

// buildMCPArgs constructs the command-line arguments for the MCP server
func buildMCPArgs() []string {
	args := []string{}
	
	// Add ClickHouse parameters
	if host := viper.GetString("clickhouse.host"); host != "" {
		args = append(args, "--ch-host", host)
	}
	if port := viper.GetInt("clickhouse.port"); port > 0 {
		args = append(args, "--ch-port", fmt.Sprintf("%d", port))
	}
	if user := viper.GetString("clickhouse.user"); user != "" {
		args = append(args, "--ch-user", user)
	}
	if password := viper.GetString("clickhouse.password"); password != "" {
		args = append(args, "--ch-password", password)
	}
	if database := viper.GetString("clickhouse.database"); database != "" {
		args = append(args, "--ch-database", database)
	}
	if cluster := viper.GetString("clickhouse.cluster"); cluster != "" {
		args = append(args, "--ch-cluster", cluster)
	}
	
	// Add Prometheus parameters
	if host := viper.GetString("prometheus.host"); host != "" {
		args = append(args, "--prom-host", host)
	}
	if port := viper.GetInt("prometheus.port"); port > 0 {
		args = append(args, "--prom-port", fmt.Sprintf("%d", port))
	}
	if viper.GetBool("prometheus.vm_cluster_mode") {
		args = append(args, "--prom-vm-cluster")
	}
	if tenant := viper.GetString("prometheus.vm_tenant_id"); tenant != "" {
		args = append(args, "--prom-vm-tenant", tenant)
	}
	if prefix := viper.GetString("prometheus.vm_path_prefix"); prefix != "" {
		args = append(args, "--prom-vm-prefix", prefix)
	}
	
	return args
}

// Run starts the Slack bot and begins listening for events
func (bot *SlackBot) Run() error {
	go func() {
		for evt := range bot.socketClient.Events {
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				bot.handleEventsAPI(evt)
			case socketmode.EventTypeInteractive:
				bot.handleInteractive(evt)
			case socketmode.EventTypeSlashCommand:
				bot.handleSlashCommand(evt)
			case socketmode.EventTypeHello:
				logrus.Info("Slack bot connected successfully")
			default:
				logrus.WithField("type", evt.Type).Debug("Unhandled event type")
			}
		}
	}()
	
	logrus.Info("Starting Slack bot in Socket Mode")
	return bot.socketClient.Run()
}

// handleEventsAPI handles Events API events (messages, app mentions, etc.)
func (bot *SlackBot) handleEventsAPI(evt socketmode.Event) {
	eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		logrus.Error("Failed to cast event to EventsAPIEvent")
		return
	}
	
	// Acknowledge the event immediately
	bot.socketClient.Ack(*evt.Request)
	
	switch eventsAPIEvent.Type {
	case slackevents.CallbackEvent:
		innerEvent := eventsAPIEvent.InnerEvent
		switch ev := innerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			bot.handleMention(ev)
		case *slackevents.MessageEvent:
			// Only respond to thread messages where we're already engaged
			if ev.ThreadTimeStamp != "" {
				if _, exists := bot.conversations.Load(ev.ThreadTimeStamp); exists {
					bot.handleThreadMessage(ev)
				}
			}
		}
	}
}

// handleMention handles when the bot is mentioned
func (bot *SlackBot) handleMention(event *slackevents.AppMentionEvent) {
	// Extract the query by removing the bot mention
	query := bot.extractQuery(event.Text)
	
	if query == "" {
		bot.client.PostMessage(
			event.Channel,
			slack.MsgOptionText("Hi! Ask me about your ClickHouse cluster or Prometheus metrics. For example:\n• What are the slowest queries?\n• Show me error rates from the last hour\n• Check memory usage across nodes", false),
			slack.MsgOptionTS(event.TimeStamp),
		)
		return
	}
	
	// Store conversation state
	bot.conversations.Store(event.TimeStamp, &ConversationState{
		ThreadTS:  event.TimeStamp,
		UserID:    event.User,
		LastQuery: query,
	})
	
	// Process the query
	bot.processQuery(event.Channel, event.TimeStamp, query)
}

// handleThreadMessage handles messages in an existing thread
func (bot *SlackBot) handleThreadMessage(event *slackevents.MessageEvent) {
	// Don't respond to bot's own messages
	if event.User == "" || event.BotID != "" {
		return
	}
	
	query := strings.TrimSpace(event.Text)
	if query == "" {
		return
	}
	
	// Update conversation state
	if state, ok := bot.conversations.Load(event.ThreadTimeStamp); ok {
		convState := state.(*ConversationState)
		convState.LastQuery = query
	}
	
	// Process the query in thread
	bot.processQuery(event.Channel, event.ThreadTimeStamp, query)
}

// handleSlashCommand handles slash commands (e.g., /clickhouse)
func (bot *SlackBot) handleSlashCommand(evt socketmode.Event) {
	cmd, ok := evt.Data.(slack.SlashCommand)
	if !ok {
		logrus.Error("Failed to cast event to SlashCommand")
		return
	}
	
	// Acknowledge the command
	bot.socketClient.Ack(*evt.Request)
	
	query := strings.TrimSpace(cmd.Text)
	if query == "" {
		bot.client.PostEphemeral(
			cmd.ChannelID,
			cmd.UserID,
			slack.MsgOptionText("Please provide a query. Example: /clickhouse show slow queries", false),
		)
		return
	}
	
	// Post initial message and get timestamp
	resp, _, _, err := bot.client.SendMessage(
		cmd.ChannelID,
		slack.MsgOptionText(fmt.Sprintf("<@%s> asked: %s", cmd.UserID, query), false),
	)
	if err != nil {
		logrus.WithError(err).Error("Failed to post initial message")
		return
	}
	
	// Store conversation state
	bot.conversations.Store(resp, &ConversationState{
		ThreadTS:  resp,
		UserID:    cmd.UserID,
		LastQuery: query,
	})
	
	// Process the query
	bot.processQuery(cmd.ChannelID, resp, query)
}

// handleInteractive handles interactive components (buttons, select menus, etc.)
func (bot *SlackBot) handleInteractive(evt socketmode.Event) {
	interaction, ok := evt.Data.(slack.InteractionCallback)
	if !ok {
		logrus.Error("Failed to cast event to InteractionCallback")
		return
	}
	
	// Acknowledge the interaction
	bot.socketClient.Ack(*evt.Request)
	
	// Handle based on interaction type
	switch interaction.Type {
	case slack.InteractionTypeBlockActions:
		// Handle button clicks, etc.
		logrus.WithField("actions", interaction.ActionCallback.BlockActions).Debug("Block action received")
	}
}

// extractQuery removes the bot mention from the message text
func (bot *SlackBot) extractQuery(text string) string {
	// Remove bot mention (e.g., <@U12345>)
	parts := strings.Fields(text)
	filtered := []string{}
	for _, part := range parts {
		if !strings.HasPrefix(part, "<@") || !strings.HasSuffix(part, ">") {
			filtered = append(filtered, part)
		}
	}
	return strings.TrimSpace(strings.Join(filtered, " "))
}

// processQuery handles the actual query processing
func (bot *SlackBot) processQuery(channel, threadTS, query string) {
	// Post initial "thinking" message
	bot.client.PostMessage(
		channel,
		slack.MsgOptionText(":hourglass: Processing your query...", false),
		slack.MsgOptionTS(threadTS),
	)
	
	// Get available MCP tools
	tools := bot.mcpClient.GetTools()
	
	// Use LLM to convert query to MCP tool call
	toolCall, err := bot.llmProvider.GenerateMCPQuery(query, tools)
	if err != nil {
		logrus.WithError(err).Error("Failed to generate MCP query")
		bot.client.PostMessage(
			channel,
			slack.MsgOptionText(fmt.Sprintf(":x: Failed to understand query: %v", err), false),
			slack.MsgOptionTS(threadTS),
		)
		return
	}
	
	logrus.WithFields(logrus.Fields{
		"tool":      toolCall.ToolName,
		"arguments": toolCall.Arguments,
	}).Debug("Generated MCP tool call")
	
	// Execute the MCP tool call
	result, err := bot.mcpClient.CallTool(toolCall.ToolName, toolCall.Arguments)
	if err != nil {
		logrus.WithError(err).Error("Failed to execute MCP tool call")
		bot.client.PostMessage(
			channel,
			slack.MsgOptionText(fmt.Sprintf(":x: Query execution failed: %v", err), false),
			slack.MsgOptionTS(threadTS),
		)
		return
	}
	
	// Format the response using LLM
	formattedResponse, err := bot.llmProvider.FormatResponse(query, result)
	if err != nil {
		logrus.WithError(err).Error("Failed to format response")
		// Fall back to raw JSON if formatting fails
		formattedResponse = fmt.Sprintf("```json\n%s\n```", string(result))
	}
	
	// Post the formatted response
	bot.client.PostMessage(
		channel,
		slack.MsgOptionBlocks(
			slack.SectionBlock{
				Type: slack.MBTSection,
				Text: &slack.TextBlockObject{
					Type: slack.MarkdownType,
					Text: formattedResponse,
				},
			},
			slack.ContextBlock{
				Type: slack.MBTContext,
				ContextElements: slack.ContextElements{
					Elements: []slack.MixedElement{
						&slack.TextBlockObject{
							Type: slack.MarkdownType,
							Text: fmt.Sprintf("Tool: `%s` | Provider: `%s`", 
								toolCall.ToolName, 
								viper.GetString("llm.provider")),
						},
					},
				},
			},
		),
		slack.MsgOptionTS(threadTS),
	)
}

// Close shuts down the bot gracefully
func (bot *SlackBot) Close() error {
	if bot.mcpClient != nil {
		if err := bot.mcpClient.Close(); err != nil {
			logrus.WithError(err).Error("Failed to close MCP client")
		}
	}
	return nil
}