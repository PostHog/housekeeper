package main

import (
	"encoding/json"
	"math"
	"math/big"
	"net"
	"reflect"
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
		{
			name:    "structured table closing paren and UNION",
			args:    queryArgs{Table: "system.one) UNION ALL SELECT * FROM remote('h', other.t) --"},
			wantErr: true,
			errMsg:  "invalid table name",
		},
		{
			name:    "structured table comma join",
			args:    queryArgs{Table: "system.one, url('http://x/', CSV, 'a String')"},
			wantErr: true,
			errMsg:  "invalid table name",
		},
		{
			name:    "structured where UNION",
			args:    queryArgs{Table: "system.one", Where: "1 UNION ALL SELECT * FROM other.t"},
			wantErr: true,
			errMsg:  "invalid where clause",
		},
		{
			name:    "structured where SETTINGS",
			args:    queryArgs{Table: "system.one", Where: "1 SETTINGS max_execution_time = 0"},
			wantErr: true,
			errMsg:  "forbidden keyword detected: settings",
		},
		{
			name:    "structured where closing paren",
			args:    queryArgs{Table: "system.one", Where: "1) UNION ALL (SELECT 1"},
			wantErr: true,
			errMsg:  "invalid where clause",
		},
		{
			name:    "structured column scalar subquery with table function",
			args:    queryArgs{Table: "system.one", Columns: []string{"(SELECT count() FROM remote('h', other.t))"}},
			wantErr: true,
			errMsg:  "table functions other than clusterAllReplicas",
		},
		{
			name:    "structured column subquery on unauthorized table",
			args:    queryArgs{Table: "system.one", Columns: []string{"(SELECT count() FROM other.t)"}},
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "structured order_by comment swallows limit",
			args:    queryArgs{Table: "system.one", OrderBy: "1 --", Limit: 10},
			wantErr: true,
			errMsg:  "comments are not allowed",
		},
		{
			name:    "structured order_by LIMIT override",
			args:    queryArgs{Table: "system.one", OrderBy: "1 LIMIT 1000000"},
			wantErr: true,
			errMsg:  "invalid order_by clause",
		},
		{
			name:    "structured where IN unauthorized table",
			args:    queryArgs{Table: "system.one", Where: "dummy IN other.t"},
			wantErr: true,
			errMsg:  "IN with a table reference",
		},
		{
			name: "structured where with IN subquery and functions",
			args: queryArgs{
				Table:   "system.query_log",
				Columns: []string{"query_id", "toStartOfMinute(event_time) AS t", "extract(query, 'x')"},
				Where:   "event_date = today() AND query_id IN (SELECT query_id FROM system.processes)",
				OrderBy: "t DESC, query_id",
				Limit:   10,
			},
			wantErr: false,
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
		{
			name:    "multiline SELECT with newline after keyword",
			sql:     "SELECT\n    count()\nFROM system.query_log\nWHERE query_duration_ms > 1000",
			wantErr: false,
		},
		{
			name:    "multiline WITH with newline after keyword",
			sql:     "WITH\n    slow AS (SELECT query_id FROM system.query_log)\nSELECT count() FROM slow",
			wantErr: false,
		},
		{
			name:    "multiline FROM does not bypass table allowlist",
			sql:     "SELECT 1\nFROM users.data",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "dollar-sign property literals are allowed",
			sql:     "SELECT count() FROM system.query_log WHERE query LIKE '%$geoip_city_name%'",
			wantErr: false,
		},
		{
			name:    "backslash-escaped quote does not desync literal stripping",
			sql:     `SELECT count() FROM system.query_log WHERE query = 'it\'s from users.data'`,
			wantErr: false,
		},
		{
			name:    "CTE referenced by name in FROM",
			sql:     "WITH slow AS (SELECT query_duration_ms FROM system.query_log WHERE query_duration_ms > 1000) SELECT count() FROM slow",
			wantErr: false,
		},
		{
			name:    "CTE referenced by name in JOIN",
			sql:     "WITH slow AS (SELECT query_id FROM system.query_log) SELECT q.query_id FROM system.query_log q JOIN slow s ON q.query_id = s.query_id",
			wantErr: false,
		},
		{
			name:    "multiple CTEs, both referenced by name",
			sql:     "WITH a AS (SELECT 1 AS x FROM system.one), b AS (SELECT 2 AS y FROM system.one) SELECT * FROM a JOIN b ON a.x = b.y",
			wantErr: false,
		},
		{
			name:    "CTE name does not bypass unauthorized inner table",
			sql:     "WITH bad AS (SELECT * FROM users.data) SELECT * FROM bad",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "subquery as table in FROM",
			sql:     "SELECT t FROM (SELECT toStartOfMinute(event_time) AS t FROM system.query_log) sub",
			wantErr: false,
		},
		{
			name:    "subquery as table in JOIN",
			sql:     "SELECT * FROM system.query_log q JOIN (SELECT query_id FROM system.processes) p ON q.query_id = p.query_id",
			wantErr: false,
		},
		{
			name:    "subquery with unauthorized inner table is rejected",
			sql:     "SELECT * FROM (SELECT * FROM users.data) sub",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "IN subquery still works",
			sql:     "SELECT * FROM system.query_log WHERE query_id IN (SELECT query_id FROM system.processes)",
			wantErr: false,
		},

		// Bypasses of the previous scanner: each must be rejected.
		{
			name:    "clusterAllReplicas on unauthorized database",
			sql:     "SELECT * FROM clusterAllReplicas(default, other.t)",
			wantErr: true,
			errMsg:  "clusterAllReplicas must target tables from allowed databases",
		},
		{
			name:    "clusterAllReplicas on unauthorized database without space",
			sql:     "SELECT * FROM clusterAllReplicas(default,other.t)",
			wantErr: true,
			errMsg:  "clusterAllReplicas must target tables from allowed databases",
		},
		{
			name:    "clusterAllReplicas with nested table function",
			sql:     "SELECT * FROM clusterAllReplicas(default, view(SELECT 1))",
			wantErr: true,
			errMsg:  "clusterAllReplicas must target tables from allowed databases",
		},
		{
			name:    "clusterAllReplicas with extra argument",
			sql:     "SELECT * FROM clusterAllReplicas(default, system.one, (SELECT 1))",
			wantErr: true,
			errMsg:  "clusterAllReplicas must target tables from allowed databases",
		},
		{
			name:    "comma join with table function",
			sql:     "SELECT * FROM system.one, remote('h', other.t)",
			wantErr: true,
			errMsg:  "comma joins are not allowed",
		},
		{
			name:    "comma join with aliases",
			sql:     "SELECT * FROM system.one AS a, other.t AS b",
			wantErr: true,
			errMsg:  "comma joins are not allowed",
		},
		{
			name:    "comma join after subquery",
			sql:     "SELECT * FROM (SELECT 1 FROM system.one) s, other.t",
			wantErr: true,
			errMsg:  "comma joins are not allowed",
		},
		{
			name:    "block comment after FROM",
			sql:     "SELECT * FROM/**/remote('h', other.t)",
			wantErr: true,
			errMsg:  "comments are not allowed",
		},
		{
			name:    "block comment before FROM",
			sql:     "SELECT */**/FROM remote('h', other.t)",
			wantErr: true,
			errMsg:  "comments are not allowed",
		},
		{
			name:    "star glued to FROM",
			sql:     "SELECT *FROM url('http://x/', CSV, 'a String')",
			wantErr: true,
			errMsg:  "table functions other than clusterAllReplicas",
		},
		{
			name:    "paren glued to FROM",
			sql:     "SELECT (1)FROM s3('http://x/', CSV, 'a String')",
			wantErr: true,
			errMsg:  "table functions other than clusterAllReplicas",
		},
		{
			name:    "FROM glued to subquery paren",
			sql:     "SELECT * FROM(SELECT * FROM other.t)",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "table function with space before paren",
			sql:     "SELECT * FROM remote ('h', other.t)",
			wantErr: true,
			errMsg:  "table functions other than clusterAllReplicas",
		},
		{
			name:    "cluster table function",
			sql:     "SELECT * FROM cluster('c', system.one)",
			wantErr: true,
			errMsg:  "table functions other than clusterAllReplicas",
		},
		{
			name:    "spaced db . table",
			sql:     "SELECT * FROM other . t",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "JOIN to table function",
			sql:     "SELECT * FROM system.one AS a JOIN numbers(10) AS b ON 1 = 1",
			wantErr: true,
			errMsg:  "table functions other than clusterAllReplicas",
		},
		{
			name:    "backtick glued to FROM",
			sql:     "SELECT 1 AS `x`FROM file('a.csv', CSV, 'a String')",
			wantErr: true,
			errMsg:  "backtick-quoted identifiers are not allowed",
		},
		{
			name:    "form feed whitespace",
			sql:     "SELECT 1 FROM\fremote('h', other.t)",
			wantErr: true,
			errMsg:  "control characters are not allowed",
		},
		{
			name:    "vertical tab whitespace",
			sql:     "SELECT 1\vFROM remote('h', other.t)",
			wantErr: true,
			errMsg:  "control characters are not allowed",
		},
		{
			name:    "NUL byte",
			sql:     "SELECT 1 FROM\x00remote('h', other.t)",
			wantErr: true,
			errMsg:  "control characters are not allowed",
		},
		{
			name:    "unicode no-break space folds to whitespace",
			sql:     "SELECT 1 FROM other.t",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "quote inside backtick identifier",
			sql:     "SELECT 1 AS `'` FROM url('http://x/', CSV, 'a String')",
			wantErr: true,
			errMsg:  "backtick-quoted identifiers are not allowed",
		},
		{
			name:    "quote inside line comment",
			sql:     "SELECT 1 FROM system.one -- it's\nUNION ALL SELECT * FROM remote('h', other.t) --'",
			wantErr: true,
			errMsg:  "comments are not allowed",
		},
		{
			name:    "quote inside block comment",
			sql:     "SELECT 1 /* ' */ FROM remote('h', other.t) /* ' */",
			wantErr: true,
			errMsg:  "comments are not allowed",
		},
		{
			name:    "hash comment",
			sql:     "SELECT 1 # '\nFROM remote('h', other.t) #'",
			wantErr: true,
			errMsg:  "comments are not allowed",
		},
		{
			name:    "quote inside heredoc literal",
			sql:     "SELECT $$'$$ AS a FROM remote('h', other.t) AS b WHERE 1 = '1'",
			wantErr: true,
			errMsg:  "heredoc",
		},
		{
			name:    "IN bare unauthorized table",
			sql:     "SELECT count() FROM system.one WHERE dummy IN other.t",
			wantErr: true,
			errMsg:  "IN with a table reference",
		},
		{
			name:    "IN parenthesized unauthorized table",
			sql:     "SELECT count() FROM system.one WHERE dummy IN (other.t)",
			wantErr: true,
			errMsg:  "IN with a table reference",
		},
		{
			name:    "NOT IN bare current-database table",
			sql:     "SELECT count() FROM system.one WHERE dummy NOT IN t",
			wantErr: true,
			errMsg:  "IN with a table reference",
		},
		{
			name:    "functional in() with unauthorized table",
			sql:     "SELECT in(0, other.t)",
			wantErr: true,
			errMsg:  "IN with a table reference",
		},
		{
			name:    "functional globalNotIn() with unauthorized table",
			sql:     "SELECT globalNotIn(0, other.t)",
			wantErr: true,
			errMsg:  "IN with a table reference",
		},
		{
			name:    "SETTINGS override",
			sql:     "SELECT * FROM system.one SETTINGS max_execution_time = 0, max_result_rows = 0",
			wantErr: true,
			errMsg:  "forbidden keyword detected: settings",
		},
		{
			name:    "FORMAT clause",
			sql:     "SELECT * FROM system.one FORMAT JSON",
			wantErr: true,
			errMsg:  "forbidden keyword detected: format",
		},
		{
			name:    "INTO OUTFILE",
			sql:     "SELECT * FROM system.one INTO OUTFILE 'x'",
			wantErr: true,
			errMsg:  "forbidden keyword detected: into",
		},
		{
			name:    "SETTINGS glued to punctuation",
			sql:     "SELECT 1 FROM system.one WHERE (dummy = 0)SETTINGS max_threads = 1",
			wantErr: true,
			errMsg:  "forbidden keyword detected: settings",
		},
		{
			name:    "EXPLAIN",
			sql:     "EXPLAIN SELECT * FROM other.t",
			wantErr: true,
			errMsg:  "only SELECT/WITH",
		},
		{
			name:    "SHOW",
			sql:     "SHOW TABLES FROM other",
			wantErr: true,
			errMsg:  "only SELECT/WITH",
		},
		{
			name:    "DESCRIBE",
			sql:     "DESCRIBE TABLE other.t",
			wantErr: true,
			errMsg:  "only SELECT/WITH",
		},
		{
			name:    "parenthesized top-level query",
			sql:     "(SELECT * FROM other.t)",
			wantErr: true,
			errMsg:  "only SELECT/WITH",
		},
		{
			name:    "UNION branch on unauthorized table",
			sql:     "SELECT dummy FROM system.one UNION ALL SELECT dummy FROM other.t",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "scalar subquery in WHERE on table function",
			sql:     "SELECT * FROM system.one WHERE dummy = (SELECT 1 FROM remote('h', other.t))",
			wantErr: true,
			errMsg:  "table functions other than clusterAllReplicas",
		},
		{
			name:    "subquery inside EXTRACT still checked",
			sql:     "SELECT EXTRACT(DAY FROM (SELECT max(t) FROM other.t))",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "CTE name declared in a subquery does not leak outward",
			sql:     "SELECT * FROM (WITH t AS (SELECT 1 FROM system.one) SELECT * FROM t) AS s JOIN t ON 1 = 1",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "CTE name used before its declaration",
			sql:     "SELECT * FROM t UNION ALL WITH t AS (SELECT 1 FROM system.one) SELECT * FROM t",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "scalar WITH alias is not a table",
			sql:     "WITH (SELECT 1 FROM system.one) AS t SELECT * FROM t",
			wantErr: true,
			errMsg:  "only tables from allowed databases",
		},
		{
			name:    "unbalanced parentheses",
			sql:     "SELECT (1 FROM system.one",
			wantErr: true,
			errMsg:  "unbalanced parentheses",
		},

		{
			name:    "IN double-quoted unauthorized table",
			sql:     `SELECT count() FROM system.one WHERE dummy IN "other"."t"`,
			wantErr: true,
			errMsg:  "double-quoted table reference",
		},
		{
			name:    "IN parenthesized double-quoted table",
			sql:     `SELECT count() FROM system.one WHERE dummy IN ("other"."t")`,
			wantErr: true,
			errMsg:  "double-quoted table reference",
		},
		{
			name:    "double-quoted table in FROM",
			sql:     `SELECT * FROM "other"."t"`,
			wantErr: true,
			errMsg:  "double-quoted table names are not supported",
		},
		{
			name:    "double-quoted CTE name does not shadow a table",
			sql:     `WITH "s" AS (SELECT 1 FROM system.one) SELECT * FROM "t"`,
			wantErr: true,
			errMsg:  "double-quoted table names are not supported",
		},
		{
			name:    "file() scalar function",
			sql:     "SELECT file('x.csv')",
			wantErr: true,
			errMsg:  "function file() is not allowed",
		},
		{
			name:    "joinGet() on a table",
			sql:     "SELECT joinGet(other.t, 'v', 1)",
			wantErr: true,
			errMsg:  "function joinget() is not allowed",
		},

		// Legitimate shapes that must keep working.
		{
			name:    "ARRAY JOIN on an array expression",
			sql:     "SELECT x FROM system.one ARRAY JOIN [1, 2] AS x",
			wantErr: false,
		},
		{
			name:    "LEFT ARRAY JOIN with several arrays",
			sql:     "SELECT a, b FROM system.one LEFT ARRAY JOIN [1, 2] AS a, [3, 4] AS b",
			wantErr: false,
		},
		{
			name:    "EXTRACT(part FROM column)",
			sql:     "SELECT EXTRACT(DAY FROM event_time) FROM system.query_log",
			wantErr: false,
		},
		{
			name:    "trim(BOTH ... FROM column)",
			sql:     "SELECT trim(BOTH ' ' FROM query) FROM system.query_log",
			wantErr: false,
		},
		{
			name:    "substring(column FROM n)",
			sql:     "SELECT substring(query FROM 1 FOR 10) FROM system.query_log",
			wantErr: false,
		},
		{
			name:    "CTE used inside a FROM subquery",
			sql:     "WITH s AS (SELECT 1 AS x FROM system.one) SELECT * FROM (SELECT x FROM s)",
			wantErr: false,
		},
		{
			name:    "CTE used in a scalar subquery and IN",
			sql:     "WITH s AS (SELECT query_id FROM system.processes) SELECT count() FROM system.query_log WHERE query_id IN s AND (SELECT count() FROM s) > 0",
			wantErr: false,
		},
		{
			name:    "CTE visible in a later UNION branch",
			sql:     "WITH s AS (SELECT dummy FROM system.one) SELECT dummy FROM s UNION ALL SELECT dummy FROM s",
			wantErr: false,
		},
		{
			name:    "scalar WITH items mixed with CTEs",
			sql:     "WITH 1000 AS threshold, slow AS (SELECT query_id FROM system.query_log WHERE query_duration_ms > threshold) SELECT count() FROM slow",
			wantErr: false,
		},
		{
			name:    "semicolon inside a string literal",
			sql:     "SELECT count() FROM system.query_log WHERE query LIKE '%;%'",
			wantErr: false,
		},
		{
			name:    "comment markers inside a string literal",
			sql:     "SELECT count() FROM system.query_log WHERE query LIKE '%--%' OR query LIKE '%/*%' OR query LIKE '%#%'",
			wantErr: false,
		},
		{
			name:    "forbidden words inside a string literal",
			sql:     "SELECT count() FROM system.query_log WHERE query ILIKE '%insert into%settings%'",
			wantErr: false,
		},
		{
			name:    "lowercase with tab after select",
			sql:     "select\tcount() from system.one",
			wantErr: false,
		},
		{
			name:    "clusterAllReplicas with quoted cluster",
			sql:     "SELECT count() FROM clusterAllReplicas('default', system.query_log) WHERE event_date = today()",
			wantErr: false,
		},
		{
			name:    "explicit JOIN with ON and USING",
			sql:     "SELECT * FROM system.query_log AS q LEFT JOIN system.processes AS p USING (query_id) INNER JOIN system.one AS o ON 1 = 1",
			wantErr: false,
		},
		{
			name:    "IN with literal list and tuple",
			sql:     "SELECT count() FROM system.query_log WHERE type IN ('QueryFinish', 'ExceptionWhileProcessing') AND (1, 2) IN ((1, 2), (3, 4)) AND is_initial_query IN (true, NULL)",
			wantErr: false,
		},
		{
			name:    "IN on allowed table",
			sql:     "SELECT count() FROM system.one WHERE dummy IN system.one",
			wantErr: false,
		},
		{
			name:    "double-quoted column identifiers",
			sql:     `SELECT "query_id", "type" FROM system.query_log WHERE "type" IN ('QueryFinish')`,
			wantErr: false,
		},
		{
			name:    "format() function",
			sql:     "SELECT format('{} {}', 'a', 'b') FROM system.one",
			wantErr: false,
		},
		{
			name:    "qualified column named like a keyword",
			sql:     "SELECT t.format, t.set FROM system.one AS t",
			wantErr: false,
		},
		{
			name:    "GROUP BY, ORDER BY and LIMIT BY with commas",
			sql:     "SELECT user, query_kind, count() FROM system.query_log GROUP BY user, query_kind ORDER BY user, query_kind LIMIT 1 BY user",
			wantErr: false,
		},
		{
			name:    "scalar subquery in select list followed by more columns",
			sql:     "SELECT (SELECT max(event_time) FROM system.query_log) AS latest, now() AS ts",
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

// stringerValue stands in for driver types that implement fmt.Stringer
// (uuid.UUID, decimal.Decimal) without importing them here.
type stringerValue [4]byte

func (s stringerValue) String() string { return "stringer" }

func TestNormalizeValue(t *testing.T) {
	str := "x"
	var nilStr *string
	n := int64(7)
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	tests := []struct {
		name  string
		value interface{}
		want  interface{}
	}{
		{name: "nil value", value: nil, want: nil},
		{name: "string value", value: "test", want: "test"},
		{name: "Array(UInt8) renders as numbers", value: []uint8{1, 2}, want: []interface{}{uint64(1), uint64(2)}},
		{name: "bool value", value: true, want: true},
		{name: "int value", value: int64(123), want: int64(123)},
		{name: "int8 widens to int64", value: int8(-5), want: int64(-5)},
		{name: "int64 beyond 2^53 is a string", value: int64(1<<53 + 1), want: "9007199254740993"},
		{name: "negative int64 beyond 2^53 is a string", value: int64(-(1<<53 + 1)), want: "-9007199254740993"},
		{name: "uint64 at 2^53-1 stays numeric", value: uint64(1<<53 - 1), want: uint64(1<<53 - 1)},
		{name: "uint64 beyond 2^53 is a string", value: uint64(18446744073709551615), want: "18446744073709551615"},
		{name: "float value", value: float64(123.45), want: float64(123.45)},
		{name: "float32 widens", value: float32(0.5), want: float64(0.5)},
		{name: "NaN", value: math.NaN(), want: "NaN"},
		{name: "+Inf", value: math.Inf(1), want: "+Inf"},
		{name: "-Inf", value: float32(math.Inf(-1)), want: "-Inf"},
		{name: "time value", value: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC), want: "2024-01-01T12:00:00Z"},
		{name: "time with zone renders as UTC", value: time.Date(2024, 1, 1, 0, 0, 0, 500, nyc), want: "2024-01-01T05:00:00.0000005Z"},
		{name: "UUID-like stringer array", value: stringerValue{1, 2, 3, 4}, want: "stringer"},
		{name: "IPv4 net.IP", value: net.ParseIP("10.0.0.1").To4(), want: "10.0.0.1"},
		{name: "IPv6 net.IP", value: net.ParseIP("::1"), want: "::1"},
		{name: "big.Int pointer", value: new(big.Int).Lsh(big.NewInt(1), 100), want: "1267650600228229401496703205376"},
		{name: "nil big.Int pointer", value: (*big.Int)(nil), want: nil},
		{name: "pointer is dereferenced", value: &str, want: "x"},
		{name: "nil pointer is null", value: nilStr, want: nil},
		{name: "pointer to pointer", value: &[]*int64{&n}[0], want: int64(7)},
		{name: "slice of ints", value: []int{1, 2, 3}, want: []interface{}{int64(1), int64(2), int64(3)}},
		{name: "nil slice", value: []string(nil), want: []interface{}{}},
		{name: "array of nullable strings", value: []*string{&str, nil}, want: []interface{}{"x", nil}},
		{name: "nested arrays", value: [][]uint8{{1}, {2, 3}}, want: []interface{}{[]interface{}{uint64(1)}, []interface{}{uint64(2), uint64(3)}}},
		{name: "tuple as []any", value: []interface{}{uint8(1), "a", nil}, want: []interface{}{uint64(1), "a", nil}},
		{name: "map with string keys", value: map[string]*string{"a": &str, "b": nil}, want: map[string]interface{}{"a": "x", "b": nil}},
		{name: "map with integer keys", value: map[uint64]string{1: "a"}, want: map[string]interface{}{"1": "a"}},
		{name: "map with stringer keys", value: map[stringerValue]int32{{}: 1}, want: map[string]interface{}{"stringer": int64(1)}},
		{name: "unknown struct falls back to fmt.Sprint", value: struct{ A int }{1}, want: "{1}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeValue(tt.value)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("normalizeValue() = %#v, want %#v", got, tt.want)
			}
			if _, err := json.Marshal(got); err != nil {
				t.Errorf("normalizeValue() result is not JSON-encodable: %v", err)
			}
		})
	}
}

