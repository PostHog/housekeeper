package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// diagnoseArgs is the input to the diagnose tool.
type diagnoseArgs struct {
	Question string `json:"question"`
	Cluster  string `json:"cluster,omitempty"`
}

// maxToolResultChars caps how much row data we feed back to the model per
// run_sql call, so a careless SELECT can't blow the context window or ship a
// huge slice of customer data into the prompt.
const maxToolResultChars = 12000

// connectAnalyst opens the elevated ClickHouse connection used ONLY by the
// server-side diagnose agent. It uses analyst_clickhouse.* config (the
// housekeeper_analyst role — full grants, raw query text), falling back to the
// restricted clickhouse.* connection if no analyst user is configured. Because
// this connection's output never leaves the account (only the model's summary
// does), it is safe for it to read raw query text.
func connectAnalyst() (driver.Conn, error) {
	host := viper.GetString("analyst_clickhouse.host")
	if host == "" {
		host = viper.GetString("clickhouse.host")
	}
	port := viper.GetInt("analyst_clickhouse.port")
	if port == 0 {
		port = viper.GetInt("clickhouse.port")
	}
	user := viper.GetString("analyst_clickhouse.user")
	password := viper.GetString("analyst_clickhouse.password")
	if user == "" {
		// No dedicated analyst credentials — fall back to the restricted role.
		user = viper.GetString("clickhouse.user")
		password = viper.GetString("clickhouse.password")
		logrus.Warn("diagnose: analyst_clickhouse.user not set; falling back to restricted clickhouse user (raw query text will be unavailable)")
	}
	database := viper.GetString("analyst_clickhouse.database")
	if database == "" {
		database = viper.GetString("clickhouse.database")
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{Database: database, Username: user, Password: password},
		TLS:  &tls.Config{InsecureSkipVerify: true},
		ClientInfo: clickhouse.ClientInfo{
			Products: []struct {
				Name    string
				Version string
			}{{Name: "housekeeper-diagnose", Version: "0.1"}},
		},
	})
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if err := conn.Ping(ctx); err != nil {
		return nil, err
	}
	logrus.WithFields(logrus.Fields{"host": host, "user": user}).Debug("diagnose: analyst connection established")
	return conn, nil
}

// queryRows runs a read-only query on the given connection and returns rows as
// JSON-friendly maps (same normalization as the public clickhouse_query tool).
func queryRows(ctx context.Context, conn driver.Conn, query string) ([]map[string]interface{}, error) {
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			logrus.WithError(cerr).Warn("diagnose: error closing rows")
		}
	}()

	cols := rows.Columns()
	colTypes := rows.ColumnTypes()
	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		holders := make([]reflect.Value, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range cols {
			st := colTypes[i].ScanType()
			if st == nil {
				st = reflect.TypeOf("")
			}
			dest := reflect.New(st)
			holders[i] = dest
			ptrs[i] = dest.Interface()
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			if colTypes[i].Nullable() {
				vptr := holders[i].Elem()
				if vptr.IsNil() {
					row[c] = nil
					continue
				}
				row[c] = normalizeValue(vptr.Elem().Interface())
			} else {
				row[c] = normalizeValue(holders[i].Elem().Interface())
			}
		}
		results = append(results, row)
	}
	return results, rows.Err()
}

// formatRowsForModel renders rows as compact JSON, truncated to a char budget.
func formatRowsForModel(rows []map[string]interface{}) string {
	if len(rows) == 0 {
		return "0 rows"
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return summarizeRows(rows)
	}
	out := string(b)
	if len(out) > maxToolResultChars {
		out = out[:maxToolResultChars] + fmt.Sprintf("\n…(truncated; %d rows total — add aggregation or LIMIT)", len(rows))
	}
	return fmt.Sprintf("%d rows:\n%s", len(rows), out)
}

