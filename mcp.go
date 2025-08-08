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
    SQL     string   `json:"sql,omitempty"`
}

// (SDK server implemented in sdk_mcp.go)

func validateQueryArgs(a queryArgs) error {
    // Free-form SQL path
    if strings.TrimSpace(a.SQL) != "" {
        return validateFreeformSQL(a.SQL)
    }

    if a.Table == "" {
        return fmt.Errorf("table is required (or provide 'sql')")
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

    var query string
    if strings.TrimSpace(a.SQL) != "" {
        query = a.SQL
    } else {
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
        query = sb.String()
    }

    ctx := context.Background()
    rows, err := conn.Query(ctx, query)
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

// validateFreeformSQL ensures the provided SQL is a single SELECT/WITH query and
// references only system.* tables (including inside clusterAllReplicas()).
func validateFreeformSQL(sql string) error {
    s := strings.TrimSpace(sql)
    if s == "" {
        return fmt.Errorf("sql is empty")
    }
    if strings.Contains(s, ";") {
        return fmt.Errorf("multiple statements are not allowed")
    }
    // Strip simple quoted strings to avoid false positives when scanning tokens
    sanitized := stripQuotedLiterals(s)
    lower := strings.ToLower(strings.TrimSpace(sanitized))
    if !(strings.HasPrefix(lower, "select ") || strings.HasPrefix(lower, "with ")) {
        return fmt.Errorf("only SELECT/WITH queries are allowed")
    }
    // Disallow obvious write/DDL keywords
    forbidden := []string{" insert ", " alter ", " update ", " delete ", " attach ", " detach ", " drop ", " create ", " truncate ", " kill ", " optimize ", " grant ", " revoke ", " set ", " use "}
    lpad := " " + lower + " "
    for _, kw := range forbidden {
        if strings.Contains(lpad, kw) {
            return fmt.Errorf("forbidden keyword detected: %s", strings.TrimSpace(kw))
        }
    }
    // Validate FROM/JOIN targets
    tokens := []string{" from ", " join "}
    for _, tok := range tokens {
        idx := 0
        for {
            pos := strings.Index(strings.ToLower(sanitized[idx:]), strings.TrimSpace(tok))
            if pos < 0 {
                break
            }
            // Move to start of table expression
            start := idx + pos + len(strings.TrimSpace(tok))
            // Skip spaces
            for start < len(sanitized) && sanitized[start] == ' ' {
                start++
            }
            // Capture up to first space, comma, newline, or parenthesis
            end := start
            for end < len(sanitized) && !strings.ContainsRune(" \n\t,)", rune(sanitized[end])) {
                end++
            }
            ref := strings.TrimSpace(sanitized[start:end])
            // Accept clusterAllReplicas(cluster, system.table)
            if strings.HasPrefix(strings.ToLower(ref), "clusterallreplicas(") {
                // try to extract 2nd arg
                // naive parse: find first '(' and last ')' in this token
                open := strings.Index(ref, "(")
                close := strings.LastIndex(ref, ")")
                if open > 0 && close > open {
                    inner := ref[open+1 : close]
                    parts := strings.SplitN(inner, ",", 2)
                    if len(parts) == 2 {
                        tbl := strings.TrimSpace(parts[1])
                        if !strings.HasPrefix(strings.ToLower(tbl), "system.") {
                            return fmt.Errorf("clusterAllReplicas must target system.* tables")
                        }
                    }
                }
            } else {
                // Raw table reference must be system.*
                if !strings.HasPrefix(strings.ToLower(ref), "system.") {
                    return fmt.Errorf("only system.* tables are allowed (found: %s)", ref)
                }
            }
            idx = end
        }
    }
    return nil
}

func stripQuotedLiterals(s string) string {
    var b strings.Builder
    inSingle, inDouble := false, false
    for i := 0; i < len(s); i++ {
        ch := s[i]
        if inSingle {
            if ch == '\'' {
                inSingle = false
            }
            b.WriteByte(' ')
            continue
        }
        if inDouble {
            if ch == '"' {
                inDouble = false
            }
            b.WriteByte(' ')
            continue
        }
        if ch == '\'' {
            inSingle = true
            b.WriteByte(' ')
            continue
        }
        if ch == '"' {
            inDouble = true
            b.WriteByte(' ')
            continue
        }
        b.WriteByte(ch)
    }
    return b.String()
}
