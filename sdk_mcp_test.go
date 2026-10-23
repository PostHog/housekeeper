package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPHandlerOriginCheck(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ServerOptions{})
	handler := newHTTPHandler(srv, "")
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