func TestQueryBuilding(t *testing.T) {
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
			wantQuery: "SELECT query, duration FROM clusterAllReplicas(test_cluster, system.query_log) WHERE duration > 1000 LIMIT 10",
		},
		{
			name: "non-system table does not use clusterAllReplicas",
			args: queryArgs{
				Table:   "models.predictions",
				Columns: []string{"id", "score"},
				Where:   "score > 0.5",
				OrderBy: "score DESC",
				Limit:   5,
			},
			wantQuery: "SELECT id, score FROM models.predictions WHERE score > 0.5 ORDER BY score DESC LIMIT 5",
		},
		{
			name:      "defaults to star and no limit",
			args:      queryArgs{Table: " SYSTEM.one "},
			wantQuery: "SELECT * FROM clusterAllReplicas(test_cluster, SYSTEM.one)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := buildStructuredQuery(tt.args)
			if query != tt.wantQuery {
				t.Errorf("buildStructuredQuery() = %q, want %q", query, tt.wantQuery)
			}
			if err := validateQueryArgs(tt.args); err != nil {
				t.Errorf("validateQueryArgs() = %v, want nil", err)
			}
		})
	}
}

func TestMaxResultRows(t *testing.T) {
	defer viper.Set("clickhouse.max_result_rows", nil)
	tests := []struct {
		name string
		set  interface{}
		want int
	}{
		{name: "unset uses default", set: nil, want: defaultMaxResultRows},
		{name: "zero uses default", set: 0, want: defaultMaxResultRows},
		{name: "explicit value", set: 50, want: 50},
		{name: "negative disables the cap", set: -1, want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Set("clickhouse.max_result_rows", tt.set)
			if got := maxResultRows(); got != tt.want {
				t.Errorf("maxResultRows() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNormalizeWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "ascii whitespace", input: "a\tb\nc\rd", want: "a b c d"},
		{name: "form feed and vertical tab", input: "a\fb\vc", want: "a b c"},
		{name: "unicode spaces", input: "a\u00a0b\u2003c\u3000d", want: "a b c d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeWhitespace(tt.input); got != tt.want {
				t.Errorf("normalizeWhitespace() = %q, want %q", got, tt.want)
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
