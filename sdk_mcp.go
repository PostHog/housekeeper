package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"crypto/x509/pkix"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/common/model"
	logrus "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// RunMCPServer starts an MCP stdio server using the official go-sdk.
func RunMCPServer() error {
	srv := buildMCPServer()
	logrus.Info("MCP stdio server ready")
	return srv.Run(context.Background(), mcp.NewStdioTransport())
}

type SayHiParams struct {
	Name string `json:"name"`
}

func SayHi(ctx context.Context, cc *mcp.ServerSession, params *mcp.CallToolParamsFor[SayHiParams]) (*mcp.CallToolResultFor[any], error) {
	return &mcp.CallToolResultFor[any]{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "Hi " + params.Arguments.Name},
		},
	}, nil
}

func RunMCPSSEServer(port int) error {
	srv := buildMCPServer()

	server1 := mcp.NewServer(&mcp.Implementation{Name: "greeter1"}, nil)
	mcp.AddTool(server1, &mcp.Tool{Name: "greet1", Description: "say hi"}, SayHi)

	// Wrap the SSE handler with CORS support
	sseHandler := mcp.NewSSEHandler(func(r *http.Request) *mcp.Server {
		switch r.URL.Path {
		case "/clickhouse":
			return srv
		case "/greeter":
			return server1
		default:
			// should not be reached because mux routes only /clickhouse/sse here
			return srv
		}
	})
	
	// CORS-enabled handler wrapper
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Cache-Control")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		
		sseHandler.ServeHTTP(w, r)
	})

	mux := http.NewServeMux()
	// Initialize OAuth (discovery + JWKS) if enabled
	initOAuth()
	if viper.GetBool("oauth.enabled") {
		mux.HandleFunc("/.well-known/openid-configuration", handleWellKnownOIDC)
		mux.HandleFunc("/.well-known/oauth-authorization-server", handleWellKnownOAuth)
		mux.HandleFunc("/.well-known/oauth-protected-resource", handleOAuthProtectedResource)
		mux.HandleFunc("/oauth/jwks", handleJWKS)
		mux.HandleFunc("/oauth/register", handleClientRegistration)
		mux.HandleFunc("/oauth/authorize", handleAuthorize)
		mux.HandleFunc("/oauth/token", handleToken)
	}
	// Simple health endpoint
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", handler)
	httpAddr := fmt.Sprintf(":%d", port)
	logrus.WithField("addr", httpAddr).Info("MCP SSE HTTP server listening")

	errCh := make(chan error, 2)

	go func() {
		if err := http.ListenAndServe(httpAddr, mux); err != nil {
			errCh <- err
		}
	}()

	if viper.GetBool("sse.tls.enabled") {
		tlsPort := viper.GetInt("sse.tls.port")
		if tlsPort == 0 {
			tlsPort = 3443
		}
		tlsAddr := fmt.Sprintf(":%d", tlsPort)

		certFile := strings.TrimSpace(viper.GetString("sse.tls.cert_file"))
		keyFile := strings.TrimSpace(viper.GetString("sse.tls.key_file"))
		selfSigned := viper.GetBool("sse.tls.self_signed")

		server := &http.Server{Addr: tlsAddr, Handler: mux}

		if certFile != "" && keyFile != "" {
			logrus.WithFields(logrus.Fields{"addr": tlsAddr, "cert": certFile}).Info("MCP SSE HTTPS server (file cert)")
			go func() { errCh <- server.ListenAndServeTLS(certFile, keyFile) }()
		} else if selfSigned {
			cert, err := generateSelfSignedCert([]string{"localhost"})
			if err != nil {
				logrus.WithError(err).Error("failed to generate self-signed cert")
			} else {
				server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
				logrus.WithField("addr", tlsAddr).Info("MCP SSE HTTPS server (self-signed)")
				ln, err := net.Listen("tcp", tlsAddr)
				if err != nil {
					errCh <- err
				} else {
					go func() { errCh <- server.ServeTLS(ln, "", "") }()
				}
			}
		}
	}

	return <-errCh
}

func generateSelfSignedCert(hosts []string) (tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "housekeeper-mcp-sse"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert := tls.Certificate{Certificate: [][]byte{derBytes}, PrivateKey: priv}
	return cert, nil
}

func buildMCPServer() *mcp.Server {
	impl := &mcp.Implementation{Name: "housekeeper-clickhouse-mcp", Title: "Housekeeper ClickHouse", Version: "0.3.0"}
	srv := mcp.NewServer(impl, &mcp.ServerOptions{})
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
			if qa.OrderBy == "" { /* alias tolerated */
			}
			logrus.WithFields(logrus.Fields{"mode": func() string {
				if strings.TrimSpace(qa.SQL) != "" {
					return "sql"
				}
				return "structured"
			}(), "table": qa.Table}).Info("clickhouse_query invoked")
			if err := validateQueryArgs(qa); err != nil {
				return nil, err
			}
			rows, err := runClickhouseQuery(qa)
			if err != nil {
				return nil, err
			}
			logrus.WithField("rows", len(rows)).Info("clickhouse_query completed")
			data := map[string]any{"results": rows, "count": len(rows)}
			summary := summarizeRows(rows)
			return &mcp.CallToolResultFor[map[string]any]{
				Content:           []mcp.Content{&mcp.TextContent{Text: summary}},
				StructuredContent: data,
			}, nil
		},
	)

	// Initialize Prometheus client
	if err := initPrometheus(); err != nil {
		logrus.WithFields(logrus.Fields{"error": err}).Error("failed to initialize prometheus client")
	} else {
		// Register Prometheus tool if Prometheus client is initialized successfully
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
	}

	logrus.WithField("tools", []string{"clickhouse_query"}).Info("MCP server initialized")
	return srv
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
