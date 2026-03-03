package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/common/model"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// RunMCPServer starts an MCP stdio server using the official go-sdk.
func RunMCPServer() error {
	impl := &mcp.Implementation{Name: "housekeeper-clickhouse-mcp", Title: "Housekeeper ClickHouse", Version: "0.3.0"}
	srv := mcp.NewServer(impl, &mcp.ServerOptions{})

	// Initialize Prometheus client
	if err := initPrometheus(); err != nil {
		return fmt.Errorf("failed to initialize prometheus client: %v", err)
	}

	// Build description with allowed databases
	allowedDbs := getAllowedDatabases()
	dbList := strings.Join(allowedDbs, ", ")
	toolDesc := fmt.Sprintf("Read-only queries against ClickHouse databases (%s). IMPORTANT: Only use clusterAllReplicas for system.* tables to get cluster-wide data. For non-system databases, query directly without clusterAllReplicas", dbList)
	
	// Register ClickHouse tool with inferred input schema (from queryArgs)
	mcp.AddTool[queryArgs, map[string]any](
		srv,
		&mcp.Tool{
			Name:        "clickhouse_query",
			Title:       "Query ClickHouse tables",
			Description: toolDesc,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, ss *mcp.ServerSession, req *mcp.CallToolParamsFor[queryArgs]) (*mcp.CallToolResultFor[map[string]any], error) {
			qa := req.Arguments
			// Note: OrderBy might be empty, which is valid
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

	if viper.GetBool("http.enabled") {
		return runHTTPMCPServer(srv)
	}
	return srv.Run(context.Background(), mcp.NewStdioTransport())
}

// runHTTPMCPServer starts the MCP server over HTTP using SSE transport.
func runHTTPMCPServer(srv *mcp.Server) error {
	addr := viper.GetString("http.addr")
	authToken := viper.GetString("http.auth_token")

	mux := http.NewServeMux()

	// Health check — no auth required; used by k8s probes and connectivity tests.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	// MCP streamable HTTP endpoint (2025-03-26 spec — what Claude Code uses).
	// POST / with Accept: application/json, text/event-stream
	streamHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		logrus.WithFields(logrus.Fields{
			"remote_addr": r.RemoteAddr,
			"user_agent":  r.Header.Get("User-Agent"),
		}).Info("New MCP streamable session opened")
		return srv
	}, nil)

	var mcpHandler http.Handler = streamHandler
	if authToken != "" {
		mcpHandler = bearerAuthMiddleware(authToken, streamHandler)
		logrus.Info("HTTP authentication enabled")
	} else {
		logrus.Warn("HTTP authentication is disabled — consider setting http.auth_token")
	}
	mux.Handle("/", mcpHandler)

	// Wrap everything with CORS + request logging.
	handler := requestLoggingMiddleware(corsMiddleware(mux))

	// Bump to debug so request logs are visible during troubleshooting.
	// Users can lower this via config once things are working.
	if logrus.GetLevel() < logrus.DebugLevel {
		logrus.SetLevel(logrus.DebugLevel)
		logrus.Debug("Log level raised to debug for HTTP mode (set logging.level in config to suppress)")
	}

	logrus.WithFields(logrus.Fields{
		"addr":       addr,
		"mcp_url":    "http://<host>" + addr + "/",
		"health_url": "http://<host>" + addr + "/health",
		"auth":       authToken != "",
	}).Info("HTTP/SSE MCP server ready — connect your MCP client to the mcp_url above")

	return http.ListenAndServe(addr, handler)
}

// requestLoggingMiddleware logs every incoming HTTP request.
func requestLoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logrus.WithFields(logrus.Fields{
			"method":      r.Method,
			"path":        r.URL.RequestURI(),
			"remote_addr": r.RemoteAddr,
			"user_agent":  r.Header.Get("User-Agent"),
			"accept":      r.Header.Get("Accept"),
			"origin":      r.Header.Get("Origin"),
			"has_auth":    r.Header.Get("Authorization") != "",
		}).Debug("Incoming request")
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware adds CORS headers so Claude.ai (and other web-based MCP clients) can connect.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerAuthMiddleware rejects requests that do not carry the expected Bearer token.
func bearerAuthMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != token {
			logrus.WithFields(logrus.Fields{
				"remote_addr": r.RemoteAddr,
				"path":        r.URL.Path,
				"method":      r.Method,
				"has_auth":    auth != "",
			}).Warn("Rejected unauthorized request")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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
