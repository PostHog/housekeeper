package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/common/model"
)

func TestValidateAndParseTimeRange_RejectsFutureStart(t *testing.T) {
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	_, _, err := validateAndParseTimeRange(future, "")
	if err == nil {
		t.Fatal("expected error for future start time, got nil")
	}
	if !strings.Contains(err.Error(), "future") {
		t.Errorf("expected error to mention 'future', got: %v", err)
	}
	if !strings.Contains(err.Error(), "current UTC is") {
		t.Errorf("expected error to surface current UTC, got: %v", err)
	}
}

func TestValidateAndParseTimeRange_RejectsFutureEnd(t *testing.T) {
	start := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	_, _, err := validateAndParseTimeRange(start, end)
	if err == nil {
		t.Fatal("expected error for future end time, got nil")
	}
	if !strings.Contains(err.Error(), "future") {
		t.Errorf("expected error to mention 'future', got: %v", err)
	}
}

func TestValidateAndParseTimeRange_AllowsSmallClockSkew(t *testing.T) {
	// 5 seconds in the future should be tolerated (within the 30s skew window)
	nearFuture := time.Now().UTC().Add(5 * time.Second).Format(time.RFC3339)
	_, _, err := validateAndParseTimeRange("-1h", nearFuture)
	if err != nil {
		t.Errorf("expected small clock-skew to be tolerated, got: %v", err)
	}
}

func TestValidateAndParseTimeRange_AcceptsPastRange(t *testing.T) {
	start := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	_, _, err := validateAndParseTimeRange(start, end)
	if err != nil {
		t.Errorf("expected past range to be accepted, got: %v", err)
	}
}

func TestValidateAndParseTimeRange_AcceptsRelativeStart(t *testing.T) {
	_, _, err := validateAndParseTimeRange("-30m", "")
	if err != nil {
		t.Errorf("expected relative start to be accepted, got: %v", err)
	}
}

func TestValidateAndParseTimeRange_RejectsStartAfterEnd(t *testing.T) {
	start := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	_, _, err := validateAndParseTimeRange(start, end)
	if err == nil {
		t.Fatal("expected error for start > end, got nil")
	}
	if !strings.Contains(err.Error(), "before") {
		t.Errorf("expected error to mention ordering, got: %v", err)
	}
}

func TestValidateAndParseTimeRange_DefaultsStartToAnHourBeforeEnd(t *testing.T) {
	start, end, err := validateAndParseTimeRange("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := end.Sub(start); got != time.Hour {
		t.Errorf("default window = %v, want 1h", got)
	}

	start, end, err = validateAndParseTimeRange("", "-3h")
	if err != nil {
		t.Fatalf("an empty start with a past end: %v", err)
	}
	if got := end.Sub(start); got != time.Hour {
		t.Errorf("default window = %v, want 1h before end", got)
	}
}

func TestResolveStep(t *testing.T) {
	end := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name    string
		step    string
		window  time.Duration
		want    time.Duration
		wantErr string
	}{
		{"default for an hour", "", time.Hour, 72 * time.Second, ""},
		{"default rounds up to whole seconds", "", 1001 * time.Second, 21 * time.Second, ""},
		{"default has a 15s floor", "", 5 * time.Minute, 15 * time.Second, ""},
		{"default for a week", "", 7 * 24 * time.Hour, 12096 * time.Second, ""},
		{"explicit step", "1m", time.Hour, time.Minute, ""},
		{"zero step", "0s", time.Hour, 0, "positive"},
		{"negative step", "-1m", time.Hour, 0, "positive"},
		{"unparsable step", "soon", time.Hour, 0, "invalid step"},
		{"too many points", "1s", 24 * time.Hour, 0, "at least 8s"},
		{"exactly the max points", "1s", (promMaxPoints - 1) * time.Second, time.Second, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveStep(tt.step, end.Add(-tt.window), end)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("resolveStep() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("resolveStep() = (%v, %v), want %v", got, err, tt.want)
			}
		})
	}
}

