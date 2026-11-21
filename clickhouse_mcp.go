package main

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// maxSafeJSONInt = 2^53-1; integers beyond this lose precision in JSON's
// double-precision representation, breaking round-trip filtering on IDs like
// normalized_query_hash.
const maxSafeJSONInt = 1<<53 - 1

// defaultMaxResultRows caps the rows returned by one clickhouse_query call when
// clickhouse.max_result_rows is unset or 0. A negative value disables the cap.
const defaultMaxResultRows = 10000

// JSON-RPC transport types
type queryArgs struct {
	Table   string   `json:"table,omitempty"`
	Columns []string `json:"columns,omitempty"`
	Where   string   `json:"where,omitempty"`
	OrderBy string   `json:"order_by,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	SQL     string   `json:"sql,omitempty"`
}

// (SDK server implemented in sdk_mcp.go)

var structuredTableRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)

func validateQueryArgs(a queryArgs) error {
	// Free-form SQL path
	if strings.TrimSpace(a.SQL) != "" {
		return validateFreeformSQL(a.SQL)
	}

	if a.Table == "" {
		return fmt.Errorf("table is required (or provide 'sql')")
	}
	t := strings.TrimSpace(a.Table)
	if !isTableAllowed(t) {
		allowedDbs := getAllowedDatabases()
		return fmt.Errorf("table must be in allowed databases: %v", allowedDbs)
	}
	if !structuredTableRe.MatchString(t) {
		return fmt.Errorf("invalid table name: must be db.table")
	}
	for _, c := range a.Columns {
		if strings.ContainsAny(c, ";\n\r\t") || strings.TrimSpace(c) == "" {
			return fmt.Errorf("invalid column name: %q", c)
		}
		if err := validateStructuredFragment(c); err != nil {
			return fmt.Errorf("invalid column %q: %v", c, err)
		}
	}
	if strings.Contains(a.Where, ";") || strings.Contains(a.OrderBy, ";") {
		return fmt.Errorf("invalid clause")
	}
	if err := validateStructuredFragment(a.Where); err != nil {
		return fmt.Errorf("invalid where clause: %v", err)
	}
	if err := validateStructuredFragment(a.OrderBy); err != nil {
		return fmt.Errorf("invalid order_by clause: %v", err)
	}
	if a.Limit < 0 {
		return fmt.Errorf("limit must be >= 0")
	}
	// The fragments are concatenated into SQL, so the assembled query must also
	// pass the free-form validator (table refs, comma joins, keywords).
	if err := validateFreeformSQL(buildStructuredQuery(a)); err != nil {
		return fmt.Errorf("invalid structured query: %v", err)
	}
	return nil
}

// structuredFragmentForbidden are words that would let a column/where/order_by
// fragment start a new query branch or a clause the builder doesn't own.
var structuredFragmentForbidden = map[string]bool{
	"union": true, "intersect": true, "limit": true, "offset": true, "where": true, "prewhere": true,
	"having": true, "group": true, "order": true, "qualify": true, "window": true,
}

// validateStructuredFragment rejects a column, where or order_by fragment that
// is not a self-contained expression: unbalanced parentheses (which could close
// the generated clusterAllReplicas()) or clause/set-operation keywords outside a
// subquery.
func validateStructuredFragment(frag string) error {
	if strings.TrimSpace(frag) == "" {
		return nil
	}
	sanitized, err := sanitizeSQL(frag)
	if err != nil {
		return err
	}
	toks := lexSQL(strings.ToLower(sanitized))
	match, err := matchTokenParens(toks)
	if err != nil {
		return err
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i].text
		if t == "(" {
			if i+1 < len(toks) && isQueryStart(toks[i+1].text) {
				// Subqueries are validated with the assembled query.
				i = match[i]
			}
			continue
		}
		if structuredFragmentForbidden[t] && (i == 0 || toks[i-1].text != ".") {
			return fmt.Errorf("keyword %q is not allowed here", t)
		}
	}
	return nil
}

// buildStructuredQuery assembles the SQL for the structured (table/columns/
// where/order_by/limit) form of clickhouse_query.
func buildStructuredQuery(a queryArgs) string {
	var sb strings.Builder
	sb.WriteString("SELECT ")
	if len(a.Columns) > 0 {
		sb.WriteString(strings.Join(a.Columns, ", "))
	} else {
		sb.WriteString("*")
	}

	table := strings.TrimSpace(a.Table)
	// Only use clusterAllReplicas for system tables
	if strings.HasPrefix(strings.ToLower(table), "system.") {
		cluster := viper.GetString("clickhouse.cluster")
		fmt.Fprintf(&sb, " FROM clusterAllReplicas(%s, %s)", cluster, table)
	} else {
		fmt.Fprintf(&sb, " FROM %s", table)
	}

	if a.Where != "" {
		sb.WriteString(" WHERE ")
		sb.WriteString(a.Where)
	}
	if a.OrderBy != "" {
		sb.WriteString(" ORDER BY ")
		sb.WriteString(a.OrderBy)
	}
	if a.Limit > 0 {
		fmt.Fprintf(&sb, " LIMIT %d", a.Limit)
	}
	return sb.String()
}

// maxResultRows returns the row cap for one query; <= 0 means no cap.
func maxResultRows() int {
	n := viper.GetInt("clickhouse.max_result_rows")
	if n == 0 {
		return defaultMaxResultRows
	}
	return n
}

func runClickhouseQuery(a queryArgs) ([]map[string]interface{}, error) {
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := conn.Close(); err != nil {
			logrus.WithError(err).Warn("Error closing ClickHouse connection")
		}
	}()

	query := a.SQL
	if strings.TrimSpace(a.SQL) == "" {
		query = buildStructuredQuery(a)
	}

	results, truncated, err := queryRowsCapped(context.Background(), conn, query, maxResultRows())
	if err != nil {
		return nil, err
	}
	if truncated {
		logrus.WithField("max_rows", len(results)).Warn("clickhouse_query result truncated at max_result_rows")
	}
	return results, nil
}

// queryRowsCapped runs query and returns at most maxRows rows (maxRows <= 0 means
// unlimited). truncated reports whether more rows were available; in that case
// the query is cancelled so the server stops streaming the rest.
func queryRowsCapped(parent context.Context, conn driver.Conn, query string, maxRows int) (results []map[string]interface{}, truncated bool, err error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	rows, err := conn.Query(ctx, query)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if truncated {
			// Stop the server-side query before Close drains the stream.
			cancel()
		}
		if cerr := rows.Close(); cerr != nil && !truncated {
			logrus.WithError(cerr).Warn("Error closing rows")
		}
	}()

	results, truncated, err = collectRows(rows, maxRows)
	return results, truncated, err
}

// collectRows scans rows into JSON-friendly maps, stopping after maxRows rows
// (maxRows <= 0 means unlimited).
func collectRows(rows driver.Rows, maxRows int) ([]map[string]interface{}, bool, error) {
	cols := rows.Columns()
	colTypes := rows.ColumnTypes()
	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		if maxRows > 0 && len(results) >= maxRows {
			return results, true, nil
		}
		ptrs := make([]interface{}, len(cols))
		holders := make([]reflect.Value, len(cols))
		for i := range cols {
			st := colTypes[i].ScanType()
			if st == nil { // fallback to string
				st = reflect.TypeOf("")
			}
			dest := reflect.New(st) // *T; for Nullable columns T is itself a pointer
			holders[i] = dest
			ptrs[i] = dest.Interface()
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, false, err
		}
		row := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			// normalizeValue dereferences pointers, so Nullable(T) and
			// LowCardinality(Nullable(T)) both come out as T or nil.
			row[c] = normalizeValue(holders[i].Elem().Interface())
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return results, false, nil
}

// normalizeValue converts scanned values into JSON-friendly representations
// while preserving useful numeric types. Unknown types fall back to fmt.Sprint.
func normalizeValue(v interface{}) interface{} {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return t
	case bool:
		return t
	case int, int8, int16, int32, int64:
		i := reflect.ValueOf(t).Int()
		if i > maxSafeJSONInt || i < -maxSafeJSONInt {
			return strconv.FormatInt(i, 10)
		}
		return i
	case uint, uint8, uint16, uint32, uint64:
		u := reflect.ValueOf(t).Uint()
		if u > maxSafeJSONInt {
			return strconv.FormatUint(u, 10)
		}
		return u
	case float32, float64:
		f := reflect.ValueOf(t).Float()
		switch {
		case math.IsNaN(f):
			return "NaN"
		case math.IsInf(f, 1):
			return "+Inf"
		case math.IsInf(f, -1):
			return "-Inf"
		}
		return f
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	}
	rv := reflect.ValueOf(v)
	// Nil pointers (Nullable) become JSON null; checked before Stringer so a nil
	// *big.Int doesn't render as "<nil>".
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil
	}
	// Stringers before the slice/array branch: uuid.UUID is [16]byte and net.IP
	// is []byte; decimal.Decimal and *big.Int also land here. A plain []byte is
	// not special-cased: the driver scans String/FixedString to string, so a
	// []uint8 is an Array(UInt8) and renders as numbers.
	if s, ok := v.(fmt.Stringer); ok {
		return s.String()
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return normalizeValue(rv.Elem().Interface())
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return []interface{}{}
		}
		n := rv.Len()
		out := make([]interface{}, n)
		for i := 0; i < n; i++ {
			out[i] = normalizeValue(rv.Index(i).Interface())
		}
		return out
	case reflect.Map:
		out := make(map[string]interface{}, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			var key string
			if iter.Key().Kind() == reflect.String {
				key = iter.Key().String()
			} else {
				key = fmt.Sprint(normalizeValue(iter.Key().Interface()))
			}
			out[key] = normalizeValue(iter.Value().Interface())
		}
		return out
	}
	return fmt.Sprint(v)
}

// getAllowedDatabases returns the list of databases the MCP server can query
func getAllowedDatabases() []string {
	allowed := viper.GetStringSlice("clickhouse.allowed_databases")
	if len(allowed) == 0 {
		// Default to system if nothing configured
		return []string{"system"}
	}
	return allowed
}

// isTableAllowed checks if a table reference is in the allowed databases
func isTableAllowed(table string) bool {
	allowedDbs := getAllowedDatabases()
	for _, db := range allowedDbs {
		if strings.HasPrefix(strings.ToLower(table), strings.ToLower(db)+".") {
			return true
		}
	}
	return false
}

// forbiddenKeywords may not appear as words anywhere in free-form SQL (outside
// string literals, and not as a qualified name like t.delete). "format" is
// handled separately so the format() function stays usable.
var forbiddenKeywords = map[string]bool{
	"insert": true, "alter": true, "update": true, "delete": true,
	"attach": true, "detach": true, "drop": true, "create": true,
	"truncate": true, "kill": true, "optimize": true, "grant": true,
	"revoke": true, "set": true, "use": true, "settings": true,
	"into": true, "outfile": true,
}

// forbiddenFunctions are scalar functions that read files or tables outside
// the FROM/JOIN/IN positions the validator checks.
var forbiddenFunctions = map[string]bool{
	"file": true, "joinget": true, "joingetornull": true,
}

// inOperators are the IN-family operators and functions whose right-hand side
// may be a bare table name (x IN db.t, x IN (db.t), in(x, db.t)).
var inOperators = map[string]bool{
	"in": true, "notin": true, "globalin": true, "globalnotin": true,
	"nullin": true, "notnullin": true, "globalnullin": true, "globalnotnullin": true,
}

// exprFromFunctions take a FROM inside their argument list that is not a table
// reference: EXTRACT(DAY FROM t), trim(BOTH ' ' FROM s), substring(s FROM 1).
var exprFromFunctions = map[string]bool{
	"extract": true, "trim": true, "substring": true, "substr": true,
}

// joinClauseEnd are words that end a FROM/JOIN table expression. A comma seen
// before one of these (at the same paren depth) is a comma join.
var joinClauseEnd = map[string]bool{
	"where": true, "prewhere": true, "group": true, "order": true, "limit": true,
	"having": true, "window": true, "qualify": true, "union": true,
	"intersect": true, "except": true, "settings": true, "format": true,
	"into": true, "join": true, "left": true, "right": true, "inner": true,
	"outer": true, "full": true, "cross": true, "any": true, "all": true,
	"semi": true, "anti": true, "asof": true, "global": true, "paste": true,
	"array": true, "on": true, "using": true,
}

// validateFreeformSQL ensures the provided SQL is a single SELECT/WITH query that
// reads only allowed database tables. It lexes a sanitized copy of the query
// (string literals blanked, whitespace folded) and checks every FROM/JOIN table
// expression and IN right-hand side. Accepted table expressions:
//
//   - db.table in allowed_databases
//   - clusterAllReplicas(cluster, db.table) with db.table allowed
//   - a CTE name declared by an enclosing or preceding WITH
//   - (SELECT ...) / (WITH ...) subqueries, validated recursively
//
// Comments, backtick identifiers, heredoc ($$) literals, control characters,
// comma joins, other table functions, SETTINGS/FORMAT/INTO OUTFILE and
// write/DDL keywords are rejected.
func validateFreeformSQL(sql string) error {
	s := strings.TrimSpace(sql)
	if s == "" {
		return fmt.Errorf("sql is empty")
	}
	sanitized, err := sanitizeSQL(s)
	if err != nil {
		return err
	}
	if strings.Contains(sanitized, ";") {
		return fmt.Errorf("multiple statements are not allowed")
	}
	toks := lexSQL(strings.ToLower(sanitized))
	if len(toks) == 0 || !isQueryStart(toks[0].text) {
		return fmt.Errorf("only SELECT/WITH queries are allowed")
	}
	for i, t := range toks {
		if i > 0 && toks[i-1].text == "." {
			continue
		}
		if forbiddenKeywords[t.text] {
			return fmt.Errorf("forbidden keyword detected: %s", t.text)
		}
		if t.text == "format" && (i+1 >= len(toks) || toks[i+1].text != "(") {
			return fmt.Errorf("forbidden keyword detected: format")
		}
		if forbiddenFunctions[t.text] && i+1 < len(toks) && toks[i+1].text == "(" {
			return fmt.Errorf("function %s() is not allowed", t.text)
		}
	}
	match, err := matchTokenParens(toks)
	if err != nil {
		return err
	}
	v := &sqlRefValidator{toks: toks, match: match}
	return v.scope(0, len(toks), map[string]struct{}{})
}

// sanitizeSQL returns a copy of s that is safe to lex with a simple scanner:
// string literals are blanked, each double-quoted identifier becomes a single
// quotedIdentToken, and all Unicode whitespace is folded to ' '. It rejects input the scanner can't follow the
// way ClickHouse does: control characters, comments, backtick identifiers and
// heredoc literals (a quote inside any of those would desync literal stripping).
func sanitizeSQL(s string) (string, error) {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b < 0x20 && b != '\t' && b != '\n' && b != '\r') || b == 0x7f {
			return "", fmt.Errorf("control characters are not allowed in sql")
		}
	}
	stripped := maskQuoted(s, true)
	switch {
	case strings.Contains(stripped, "--"), strings.Contains(stripped, "/*"), strings.Contains(stripped, "#"):
		return "", fmt.Errorf("comments are not allowed in sql")
	case strings.Contains(stripped, "`"):
		return "", fmt.Errorf("backtick-quoted identifiers are not allowed in sql")
	case strings.Contains(stripped, "$"):
		return "", fmt.Errorf("heredoc ($) literals are not allowed in sql")
	}
	return normalizeWhitespace(stripped), nil
}

