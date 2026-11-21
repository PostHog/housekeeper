package main

import (
	"context"
	"os"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// TestDecodeSimpleAggregateFunctionLowCardinality guards the driver decode of
// SimpleAggregateFunction(max, LowCardinality(String)), a common column type in
// AggregatingMergeTree tables. Older clickhouse-go versions failed every read of
// such a column with "(col LowCardinality) additional keys bit is missing".
//
// Needs a ClickHouse server: set HOUSEKEEPER_TEST_CLICKHOUSE_ADDR (host:port of the
// native protocol, no TLS), and HOUSEKEEPER_TEST_CLICKHOUSE_PASSWORD if needed.
func TestDecodeSimpleAggregateFunctionLowCardinality(t *testing.T) {
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

	ctx := context.Background()
	if err := conn.Exec(ctx, "CREATE TEMPORARY TABLE saf_lc (k UInt8, u SimpleAggregateFunction(max, LowCardinality(String))) ENGINE = Memory"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := conn.Exec(ctx, "INSERT INTO saf_lc SELECT number, toString(number % 3) FROM numbers(10)"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := conn.Query(ctx, "SELECT u, count() FROM saf_lc GROUP BY u ORDER BY u")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Logf("close rows: %v", err)
		}
	}()

	got := map[string]uint64{}
	for rows.Next() {
		var u string
		var n uint64
		if err := rows.Scan(&u, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[u] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := map[string]uint64{"0": 4, "1": 3, "2": 3}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("u=%q: got %d rows, want %d (all: %v)", k, got[k], v, got)
		}
	}
}
