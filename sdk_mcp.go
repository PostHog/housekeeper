package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/common/model"
)

// RunMCPServer starts an MCP stdio server using the official go-sdk.
func RunMCPServer() error {
    impl := &mcp.Implementation{Name: "housekeeper-clickhouse-mcp", Title: "Housekeeper ClickHouse", Version: "0.3.0"}
	srv := mcp.NewServer(impl, &mcp.ServerOptions{})

	// Initialize Prometheus client
	if err := initPrometheus(); err != nil {
		return fmt.Errorf("failed to initialize prometheus client: %v", err)
	}

	// Register ClickHouse tool with inferred input schema (from queryArgs)
	mcp.AddTool[queryArgs, map[string]any](
		srv,
		&mcp.Tool{
			Name:        "clickhouse_query",
			Title:       "Query ClickHouse system tables",
			Description: "Read-only queries against ClickHouse system.* via clusterAllReplicas",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, ss *mcp.ServerSession, req *mcp.CallToolParamsFor[queryArgs]) (*mcp.CallToolResultFor[map[string]any], error) {
			qa := req.Arguments
            if qa.OrderBy == "" { /* tolerate orderBy alias via schema inference not possible here */ }
			if err := validateQueryArgs(qa); err != nil {
				return nil, err
			}
			rows, err := runClickhouseQuery(qa)
			if err != nil {
				return nil, err
			}
			data := map[string]any{"results": rows, "count": len(rows)}
			// Produce a concise, useful text summary for the LLM/UI
			summary := summarizeRows(rows)
			return &mcp.CallToolResultFor[map[string]any]{
				Content:           []mcp.Content{&mcp.TextContent{Text: summary}},
				StructuredContent: data,
			}, nil
		},
	)

	// Register Prometheus tool
	mcp.AddTool[prometheusArgs, map[string]any](
		srv,
		&mcp.Tool{
			Name:        "prometheus_query",
			Title:       "Query Prometheus metrics",
			Description: "Execute PromQL queries against Prometheus metrics",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, ss *mcp.ServerSession, req *mcp.CallToolParamsFor[prometheusArgs]) (*mcp.CallToolResultFor[map[string]any], error) {
			pa := req.Arguments

			if pa.Query == "" {
				return nil, fmt.Errorf("query is required")
			}

			var result interface{}
			var err error

			start, end, err := validateAndParseTimeRange(pa.Start, pa.End)
			if err != nil {
				return nil, err
			}

			step, err := time.ParseDuration(pa.Step)
			if err != nil {
				return nil, fmt.Errorf("invalid step duration: %v", err)
			}

			result, err = queryPrometheus(pa.Query, start, end, step)
			if err != nil {
				return nil, err
			}

			data := map[string]any{"result": result}

			// Create a simple summary showing the raw values
			var summary string
			if resultMap, ok := result.(map[string]interface{}); ok {
				if lastValues, ok := resultMap["last_values"].([]map[string]interface{}); ok && len(lastValues) > 0 {
					var parts []string
					for _, val := range lastValues {
						metric := val["metric"].(model.Metric)
						value := val["value"].(model.SampleValue)
						parts = append(parts, fmt.Sprintf("%v: %v", metric, value))
					}
					summary = strings.Join(parts, "\n")
				} else if raw, ok := resultMap["raw_result"]; ok {
					summary = fmt.Sprintf("%v", raw)
				} else {
					summary = "Query returned data in non-matrix format"
				}
			} else {
				summary = fmt.Sprintf("%v", result)
			}

			return &mcp.CallToolResultFor[map[string]any]{
				Content:           []mcp.Content{&mcp.TextContent{Text: summary}},
				StructuredContent: data,
			}, nil
		},
	)

	return srv.Run(context.Background(), mcp.NewStdioTransport())
}

// summarizeRows renders a compact, human-friendly summary of results.
// - If 0 rows: "no rows"
// - If 1 row: print key=value pairs (enhance common units)
// - If few rows (<=5): print each row on a line with k=v pairs
// - Else: print count and first row preview
func summarizeRows(rows []map[string]interface{}) string {
	if len(rows) == 0 {
		return "no rows"
	}
	if len(rows) == 1 {
		return formatRow(rows[0])
	}
	if len(rows) <= 5 {
		var b strings.Builder
		for i := range rows {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(formatRow(rows[i]))
		}
		return b.String()
	}
	// many rows: show count and a preview of the first row
	return fmt.Sprintf("rows: %d\nfirst: %s", len(rows), formatRow(rows[0]))
}

func formatRow(row map[string]interface{}) string {
	// stable key order
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := row[k]
		parts = append(parts, fmt.Sprintf("%s=%s", k, prettyValue(k, v)))
	}
	return strings.Join(parts, " ")
}

func prettyValue(key string, v interface{}) string {
	// Special-case time units
	lk := strings.ToLower(key)
	switch x := v.(type) {
	case int64:
		return prettyNumericWithUnits(lk, float64(x))
	case uint64:
		// lossless for values < 2^53 (~9e15), acceptable for rendering
		f := float64(x)
		return prettyNumericWithUnits(lk, f)
	case int, int32, uint32, uint16, uint8, int16, int8:
		return fmt.Sprintf("%v", v)
	case float64, float32:
		return fmt.Sprintf("%v", v)
	case string:
		return x
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func prettyNumericWithUnits(lkey string, val float64) string {
	if strings.Contains(lkey, "second") || strings.HasSuffix(lkey, "_seconds") || strings.HasSuffix(lkey, "seconds") {
		return fmt.Sprintf("%ss", trimFloat(val))
	}
	if strings.Contains(lkey, "microsecond") {
		// render both microseconds and seconds
		secs := val / 1_000_000.0
		return fmt.Sprintf("%.0fµs (%.3fs)", val, secs)
	}
	if strings.Contains(lkey, "millisecond") {
		secs := val / 1_000.0
		return fmt.Sprintf("%.0fms (%.3fs)", val, secs)
	}
	if strings.Contains(lkey, "nanosecond") {
		secs := val / 1_000_000_000.0
		return fmt.Sprintf("%.0fns (%.3fs)", val, secs)
	}
	if strings.Contains(lkey, "bytes") || strings.HasSuffix(lkey, "_bytes") {
		return humanBytes(val)
	}
	return trimFloat(val)
}

func trimFloat(val float64) string {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return fmt.Sprintf("%v", val)
	}
	if val == math.Trunc(val) {
		return fmt.Sprintf("%.0f", val)
	}
	return fmt.Sprintf("%.6g", val)
}

func humanBytes(val float64) string {
	if val < 1024 {
		return fmt.Sprintf("%.0f B", val)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := val
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}
