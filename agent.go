package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/spf13/viper"
	"google.golang.org/genai"
)

type QuerySystemTableArgs struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns,omitempty"`
	Where   string   `json:"where,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

var querySystemTableTool = &genai.Tool{
	FunctionDeclarations: []*genai.FunctionDeclaration{
		{
			Name:        "query_clickhouse_system_table",
			Description: "Query any ClickHouse system table to get diagnostic information",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"table": {
						Type:        genai.TypeString,
						Description: "Name of the system table to query (e.g., 'system.parts', 'system.metrics', 'system.processes')",
					},
					"columns": {
						Type: genai.TypeArray,
						Items: &genai.Schema{
							Type: genai.TypeString,
						},
						Description: "Specific columns to select. If empty, selects all columns",
					},
					"where": {
						Type:        genai.TypeString,
						Description: "WHERE clause conditions (without the WHERE keyword)",
					},
					"limit": {
						Type:        genai.TypeNumber,
						Description: "Number of rows to limit the result to",
					},
				},
				Required: []string{"table"},
			},
		},
	},
}

func QuerySystemTable(ctx context.Context, conn driver.Conn, args QuerySystemTableArgs) ([]map[string]interface{}, error) {
	cluster := viper.GetString("clickhouse.cluster")

	var query strings.Builder
	query.WriteString("SELECT ")

	if len(args.Columns) > 0 {
		query.WriteString(strings.Join(args.Columns, ", "))
	} else {
		query.WriteString("*")
	}

	query.WriteString(fmt.Sprintf(" FROM clusterAllReplicas(%s, %s)", cluster, args.Table))

	if args.Where != "" {
		query.WriteString(" WHERE " + args.Where)
	}

	if args.Limit > 0 {
		query.WriteString(fmt.Sprintf(" LIMIT %d", args.Limit))
	}

	rows, err := conn.Query(ctx, query.String())
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}
	defer rows.Close()

	columns := rows.Columns()
	columnTypes := rows.ColumnTypes()

	var results []map[string]interface{}

	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))

		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			row[col] = values[i]
		}
		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	log.Printf("Query executed: %s, returned %d rows, columns: %v, types: %v",
		query.String(), len(results), columns, columnTypes)

	return results, nil
}

func AnalyzeErrorsWithAgent(chErrors CHErrors) string {
	ctx := context.Background()

	apiKey := viper.GetString("gemini_key")

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		log.Fatal("Error creating client:", err)
	}

	conn, err := connect()
	if err != nil {
		log.Fatal("Error connecting to ClickHouse:", err)
	}
	defer conn.Close()

	systemPrompt := `You are a ClickHouse database administrator analyzing system errors.
You have access to query any ClickHouse system table to gather more context about errors.
Available system tables include but are not limited to:
- system.metrics: Current metrics values
- system.processes: Currently executing queries
- system.parts: Information about parts of MergeTree tables
- system.replicas: Information about replicas
- system.replication_queue: Tasks in replication queue
- system.mutations: Information about mutations
- system.merges: Information about merges in progress
- system.query_log: Query execution history
- system.settings: Current settings values
- system.clusters: Cluster configuration

When analyzing errors, use the query_clickhouse_system_table function to gather relevant context.
Focus on identifying root causes and patterns.

IMPORTANT: Keep your response CONCISE and under 2500 characters total.
Format your final analysis for a Slack channel message using markdown.
Prioritize the most critical issues and actionable recommendations.`

	config := &genai.GenerateContentConfig{
		Temperature:     genai.Ptr(float32(0.7)),
		MaxOutputTokens: 2000,
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: systemPrompt}},
		},
		Tools: []*genai.Tool{querySystemTableTool},
	}

	prompt := fmt.Sprintf(`Analyze the following ClickHouse errors from the past hour.
Use the query_clickhouse_system_table function to gather additional context about these errors.
For example, you might want to check:
- Current system metrics if there are resource-related errors
- Running processes if there are query timeout errors
- Replication status if there are replication errors
- Merge/mutation status if there are table operation errors

Errors from system.errors table:
%s

Provide a CONCISE analysis (under 2500 characters) with:
1. Top 3 most critical issues
2. Root cause for each critical issue
3. Immediate action items
4. Use Slack markdown formatting with urgency indicators (🔴 critical, 🟡 warning, 🟢 info)

Be brief and focus only on actionable insights.`, chErrors.String())

	chat, err := client.Chats.Create(ctx, "gemini-1.5-flash", config, nil)
	if err != nil {
		log.Fatal("Error creating chat:", err)
	}

	resp, err := chat.SendMessage(ctx, genai.Part{Text: prompt})
	if err != nil {
		log.Fatal("Error sending message:", err)
	}

	maxIterations := 5
	for range maxIterations {
		functionCalls := resp.FunctionCalls()
		if len(functionCalls) == 0 {
			break
		}

		var funcResponses []genai.Part
		for _, call := range functionCalls {
			if call.Name == "query_clickhouse_system_table" {
				var args QuerySystemTableArgs
				if argsJSON, err := json.Marshal(call.Args); err == nil {
					if err := json.Unmarshal(argsJSON, &args); err == nil {
						results, err := QuerySystemTable(ctx, conn, args)
						if err != nil {
							funcResponses = append(funcResponses, genai.Part{
								FunctionResponse: &genai.FunctionResponse{
									Name: call.Name,
									Response: map[string]interface{}{
										"error": err.Error(),
									},
								},
							})
						} else {
							funcResponses = append(funcResponses, genai.Part{
								FunctionResponse: &genai.FunctionResponse{
									Name: call.Name,
									Response: map[string]interface{}{
										"results": results,
										"count":   len(results),
									},
								},
							})
						}
					}
				}
			}
		}

		if len(funcResponses) > 0 {
			resp, err = chat.SendMessage(ctx, funcResponses...)
			if err != nil {
				log.Fatal("Error processing function responses:", err)
			}
		}
	}

	return resp.Text()
}
