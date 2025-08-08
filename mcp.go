package main

import (
    "context"
    "fmt"
    "strings"

    "github.com/spf13/viper"
)

// JSON-RPC transport types
type queryArgs struct {
    Table   string   `json:"table"`
    Columns []string `json:"columns,omitempty"`
    Where   string   `json:"where,omitempty"`
    OrderBy string   `json:"order_by,omitempty"`
    Limit   int      `json:"limit,omitempty"`
}

// (SDK server implemented in sdk_mcp.go)

func validateQueryArgs(a queryArgs) error {
	if a.Table == "" {
		return fmt.Errorf("table is required")
	}
	t := strings.TrimSpace(a.Table)
	if !strings.HasPrefix(t, "system.") {
		return fmt.Errorf("only system.* tables are allowed")
	}
	if strings.ContainsAny(t, ";\n\r\t") {
		return fmt.Errorf("invalid table name")
	}
	for _, c := range a.Columns {
		if strings.ContainsAny(c, ";\n\r\t") || c == "" {
			return fmt.Errorf("invalid column name: %q", c)
		}
	}
	if strings.Contains(a.Where, ";") || strings.Contains(a.OrderBy, ";") {
		return fmt.Errorf("invalid clause")
	}
	if a.Limit < 0 {
		return fmt.Errorf("limit must be >= 0")
	}
	return nil
}

func runClickhouseQuery(a queryArgs) ([]map[string]interface{}, error) {
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	cluster := viper.GetString("clickhouse.cluster")
	var sb strings.Builder
	sb.WriteString("SELECT ")
	if len(a.Columns) > 0 {
		sb.WriteString(strings.Join(a.Columns, ", "))
	} else {
		sb.WriteString("*")
	}
	sb.WriteString(fmt.Sprintf(" FROM clusterAllReplicas(%s, %s)", cluster, a.Table))
	if a.Where != "" {
		sb.WriteString(" WHERE ")
		sb.WriteString(a.Where)
	}
	if a.OrderBy != "" {
		sb.WriteString(" ORDER BY ")
		sb.WriteString(a.OrderBy)
	}
	if a.Limit > 0 {
		sb.WriteString(fmt.Sprintf(" LIMIT %d", a.Limit))
	}

	ctx := context.Background()
	rows, err := conn.Query(ctx, sb.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := rows.Columns()
	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		ptrs := make([]interface{}, len(cols))
		for i := range cols {
			var s string
			ptrs[i] = &s
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			row[c] = *(ptrs[i].(*string))
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
