package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"github.com/spf13/viper"
	"google.golang.org/api/option"
)

// LLMProvider represents an interface for different LLM providers
type LLMProvider interface {
	// GenerateMCPQuery converts natural language to MCP tool calls
	GenerateMCPQuery(userQuery string, availableTools []MCPTool) (*MCPToolCall, error)
	
	// FormatResponse formats MCP results for Slack
	FormatResponse(query string, result json.RawMessage) (string, error)
}

// MCPToolCall represents a tool call to be executed
type MCPToolCall struct {
	ToolName  string                 `json:"tool_name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// LLMFactory creates the appropriate LLM provider based on configuration
func NewLLMProvider() (LLMProvider, error) {
	provider := viper.GetString("llm.provider")
	
	switch strings.ToLower(provider) {
	case "gemini":
		return NewGeminiProvider()
	case "claude":
		return NewClaudeProvider()
	case "openai", "gpt4", "gpt-4":
		return NewOpenAIProvider()
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", provider)
	}
}

// GeminiProvider implements LLMProvider using Google Gemini
type GeminiProvider struct {
	client *genai.Client
	model  *genai.GenerativeModel
}

func NewGeminiProvider() (*GeminiProvider, error) {
	apiKey := viper.GetString("llm.gemini.api_key")
	if apiKey == "" {
		return nil, fmt.Errorf("gemini API key not configured")
	}
	
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}
	
	model := client.GenerativeModel(viper.GetString("llm.gemini.model"))
	if model == nil {
		model = client.GenerativeModel("gemini-1.5-flash")
	}
	
	// Configure model for structured output
	model.SetTemperature(0.1)
	model.ResponseMIMEType = "application/json"
	
	return &GeminiProvider{
		client: client,
		model:  model,
	}, nil
}

func (g *GeminiProvider) GenerateMCPQuery(userQuery string, availableTools []MCPTool) (*MCPToolCall, error) {
	toolsJSON, _ := json.MarshalIndent(availableTools, "", "  ")
	
	prompt := fmt.Sprintf(`You are a helpful assistant that converts natural language queries into MCP tool calls.

Available tools:
%s

User query: "%s"

Convert this query into an appropriate MCP tool call. Consider:
1. For ClickHouse queries, use clickhouse_query with either structured parameters or raw SQL
2. For metrics queries, use prometheus_query with PromQL
3. Choose the most appropriate tool based on the query intent

Respond with a JSON object containing:
{
  "tool_name": "clickhouse_query or prometheus_query",
  "arguments": {
    // appropriate arguments for the chosen tool
  }
}

If the query is about database performance, errors, or system tables, use clickhouse_query.
If the query is about metrics, rates, or monitoring data, use prometheus_query.`, string(toolsJSON), userQuery)
	
	ctx := context.Background()
	resp, err := g.model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return nil, fmt.Errorf("failed to generate content: %w", err)
	}
	
	if len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("no response from Gemini")
	}
	
	// Extract JSON from response
	content := fmt.Sprint(resp.Candidates[0].Content.Parts[0])
	
	var toolCall MCPToolCall
	if err := json.Unmarshal([]byte(content), &toolCall); err != nil {
		return nil, fmt.Errorf("failed to parse tool call: %w", err)
	}
	
	return &toolCall, nil
}

func (g *GeminiProvider) FormatResponse(query string, result json.RawMessage) (string, error) {
	prompt := fmt.Sprintf(`Format this database/metrics query result for Slack. Make it concise and readable.

Original query: "%s"

Result data:
%s

Provide a brief, formatted summary suitable for Slack. Use markdown formatting where appropriate.
Focus on the most important information and insights.`, query, string(result))
	
	ctx := context.Background()
	g.model.ResponseMIMEType = "" // Reset to text for formatting
	resp, err := g.model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return "", fmt.Errorf("failed to format response: %w", err)
	}
	
	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("no response from Gemini")
	}
	
	return fmt.Sprint(resp.Candidates[0].Content.Parts[0]), nil
}

// ClaudeProvider implements LLMProvider using Anthropic Claude
type ClaudeProvider struct {
	apiKey string
	model  string
}

func NewClaudeProvider() (*ClaudeProvider, error) {
	apiKey := viper.GetString("llm.claude.api_key")
	if apiKey == "" {
		return nil, fmt.Errorf("claude API key not configured")
	}
	
	model := viper.GetString("llm.claude.model")
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}
	
	return &ClaudeProvider{
		apiKey: apiKey,
		model:  model,
	}, nil
}

func (c *ClaudeProvider) GenerateMCPQuery(userQuery string, availableTools []MCPTool) (*MCPToolCall, error) {
	toolsJSON, _ := json.MarshalIndent(availableTools, "", "  ")
	
	requestBody := map[string]interface{}{
		"model": c.model,
		"max_tokens": 1024,
		"temperature": 0.1,
		"messages": []map[string]string{
			{
				"role": "user",
				"content": fmt.Sprintf(`Convert this natural language query into an MCP tool call.

Available tools:
%s

Query: "%s"

Respond with only a JSON object:
{
  "tool_name": "clickhouse_query or prometheus_query",
  "arguments": { ... }
}`, string(toolsJSON), userQuery),
			},
		},
	}
	
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Claude API error (%d): %s", resp.StatusCode, string(body))
	}
	
	var claudeResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	
	if len(claudeResp.Content) == 0 {
		return nil, fmt.Errorf("no content in Claude response")
	}
	
	var toolCall MCPToolCall
	if err := json.Unmarshal([]byte(claudeResp.Content[0].Text), &toolCall); err != nil {
		return nil, fmt.Errorf("failed to parse tool call: %w", err)
	}
	
	return &toolCall, nil
}

func (c *ClaudeProvider) FormatResponse(query string, result json.RawMessage) (string, error) {
	requestBody := map[string]interface{}{
		"model": c.model,
		"max_tokens": 1024,
		"temperature": 0.3,
		"messages": []map[string]string{
			{
				"role": "user",
				"content": fmt.Sprintf(`Format this query result for Slack (markdown supported).
Query: "%s"
Result: %s

Provide a concise, readable summary.`, query, string(result)),
			},
		},
	}
	
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}
	
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}
	
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Claude API error (%d): %s", resp.StatusCode, string(body))
	}
	
	var claudeResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	
	if len(claudeResp.Content) == 0 {
		return "", fmt.Errorf("no content in Claude response")
	}
	
	return claudeResp.Content[0].Text, nil
}

// OpenAIProvider implements LLMProvider using OpenAI GPT-4
type OpenAIProvider struct {
	apiKey string
	model  string
}

func NewOpenAIProvider() (*OpenAIProvider, error) {
	apiKey := viper.GetString("llm.openai.api_key")
	if apiKey == "" {
		return nil, fmt.Errorf("openai API key not configured")
	}
	
	model := viper.GetString("llm.openai.model")
	if model == "" {
		model = "gpt-4-turbo-preview"
	}
	
	return &OpenAIProvider{
		apiKey: apiKey,
		model:  model,
	}, nil
}

func (o *OpenAIProvider) GenerateMCPQuery(userQuery string, availableTools []MCPTool) (*MCPToolCall, error) {
	toolsJSON, _ := json.MarshalIndent(availableTools, "", "  ")
	
	requestBody := map[string]interface{}{
		"model": o.model,
		"temperature": 0.1,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []map[string]string{
			{
				"role": "system",
				"content": "You convert natural language queries into MCP tool calls. Always respond with valid JSON.",
			},
			{
				"role": "user",
				"content": fmt.Sprintf(`Available tools:
%s

Query: "%s"

Return JSON: {"tool_name": "...", "arguments": {...}}`, string(toolsJSON), userQuery),
			},
		},
	}
	
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	
	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI API error (%d): %s", resp.StatusCode, string(body))
	}
	
	var openAIResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	
	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in OpenAI response")
	}
	
	var toolCall MCPToolCall
	if err := json.Unmarshal([]byte(openAIResp.Choices[0].Message.Content), &toolCall); err != nil {
		return nil, fmt.Errorf("failed to parse tool call: %w", err)
	}
	
	return &toolCall, nil
}

func (o *OpenAIProvider) FormatResponse(query string, result json.RawMessage) (string, error) {
	requestBody := map[string]interface{}{
		"model": o.model,
		"temperature": 0.3,
		"messages": []map[string]string{
			{
				"role": "system",
				"content": "Format database/metrics results for Slack using markdown. Be concise and highlight key insights.",
			},
			{
				"role": "user",
				"content": fmt.Sprintf("Query: %s\nResult: %s", query, string(result)),
			},
		},
	}
	
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}
	
	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}
	
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenAI API error (%d): %s", resp.StatusCode, string(body))
	}
	
	var openAIResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	
	if len(openAIResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in OpenAI response")
	}
	
	return openAIResp.Choices[0].Message.Content, nil
}