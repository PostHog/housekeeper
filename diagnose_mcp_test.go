package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestRunSQLTimeout(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	maxQuery := 60 * time.Second
	tests := []struct {
		name     string
		deadline time.Time
		want     time.Duration
	}{
		{"no budget uses the cap", time.Time{}, maxQuery},
		{"long budget uses the cap", now.Add(300 * time.Second), maxQuery},
		{"short time left shortens the timeout", now.Add(20 * time.Second), 20 * time.Second},
		{"almost no time left uses the floor", now.Add(time.Second), minQueryTimeout},
		{"past the budget uses the floor", now.Add(-time.Minute), minQueryTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runSQLTimeout(now, tt.deadline, maxQuery); got != tt.want {
				t.Errorf("runSQLTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRunSQLMaxExecutionTime guards the diagnose budget bug: the driver turns a
// context deadline into max_execution_time = time left + 5s, so an async job
// with budget_seconds=300 (deadline 300s + grace) asked for more than a profile
// capped at 300 allows, and every run_sql query failed with code 452.
//
// Needs a ClickHouse server: set HOUSEKEEPER_TEST_CLICKHOUSE_ADDR (host:port of the
// native protocol, no TLS), and HOUSEKEEPER_TEST_CLICKHOUSE_PASSWORD if needed.
func TestRunSQLMaxExecutionTime(t *testing.T) {
	addr := os.Getenv("HOUSEKEEPER_TEST_CLICKHOUSE_ADDR")
	if addr == "" {
		t.Skip("HOUSEKEEPER_TEST_CLICKHOUSE_ADDR not set")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{
			Database: "default",
			Username: "default",
			Password: os.Getenv("HOUSEKEEPER_TEST_CLICKHOUSE_PASSWORD"),
		},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close connection: %v", err)
		}
	}()

	const budget = 300
	maxQuery := 60 * time.Second
	// The async job's context: the budget plus the summarize grace.
	ctx, cancel := context.WithTimeout(context.Background(), budget*time.Second+diagnoseGrace)
	defer cancel()
	handle := runSQLHandler(ctx, conn, time.Now().Add(budget*time.Second), maxQuery)

	out, err := handle("run_sql", map[string]any{"sql": "SELECT toUInt64(getSetting('max_execution_time')) AS met FROM system.one"})
	if err != nil {
		t.Fatalf("run_sql: %v", err)
	}
	start := strings.Index(out, "[")
	end := strings.LastIndex(out, "]")
	if start < 0 || end < start {
		t.Fatalf("no rows in %q", out)
	}
	var rows []struct {
		Met uint64 `json:"met"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("decode %q: %v", out, err)
	}
	limit := uint64(maxQuery.Seconds()) + 5
	if rows[0].Met == 0 || rows[0].Met > limit {
		t.Errorf("max_execution_time = %d, want 1..%d", rows[0].Met, limit)
	}
}