func testMatrix(series, points int) model.Matrix {
	m := make(model.Matrix, 0, series)
	for i := range series {
		values := make([]model.SamplePair, points)
		for j := range values {
			values[j] = model.SamplePair{Timestamp: model.Time(j * 1000), Value: model.SampleValue(j)}
		}
		m = append(m, &model.SampleStream{Metric: model.Metric{"i": model.LabelValue(fmt.Sprint(i))}, Values: values})
	}
	return m
}

func rawSamples(m model.Matrix) int {
	n := 0
	for _, s := range m {
		n += len(s.Values)
	}
	return n
}

func TestSummarizePromResultCaps(t *testing.T) {
	tests := []struct {
		name          string
		series        int
		points        int
		wantTruncated bool
		wantLast      int
		wantSamples   int
	}{
		{"small result is untouched", 3, 10, false, 3, 30},
		{"too many series", promMaxSeries + 20, 1, true, promMaxSeries, promMaxSeries},
		{"too many samples", 20, 1000, true, 20, promMaxRawSamples},
		{"one long series keeps its latest samples", 1, promMaxPoints, true, 1, promMaxRawSamples},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := summarizePromResult(testMatrix(tt.series, tt.points))
			if err != nil {
				t.Fatal(err)
			}
			m := out.(map[string]interface{})
			raw := m["raw_result"].(model.Matrix)
			if m["truncated"] != tt.wantTruncated || m["series_total"] != tt.series {
				t.Errorf("truncated = %v, series_total = %v; want %v, %d", m["truncated"], m["series_total"], tt.wantTruncated, tt.series)
			}
			if got := len(m["last_values"].([]map[string]interface{})); got != tt.wantLast {
				t.Errorf("last_values = %d, want %d", got, tt.wantLast)
			}
			if got := rawSamples(raw); got != tt.wantSamples {
				t.Errorf("raw samples = %d, want %d", got, tt.wantSamples)
			}
			last := raw[0].Values[len(raw[0].Values)-1]
			if want := model.SampleValue(tt.points - 1); last.Value != want {
				t.Errorf("latest raw sample = %v, want %v", last.Value, want)
			}
		})
	}
}

// TestPrometheusToolEndToEnd calls the tool through an MCP session against a
// fake Prometheus, covering the defaults and the per-call timeout.
func TestPrometheusToolEndToEnd(t *testing.T) {
	var gotStep string
	slow := make(chan struct{})
	t.Cleanup(func() { close(slow) })
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("query") == "slow" {
			select {
			case <-slow:
			case <-r.Context().Done():
			}
			return
		}
		gotStep = r.Form.Get("step")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"job":"ch"},"values":[[1800000000,"1"],[1800000060,"2"]]}]}}`))
	}))
	defer prom.Close()

	u, err := url.Parse(prom.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	setConfig(t, map[string]any{
		"test_prom.host": u.Hostname(), "test_prom.port": port,
		"prometheus.query_timeout_seconds": 1,
	})
	client, err := initPromClient("test_prom")
	if err != nil {
		t.Fatal(err)
	}
	promClients["test"] = client
	t.Cleanup(func() { delete(promClients, "test") })

	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	registerPrometheusTool(srv, "prometheus_query", "t", "d", "test")
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "prometheus_query", Arguments: map[string]any{"query": "up"}})
	if err != nil || res.IsError {
		t.Fatalf("CallTool = %+v, %v", res, err)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `job="ch"`) || !strings.HasSuffix(text, ": 2") {
		t.Errorf("summary = %q, want the last value of the series", text)
	}
	if gotStep != "72" {
		t.Errorf("step sent = %q, want 72 (1h default window / 50)", gotStep)
	}

	started := time.Now()
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "prometheus_query", Arguments: map[string]any{"query": "slow"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || time.Since(started) > 5*time.Second {
		t.Errorf("slow query: IsError = %v after %v, want a timeout error after ~1s", res.IsError, time.Since(started))
	}
}
