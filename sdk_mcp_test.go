package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
)

func TestHTTPHandlerOriginCheck(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ServerOptions{})
	handler := newHTTPHandler(srv, "", 0)
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`

	tests := []struct {
		name       string
		method     string
		headers    map[string]string
		wantStatus int
	}{
		{
			name:       "client without browser headers",
			method:     http.MethodPost,
			headers:    map[string]string{"Content-Type": "application/json"},
			wantStatus: http.StatusOK,
		},
		{
			name:   "same-origin browser",
			method: http.MethodPost,
			headers: map[string]string{
				"Content-Type":   "application/json",
				"Origin":         "https://housekeeper.example.com",
				"Sec-Fetch-Site": "same-origin",
			},
			wantStatus: http.StatusOK,
		},
		{
			name:   "cross-site browser",
			method: http.MethodPost,
			headers: map[string]string{
				"Content-Type":   "text/plain",
				"Origin":         "https://other.example",
				"Sec-Fetch-Site": "cross-site",
			},
			wantStatus: http.StatusForbidden,
		},
		{
			// Older browsers send Origin but no Sec-Fetch-Site.
			name:   "foreign origin without fetch metadata",
			method: http.MethodPost,
			headers: map[string]string{
				"Content-Type": "text/plain",
				"Origin":       "https://other.example",
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name:   "cross-site preflight",
			method: http.MethodOptions,
			headers: map[string]string{
				"Origin":                        "https://other.example",
				"Access-Control-Request-Method": "POST",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "https://housekeeper.example.com/", strings.NewReader(initialize))
			req.Header.Set("Accept", "application/json, text/event-stream")
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if tt.wantStatus != 0 && rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("Access-Control-Allow-Origin = %q, want none", got)
			}
		})
	}
}

func TestQueryInputSchema(t *testing.T) {
	schema, err := queryInputSchema()
	if err != nil {
		t.Fatal(err)
	}
	if got := schema.Properties["columns"].Type; got != "array" {
		t.Errorf(`columns type = %q, want "array"`, got)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"sql": "SELECT 1"},
		{"table": "system.one", "columns": []any{"dummy"}, "limit": 1},
	} {
		if err := resolved.Validate(args); err != nil {
			t.Errorf("Validate(%v) = %v, want nil", args, err)
		}
	}
}

func TestHTTPHandlerBearerAuth(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ServerOptions{})
	handler := newHTTPHandler(srv, "s3cret", 0)
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`

	tests := []struct {
		name       string
		path       string
		auth       string
		wantStatus int
	}{
		{"missing token", "/", "", http.StatusUnauthorized},
		{"wrong token", "/", "Bearer nope", http.StatusUnauthorized},
		{"token prefix only", "/", "Bearer s3cre", http.StatusUnauthorized},
		{"wrong scheme", "/", "Basic s3cret", http.StatusUnauthorized},
		{"right token", "/", "Bearer s3cret", http.StatusOK},
		{"health needs no token", "/health", "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, body := http.MethodPost, strings.NewReader(initialize)
			if tt.path == "/health" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, "http://housekeeper.example.com"+tt.path, body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestWithToolTimeout(t *testing.T) {
	viper.Set("test.timeout_seconds", 7)
	t.Cleanup(func() { viper.Set("test.timeout_seconds", nil) })
	ctx, cancel := withToolTimeout(context.Background(), "test.timeout_seconds")
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 7*time.Second || time.Until(deadline) < 6*time.Second {
		t.Errorf("deadline = %v (set %v), want ~7s from now", time.Until(deadline), ok)
	}

	viper.Set("test.timeout_seconds", 0)
	ctx, cancel = withToolTimeout(context.Background(), "test.timeout_seconds")
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Error("a zero timeout set a deadline")
	}
}

func TestServeUntilDoneWaitsForJobs(t *testing.T) {
	release := make(chan struct{})
	runner := newDiagnoseRunner(1, blockingRun(release))
	job, err := runner.start("q", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := &http.Server{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serveUntilDone(ctx, httpSrv, runner, 5*time.Second) }()
	cancel()
	time.AfterFunc(50*time.Millisecond, func() { close(release) })

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serveUntilDone() = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveUntilDone did not return")
	}
	if got, _ := runner.get(job.ID); got.Status != "done" {
		t.Errorf("job status = %q, want done (shutdown should wait for it)", got.Status)
	}
}