type sqlToken struct {
	text string // lowercased
	pos  int
}

// lexSQL splits sanitized, lowercased SQL into words ([a-z0-9_]+) and
// single-character punctuation tokens. Whitespace separates tokens.
func lexSQL(lower string) []sqlToken {
	var toks []sqlToken
	for i := 0; i < len(lower); {
		b := lower[i]
		switch {
		case b == ' ':
			i++
		case isWordByte(b):
			start := i
			for i < len(lower) && isWordByte(lower[i]) {
				i++
			}
			toks = append(toks, sqlToken{text: lower[start:i], pos: start})
		default:
			toks = append(toks, sqlToken{text: lower[i : i+1], pos: i})
			i++
		}
	}
	return toks
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// isIdent reports whether a word token can be an identifier (not a number).
func isIdent(w string) bool {
	return w != "" && (w[0] < '0' || w[0] > '9') && isWordByte(w[0])
}

func isQueryStart(w string) bool {
	return w == "select" || w == "with"
}

// matchTokenParens maps each '(' token index to its ')' and vice versa.
func matchTokenParens(toks []sqlToken) ([]int, error) {
	match := make([]int, len(toks))
	var stack []int
	for i, t := range toks {
		switch t.text {
		case "(":
			stack = append(stack, i)
		case ")":
			if len(stack) == 0 {
				return nil, fmt.Errorf("unbalanced parentheses")
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			match[open], match[i] = i, open
		}
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("unbalanced parentheses")
	}
	return match, nil
}

type sqlRefValidator struct {
	toks  []sqlToken
	match []int
}

// scope validates the query in toks[start:end]. CTE names are visible from the
// point they are declared onward and in nested subqueries.
func (v *sqlRefValidator) scope(start, end int, inherited map[string]struct{}) error {
	ctes := make(map[string]struct{}, len(inherited))
	for k := range inherited {
		ctes[k] = struct{}{}
	}
	var parens []int // open expression parens within this scope
	for i := start; i < end; i++ {
		t := v.toks[i].text
		qualified := i > start && v.toks[i-1].text == "."
		switch {
		case t == "(":
			if i+1 < end && isQueryStart(v.toks[i+1].text) {
				if err := v.scope(i+1, v.match[i], ctes); err != nil {
					return err
				}
				i = v.match[i]
				continue
			}
			parens = append(parens, i)
		case t == ")":
			if len(parens) > 0 {
				parens = parens[:len(parens)-1]
			}
		case t == "with" && (i == start || isSetOpWord(v.toks[i-1].text)):
			v.collectCTEs(i+1, end, ctes)
		case t == "from" && !qualified:
			if len(parens) > 0 {
				p := parens[len(parens)-1]
				if p > 0 && exprFromFunctions[v.toks[p-1].text] {
					continue
				}
			}
			if err := v.tableExpr(i+1, end, ctes); err != nil {
				return err
			}
		case t == "join" && !qualified:
			if i > start && v.toks[i-1].text == "array" {
				continue // ARRAY JOIN takes array expressions, not tables
			}
			if err := v.tableExpr(i+1, end, ctes); err != nil {
				return err
			}
		case inOperators[t] && !qualified:
			if err := v.inRHS(i+1, end, ctes); err != nil {
				return err
			}
		}
	}
	return nil
}

func isSetOpWord(w string) bool {
	return w == "union" || w == "all" || w == "distinct" || w == "intersect" || w == "except"
}

// collectCTEs records the names bound by a WITH clause starting at toks[i]:
// "name AS (SELECT|WITH ...)" items, comma-separated, up to the SELECT.
// Scalar items ("expr AS alias") are skipped.
func (v *sqlRefValidator) collectCTEs(i, end int, ctes map[string]struct{}) {
	for i < end {
		if i+3 < end && isIdent(v.toks[i].text) && v.toks[i+1].text == "as" &&
			v.toks[i+2].text == "(" && isQueryStart(v.toks[i+3].text) {
			ctes[v.toks[i].text] = struct{}{}
		}
		for i < end {
			t := v.toks[i].text
			if t == "(" {
				i = v.match[i] + 1
				continue
			}
			if t == "select" {
				return
			}
			i++
			if t == "," {
				break
			}
		}
	}
}

// parseRef reads "ident" or "ident.ident" at toks[k] and returns the name and
// the index after it; ok is false if toks[k] is not an identifier.
func (v *sqlRefValidator) parseRef(k, end int) (name string, dotted bool, next int, ok bool) {
	if k >= end || !isIdent(v.toks[k].text) {
		return "", false, k, false
	}
	if k+2 < end && v.toks[k+1].text == "." && isIdent(v.toks[k+2].text) {
		return v.toks[k].text + "." + v.toks[k+2].text, true, k + 3, true
	}
	return v.toks[k].text, false, k + 1, true
}

func (v *sqlRefValidator) refAllowed(name string, dotted bool, ctes map[string]struct{}) bool {
	if dotted {
		return isTableAllowed(name)
	}
	_, ok := ctes[name]
	return ok
}

// tableExpr validates the table expression after FROM/JOIN at toks[k].
func (v *sqlRefValidator) tableExpr(k, end int, ctes map[string]struct{}) error {
	if k >= end {
		return fmt.Errorf("missing table after FROM/JOIN")
	}
	var after int
	if v.toks[k].text == "(" {
		if k+1 >= end || !isQueryStart(v.toks[k+1].text) {
			return fmt.Errorf("subquery in FROM/JOIN must be a SELECT or WITH")
		}
		after = v.match[k] + 1
	} else {
		if v.toks[k].text == quotedIdentToken {
			return fmt.Errorf("double-quoted table names are not supported in FROM/JOIN; use db.table")
		}
		name, dotted, next, ok := v.parseRef(k, end)
		if !ok {
			return fmt.Errorf("only tables from allowed databases %v are allowed (found: %s)", getAllowedDatabases(), v.toks[k].text)
		}
		switch {
		case next < end && v.toks[next].text == "(":
			if dotted || name != "clusterallreplicas" {
				return fmt.Errorf("table functions other than clusterAllReplicas are not allowed (found: %s)", name)
			}
			if err := v.clusterAllReplicasArgs(next); err != nil {
				return err
			}
			after = v.match[next] + 1
		case next < end && v.toks[next].text == ".":
			return fmt.Errorf("only db.table references are allowed (found: %s.)", name)
		case !v.refAllowed(name, dotted, ctes):
			return fmt.Errorf("only tables from allowed databases %v are allowed (found: %s)", getAllowedDatabases(), name)
		default:
			after = next
		}
	}
	return v.noCommaJoin(after, end)
}

// clusterAllReplicasArgs validates clusterAllReplicas(cluster, db.table) whose
// '(' is at toks[open]. The cluster is an identifier or a (blanked) string.
func (v *sqlRefValidator) clusterAllReplicasArgs(open int) error {
	args := v.splitArgs(open+1, v.match[open])
	errTarget := fmt.Errorf("clusterAllReplicas must target tables from allowed databases: %v", getAllowedDatabases())
	if len(args) != 2 {
		return errTarget
	}
	if c := args[0]; len(c) > 1 || (len(c) == 1 && !isIdent(c[0].text)) {
		return fmt.Errorf("clusterAllReplicas cluster must be a name or string literal")
	}
	t := args[1]
	if len(t) != 3 || !isIdent(t[0].text) || t[1].text != "." || !isIdent(t[2].text) {
		return errTarget
	}
	if !isTableAllowed(t[0].text + "." + t[2].text) {
		return errTarget
	}
	return nil
}

// splitArgs splits toks[start:end] at depth-0 commas.
func (v *sqlRefValidator) splitArgs(start, end int) [][]sqlToken {
	var args [][]sqlToken
	cur := []sqlToken{}
	for i := start; i < end; i++ {
		t := v.toks[i]
		if t.text == "(" {
			cur = append(cur, v.toks[i:v.match[i]+1]...)
			i = v.match[i]
			continue
		}
		if t.text == "," {
			args = append(args, cur)
			cur = []sqlToken{}
			continue
		}
		cur = append(cur, t)
	}
	return append(args, cur)
}

// noCommaJoin rejects "FROM a, b": a comma after the table expression before
// the next clause keyword at the same paren depth.
func (v *sqlRefValidator) noCommaJoin(k, end int) error {
	for i := k; i < end; i++ {
		t := v.toks[i].text
		switch {
		case t == "(":
			i = v.match[i]
		case t == ")":
			return nil
		case t == ",":
			return fmt.Errorf("comma joins are not allowed in FROM; use JOIN")
		case joinClauseEnd[t] && (i == 0 || v.toks[i-1].text != "."):
			return nil
		}
	}
	return nil
}

// inRHS validates the right-hand side of an IN-family operator at toks[k]: a
// bare table name there (x IN db.t, x IN (db.t), in(x, db.t)) reads that table.
func (v *sqlRefValidator) inRHS(k, end int, ctes map[string]struct{}) error {
	if k >= end {
		return nil
	}
	if v.toks[k].text == "(" {
		if k+1 < end && isQueryStart(v.toks[k+1].text) {
			return nil // subquery: validated as its own scope
		}
		for _, arg := range v.splitArgs(k+1, v.match[k]) {
			if err := v.inBareRef(arg, ctes); err != nil {
				return err
			}
		}
		return nil
	}
	if v.toks[k].text == quotedIdentToken {
		return fmt.Errorf("IN with a double-quoted table reference is not allowed")
	}
	name, dotted, next, ok := v.parseRef(k, end)
	if !ok || (next < end && v.toks[next].text == "(") {
		return nil // literal or function call (e.g. tuple(...))
	}
	if !v.refAllowed(name, dotted, ctes) {
		return fmt.Errorf("IN with a table reference must use an allowed database table (found: %s)", name)
	}
	return nil
}

func (v *sqlRefValidator) inBareRef(arg []sqlToken, ctes map[string]struct{}) error {
	// A ref made of identifiers, dots and at least one double-quoted identifier.
	quoted, onlyRef := false, len(arg) > 0 && len(arg) <= 3
	for _, t := range arg {
		quoted = quoted || t.text == quotedIdentToken
		onlyRef = onlyRef && (t.text == quotedIdentToken || t.text == "." || isIdent(t.text))
	}
	if quoted && onlyRef {
		return fmt.Errorf("IN with a double-quoted table reference is not allowed")
	}
	var name string
	var dotted bool
	switch {
	case len(arg) == 1 && isIdent(arg[0].text):
		name = arg[0].text
		if name == "null" || name == "true" || name == "false" {
			return nil
		}
	case len(arg) == 3 && isIdent(arg[0].text) && arg[1].text == "." && isIdent(arg[2].text):
		name, dotted = arg[0].text+"."+arg[2].text, true
	default:
		return nil
	}
	if !v.refAllowed(name, dotted, ctes) {
		return fmt.Errorf("IN with a table reference must use an allowed database table (found: %s)", name)
	}
	return nil
}

// normalizeWhitespace folds all Unicode whitespace to single spaces. Only used
// on the sanitized scan copy — the original SQL is what gets sent to ClickHouse.
func normalizeWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
}

// quotedIdentToken replaces a double-quoted identifier in the sanitized copy.
// It is a control byte, which sanitizeSQL rejects in user input, so it can't
// collide with a real identifier (e.g. a CTE name).
const quotedIdentToken = "\x01"

// stripQuotedLiterals blanks single- and double-quoted sections with spaces.
func stripQuotedLiterals(s string) string {
	return maskQuoted(s, false)
}

// maskQuoted blanks quoted sections with spaces. With markIdents, a
// double-quoted identifier is instead replaced by " " + quotedIdentToken + " "
// so the scanner still sees an identifier there (x IN "db"."t" reads a table).
func maskQuoted(s string, markIdents bool) string {
	var b strings.Builder
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inSingle {
			// ClickHouse escapes quotes inside string literals with a backslash
			// ('it\'s'); consume the escaped character so it can't terminate the
			// literal and desync the scan.
			if ch == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(' ')
				b.WriteByte(' ')
				continue
			}
			if ch == '\'' {
				inSingle = false
			}
			b.WriteByte(' ')
			continue
		}
		if inDouble {
			if ch == '\\' && i+1 < len(s) {
				i++
				if !markIdents {
					b.WriteString("  ")
				}
				continue
			}
			if ch == '"' {
				inDouble = false
				if markIdents {
					b.WriteString(" " + quotedIdentToken + " ")
					continue
				}
			}
			if !markIdents {
				b.WriteByte(' ')
			}
			continue
		}
		if ch == '\'' {
			inSingle = true
			b.WriteByte(' ')
			continue
		}
		if ch == '"' {
			inDouble = true
			if !markIdents {
				b.WriteByte(' ')
			}
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}