const diagnoseSystemPrompt = `You are a senior ClickHouse SRE. Answer the operator's question by investigating with the run_sql tool, then give a concise, actionable diagnosis.

Querying:
- run_sql executes ONE read-only SELECT/WITH statement per call. No DDL/DML.
- system.* tables are per-node: wrap them in clusterAllReplicas('<cluster>', system.<table>) for cluster-wide visibility. Read system.clusters first if unsure which clusters exist.
- Use live system.* tables for current state (running queries, merges, parts, replication, errors); use query-log history for trends and attribution.
- Attribution: queries are often tagged via log_comment (frequently surfaced as lc_* columns) and collapsed by normalized_query_hash. count(), sum(query_duration_ms), sum(read_bytes) GROUP BY normalized_query_hash is the canonical "what's driving load" query.
- Calibrate memory_usage / read_bytes against the cluster's node size before flagging anything as concerning.

Output rules:
- Investigate efficiently: a handful of targeted queries, not dozens. Aggregate; never SELECT * without a tight LIMIT.
- You may read raw query text to understand a problem, but your ANSWER MUST NOT reproduce raw query text, literals, PII, or other user-supplied content. Refer to queries by normalized_query_hash and attribution columns instead.
- Lead with the diagnosis and the evidence (numbers), then concrete next steps.

Deployment-specific details (databases, tables, clusters, node sizes) are appended below when configured.`

// registerDiagnoseTool adds the in-MCP, Bedrock-backed diagnose tool. The model
// runs server-side, queries ClickHouse with the elevated analyst connection, and
// only its summary is returned to the client.
func registerDiagnoseTool(srv *mcp.Server) {
	system := diagnoseSystemPrompt
	if extra := strings.TrimSpace(viper.GetString("mcp.extra_tool_description")); extra != "" {
		system += "\n\nDeployment-specific context:\n" + extra
	}

	desc := `Ask a natural-language question about ClickHouse health and get an investigated, attributed diagnosis. Runs an in-account LLM agent that queries the cluster server-side with elevated grants (full query-log + attribution) and returns only a summary — raw query text never leaves the account. Use for "why is X slow / lagging / erroring", "what's driving load on <cluster>", "who owns this query pattern". For raw row access use clickhouse_query instead.`

	mcp.AddTool[diagnoseArgs, map[string]any](
		srv,
		&mcp.Tool{
			Name:        "clickhouse_diagnose",
			Title:       "Diagnose ClickHouse (in-account LLM)",
			Description: desc,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, ss *mcp.ServerSession, req *mcp.CallToolParamsFor[diagnoseArgs]) (*mcp.CallToolResultFor[map[string]any], error) {
			q := strings.TrimSpace(req.Arguments.Question)
			if q == "" {
				return nil, fmt.Errorf("question is required")
			}

			client, err := newBedrockClient(ctx)
			if err != nil {
				return nil, err
			}
			conn, err := connectAnalyst()
			if err != nil {
				return nil, fmt.Errorf("analyst connection: %w", err)
			}
			defer func() {
				if cerr := conn.Close(); cerr != nil {
					logrus.WithError(cerr).Warn("diagnose: error closing analyst connection")
				}
			}()

			// The single tool the model may call: a guarded read-only query.
			runSQL := bedrockTool{
				name:        "run_sql",
				description: "Execute one read-only SELECT/WITH query against ClickHouse and return the rows. Single statement only.",
				inputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"sql": map[string]any{
							"type":        "string",
							"description": "A single read-only SELECT or WITH query. Use clusterAllReplicas('<cluster>', system.<table>) for system tables.",
						},
					},
					"required": []any{"sql"},
				},
			}

			handle := func(name string, input map[string]any) (string, error) {
				if name != "run_sql" {
					return "", fmt.Errorf("unknown tool %q", name)
				}
				sql, _ := input["sql"].(string)
				sql = strings.TrimSpace(sql)
				if sql == "" {
					return "", fmt.Errorf("sql is empty")
				}
				if err := validateFreeformSQL(sql); err != nil {
					return "", err
				}
				rows, err := queryRows(ctx, conn, sql)
				if err != nil {
					return "", err
				}
				return formatRowsForModel(rows), nil
			}

			userMsg := q
			if c := strings.TrimSpace(req.Arguments.Cluster); c != "" {
				userMsg = fmt.Sprintf("%s\n\n(focus cluster: %s)", q, c)
			}

			answer, err := runBedrockAgent(
				ctx, client,
				viper.GetString("bedrock.model_id"),
				system, userMsg,
				[]bedrockTool{runSQL}, handle,
				int32(viper.GetInt("bedrock.max_tokens")),
				int32(viper.GetInt("bedrock.max_iterations")),
				float32(viper.GetFloat64("bedrock.temperature")),
			)
			if err != nil {
				return nil, err
			}

			return &mcp.CallToolResultFor[map[string]any]{
				Content:           []mcp.Content{&mcp.TextContent{Text: answer}},
				StructuredContent: map[string]any{"answer": answer},
			}, nil
		},
	)
}
