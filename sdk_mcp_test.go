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

// Each handler stands in for one replica. A request that skips initialize, sent to a
// replica that never saw the client, must still succeed, and no session ID may be issued.
func TestHTTPHandlerStateless(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ServerOptions{})
	replicaA := newHTTPHandler(srv, "")
	replicaB := newHTTPHandler(srv, "")

	post := func(h http.Handler, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://housekeeper.example.com/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	initRec := post(replicaA, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	if initRec.Code != http.StatusOK {
		t.Fatalf("initialize on replica A: status = %d, want 200", initRec.Code)
	}
	if got := initRec.Header().Get("Mcp-Session-Id"); got != "" {
		t.Errorf("initialize issued Mcp-Session-Id %q, want none", got)
	}

	listRec := post(replicaB, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if listRec.Code != http.StatusOK {
		t.Fatalf("tools/list on replica B: status = %d, want 200 (body %q)", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), `"tools"`) {
		t.Errorf("tools/list on replica B: body %q has no tools result", listRec.Body.String())
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
