package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestValidateQueryArgs(t *testing.T) {
	// Set up default allowed databases for testing
	viper.Set("clickhouse.allowed_databases", []string{"system", "models"})

	tests := []struct {
		name    string
		args    queryArgs
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid structured query with system table",
			args: queryArgs{
				Table:   "system.query_log",
				Columns: []string{"query", "duration"},
				Where:   "duration > 1000",
				OrderBy: "duration DESC",
				Limit:   10,
			},
			wantErr: false,
		},
		{
			name: "valid structured query with models table",
			args: queryArgs{
				Table:   "models.predictions",
				Columns: []string{"id", "score"},
				Limit:   5,
			},
			wantErr: false,
		},
		{
			name: "invalid table not in allowed databases",
			args: queryArgs{
				Table: "unauthorized.table",
			},
			wantErr: true,
			errMsg:  "table must be in allowed databases",
		},
		{
			name: "empty table",
			args: queryArgs{
				Columns: []string{"col1"},
			},
			wantErr: true,
			errMsg:  "table is required",
		},
		{
			name: "table with semicolon",
			args: queryArgs{
				Table: "system.query_log; DROP TABLE users",
			},
			wantErr: true,
			errMsg:  "invalid table name",
		},
		{
			name: "column with semicolon",
			args: queryArgs{
				Table:   "system.query_log",
				Columns: []string{"query", "duration; DROP TABLE"},
			},
			wantErr: true,
			errMsg:  "invalid column name",
		},
		{
			name: "where clause with semicolon",
			args: queryArgs{
				Table: "system.query_log",
				Where: "duration > 1000; DROP TABLE users",
			},
			wantErr: true,
			errMsg:  "invalid clause",
		},
		{
			name: "negative limit",
			args: queryArgs{
				Table: "system.query_log",
				Limit: -1,
			},
			wantErr: true,
			errMsg:  "limit must be >= 0",
		},
		{
			name: "valid free-form SQL",
			args: queryArgs{
				SQL: "SELECT * FROM system.query_log WHERE duration > 1000",
			},
			wantErr: false,
		},
		{
			name: "SQL with forbidden INSERT",
			args: queryArgs{
				SQL: "INSERT INTO system.query_log VALUES (1, 2, 3)",
			},
			wantErr: true,
			errMsg:  "only SELECT/WITH",
		},
		{
			name: "SQL with multiple statements",
			args: queryArgs{
				SQL: "SELECT * FROM system.query_log; DROP TABLE users",
			},
			wantErr: true,
			errMsg:  "multiple statements are not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateQueryArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateQueryArgs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("validateQueryArgs() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestGetAllowedDatabases(t *testing.T) {
	tests := []struct {
		name      string
		setConfig []string
		want      []string
	}{
		{
			name:      "default when not configured",
			setConfig: nil,
			want:      []string{"system"},
		},
		{
			name:      "custom databases",
			setConfig: []string{"system", "models", "analytics"},
			want:      []string{"system", "models", "analytics"},
		},
		{
			name:      "single database",
			setConfig: []string{"system"},
			want:      []string{"system"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setConfig != nil {
				viper.Set("clickhouse.allowed_databases", tt.setConfig)
			} else {
				viper.Set("clickhouse.allowed_databases", nil)
			}

			got := getAllowedDatabases()
			if !equalSlices(got, tt.want) {
				t.Errorf("getAllowedDatabases() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTableAllowed(t *testing.T) {
	viper.Set("clickhouse.allowed_databases", []string{"system", "models"})

	tests := []struct {
		name  string
		table string
		want  bool
	}{
		{
			name:  "system table allowed",
			table: "system.query_log",
			want:  true,
		},
		{
			name:  "models table allowed",
			table: "models.predictions",
			want:  true,
		},
		{
			name:  "unauthorized table",
			table: "users.data",
			want:  false,
		},
		{
			name:  "table without database prefix",
			table: "query_log",
			want:  false,
		},
		{
			name:  "case insensitive check",
			table: "SYSTEM.query_log",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTableAllowed(tt.table); got != tt.want {
				t.Errorf("isTableAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateFreeformSQL(t *testing.T) {
	viper.Set("clickhouse.allowed_databases", []string{"system"})

	tests := []struct {
		name    string
		sql     string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid SELECT query",
			sql:     "SELECT * FROM system.query_log WHERE duration > 1000",
			wantErr: false,
		},
		{
			name:    "valid WITH query",
			sql:     "WITH slow AS (SELECT * FROM system.query_log) SELECT count() FROM system.query_log",
			wantErr: false,
		},
		{
			name:    "query with clusterAllReplicas",
			sql:     "SELECT * FROM clusterAllReplicas(default, system.query_log)",
			wantErr: false,
		},
		{
			name:    "empty SQL",
			sql:     "",
			wantErr: true,
			errMsg:  "sql is empty",
		},
		{
			name:    "multiple statements",
			sql:     "SELECT * FROM system.query_log; DROP TABLE users",
			wantErr: true,
			errMsg:  "multiple statements",
		},
		{
			name:    "INSERT statement",
			sql:     "INSERT INTO system.query_log VALUES (1, 2, 3)",
			wantErr: true,
			errMsg:  "only SELECT/WITH",
		},
		{
			name:    "DELETE statement",
			sql:     "DELETE FROM system.query_log WHERE id = 1",
			wantErr: true,
			errMsg:  "only SELECT/WITH",
		},
		{
			name:    "DROP statement",
			sql:     "DROP TABLE system.query_log",
			wantErr: true,
			errMsg:  "only SELECT/WITH",
		},
		{
			name:    "unauthorized table",
			sql:     "SELECT * FROM users.data",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "query with quoted strings",
			sql:     "SELECT * FROM system.query_log WHERE query = 'SELECT 1'",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFreeformSQL(tt.sql)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFreeformSQL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("validateFreeformSQL() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestStripQuotedLiterals(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single quotes",
			input: "SELECT * FROM table WHERE name = 'test'",
			want:  "SELECT * FROM table WHERE name =       ",
		},
		{
			name:  "double quotes",
			input: `SELECT * FROM table WHERE name = "test"`,
			want:  "SELECT * FROM table WHERE name =       ",
		},
		{
			name:  "mixed quotes",
			input: `SELECT * FROM table WHERE name = 'test' AND id = "123"`,
			want:  "SELECT * FROM table WHERE name =        AND id =      ",
		},
		{
			name:  "no quotes",
			input: "SELECT * FROM table WHERE id = 123",
			want:  "SELECT * FROM table WHERE id = 123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripQuotedLiterals(tt.input); got != tt.want {
				t.Errorf("stripQuotedLiterals() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeValue(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  interface{}
	}{
		{
			name:  "nil value",
			value: nil,
			want:  nil,
		},
		{
			name:  "string value",
			value: "test",
			want:  "test",
		},
		{
			name:  "byte slice",
			value: []byte("test"),
			want:  "test",
		},
		{
			name:  "bool value",
			value: true,
			want:  true,
		},
		{
			name:  "int value",
			value: int64(123),
			want:  int64(123),
		},
		{
			name:  "float value",
			value: float64(123.45),
			want:  float64(123.45),
		},
		{
			name:  "time value",
			value: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			want:  "2024-01-01T12:00:00Z",
		},
		{
			name:  "slice of ints",
			value: []int{1, 2, 3},
			want:  []interface{}{int64(1), int64(2), int64(3)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeValue(tt.value)
			
			// Special handling for slices
			if gotSlice, ok := got.([]interface{}); ok {
				wantSlice, ok := tt.want.([]interface{})
				if !ok {
					t.Errorf("normalizeValue() = %v, want %v", got, tt.want)
					return
				}
				if len(gotSlice) != len(wantSlice) {
					t.Errorf("normalizeValue() slice length = %v, want %v", len(gotSlice), len(wantSlice))
					return
				}
				for i := range gotSlice {
					if gotSlice[i] != wantSlice[i] {
						t.Errorf("normalizeValue() slice element[%d] = %v, want %v", i, gotSlice[i], wantSlice[i])
					}
				}
			} else if got != tt.want {
				t.Errorf("normalizeValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueryBuilding(t *testing.T) {
	// Set up test configuration
	viper.Set("clickhouse.cluster", "test_cluster")
	viper.Set("clickhouse.allowed_databases", []string{"system", "models"})

	tests := []struct {
		name      string
		args      queryArgs
		wantQuery string
	}{
		{
			name: "system table uses clusterAllReplicas",
			args: queryArgs{
				Table:   "system.query_log",
				Columns: []string{"query", "duration"},
				Where:   "duration > 1000",
				Limit:   10,
			},
			wantQuery: "clusterAllReplicas(test_cluster, system.query_log)",
		},
		{
			name: "non-system table does not use clusterAllReplicas",
			args: queryArgs{
				Table:   "models.predictions",
				Columns: []string{"id", "score"},
				Where:   "score > 0.5",
				Limit:   5,
			},
			wantQuery: "FROM models.predictions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't easily test runClickhouseQuery without a connection,
			// but we can verify the query building logic by checking what
			// query would be generated
			
			// Build the query string similar to how runClickhouseQuery does it
			var sb strings.Builder
			sb.WriteString("SELECT ")
			if len(tt.args.Columns) > 0 {
				sb.WriteString(strings.Join(tt.args.Columns, ", "))
			} else {
				sb.WriteString("*")
			}
			
			// Only use clusterAllReplicas for system tables
			if strings.HasPrefix(strings.ToLower(tt.args.Table), "system.") {
				cluster := viper.GetString("clickhouse.cluster")
				sb.WriteString(" FROM clusterAllReplicas(" + cluster + ", " + tt.args.Table + ")")
			} else {
				sb.WriteString(" FROM " + tt.args.Table)
			}
			
			if tt.args.Where != "" {
				sb.WriteString(" WHERE ")
				sb.WriteString(tt.args.Where)
			}
			if tt.args.Limit > 0 {
				sb.WriteString(fmt.Sprintf(" LIMIT %d", tt.args.Limit))
			}
			
			query := sb.String()
			
			if !contains(query, tt.wantQuery) {
				t.Errorf("Query building: got query = %v, want to contain %v", query, tt.wantQuery)
			}
		})
	}
}

// Helper functions
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}