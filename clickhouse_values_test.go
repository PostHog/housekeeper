package main

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// liveConn opens a native-protocol connection to the test server, or skips.
// Needs HOUSEKEEPER_TEST_CLICKHOUSE_ADDR (host:port, no TLS), and
// HOUSEKEEPER_TEST_CLICKHOUSE_PASSWORD if needed.
func liveConn(t *testing.T) driver.Conn {
	t.Helper()
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
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	})
	return conn
}

// TestQueryRowsCappedValues decodes driver column types end to end through the
// same scan + normalizeValue path clickhouse_query uses, and checks the result
// is JSON-encodable.
func TestQueryRowsCappedValues(t *testing.T) {
	conn := liveConn(t)

	tests := []struct {
		name string
		sql  string
		want map[string]interface{}
	}{
		{
			name: "UUID and IP addresses render as strings",
			sql:  "SELECT toUUID('1b4e28ba-2fa1-11d2-883f-0016d3cca427') AS u, toIPv4('10.0.0.1') AS ip4, toIPv6('::1') AS ip6",
			want: map[string]interface{}{"u": "1b4e28ba-2fa1-11d2-883f-0016d3cca427", "ip4": "10.0.0.1", "ip6": "::1"},
		},
		{
			name: "Nullable and LowCardinality(Nullable) values and NULLs",
			sql:  "SELECT toNullable('a') AS n, CAST(NULL, 'Nullable(String)') AS nn, toLowCardinality(toNullable('x')) AS lcn, CAST(NULL, 'LowCardinality(Nullable(String))') AS lcnn",
			want: map[string]interface{}{"n": "a", "nn": nil, "lcn": "x", "lcnn": nil},
		},
		{
			name: "arrays of nullable, nested arrays, maps and tuples",
			sql:  "SELECT [toNullable('a'), NULL] AS arrn, [[1, 2], [3]] AS nested, [toUInt8(7)] AS bytes, map(1, 'a') AS m, map('k', toNullable(1)) AS ms, tuple(1, 'a') AS tup",
			want: map[string]interface{}{
				"arrn":   []interface{}{"a", nil},
				"nested": []interface{}{[]interface{}{uint64(1), uint64(2)}, []interface{}{uint64(3)}},
				"bytes":  []interface{}{uint64(7)},
				"m":      map[string]interface{}{"1": "a"},
				"ms":     map[string]interface{}{"k": uint64(1)},
				"tup":    []interface{}{uint64(1), "a"},
			},
		},
		{
			name: "large integers, decimals and time zones",
			sql:  "SELECT toUInt64(18446744073709551615) AS big, toInt128(5) AS i128, toDecimal64(1.23, 4) AS d, toDateTime('2024-01-01 00:00:00', 'America/New_York') AS dt",
			want: map[string]interface{}{"big": "18446744073709551615", "i128": "5", "d": "1.23", "dt": "2024-01-01T05:00:00Z"},
		},
		{
			name: "NaN and infinities",
			sql:  "SELECT nan AS n, 1/0 AS pinf, -1/0 AS ninf",
			want: map[string]interface{}{"n": "NaN", "pinf": "+Inf", "ninf": "-Inf"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, truncated, err := queryRowsCapped(context.Background(), conn, tt.sql, 10)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if truncated || len(rows) != 1 {
				t.Fatalf("got %d rows (truncated=%v), want 1", len(rows), truncated)
			}
			if !reflect.DeepEqual(rows[0], tt.want) {
				t.Errorf("row = %#v, want %#v", rows[0], tt.want)
			}
			if _, err := json.Marshal(rows); err != nil {
				t.Errorf("rows are not JSON-encodable: %v", err)
			}
		})
	}
}

func TestQueryRowsCappedLimits(t *testing.T) {
	conn := liveConn(t)

	tests := []struct {
		name          string
		sql           string
		maxRows       int
		wantRows      int
		wantTruncated bool
	}{
		{name: "empty result", sql: "SELECT number FROM numbers(0)", maxRows: 10, wantRows: 0},
		{name: "under the cap", sql: "SELECT number FROM numbers(5)", maxRows: 10, wantRows: 5},
		{name: "exactly the cap", sql: "SELECT number FROM numbers(10)", maxRows: 10, wantRows: 10},
		{name: "over the cap is truncated", sql: "SELECT number FROM numbers(1000000000)", maxRows: 10, wantRows: 10, wantTruncated: true},
		{name: "no cap", sql: "SELECT number FROM numbers(20000)", maxRows: -1, wantRows: 20000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, truncated, err := queryRowsCapped(context.Background(), conn, tt.sql, tt.maxRows)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(rows) != tt.wantRows || truncated != tt.wantTruncated {
				t.Errorf("got %d rows (truncated=%v), want %d (truncated=%v)", len(rows), truncated, tt.wantRows, tt.wantTruncated)
			}
		})
	}
}
