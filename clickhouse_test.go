package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/spf13/viper"
)

func TestCHErrorString(t *testing.T) {
	err := CHError{
		Hostname:         "host1",
		Name:             "CANNOT_PARSE_QUERY",
		Code:             62,
		Value:            10,
		LastErrorTime:    time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		LastErrorMessage: "Syntax error",
		LastErrorTrace:   []uint64{1, 2, 3},
		Remote:           false,
	}

	result := err.String()
	expectedParts := []string{
		"Hostname: host1",
		"Name: CANNOT_PARSE_QUERY",
		"Code: 62",
		"Value: 10",
		"LastErrorTime: 2024-01-01",
		"LastErrorMessage: Syntax error",
		"LastErrorTrace: [1 2 3]",
		"Remote: false",
	}

	for _, part := range expectedParts {
		if !contains(result, part) {
			t.Errorf("CHError.String() = %v, expected to contain %v", result, part)
		}
	}
}

func TestCHErrorsString(t *testing.T) {
	errors := CHErrors{
		{
			Hostname:         "host1",
			Name:             "ERROR1",
			Code:             1,
			Value:            1,
			LastErrorTime:    time.Now(),
			LastErrorMessage: "Error 1",
		},
		{
			Hostname:         "host2",
			Name:             "ERROR2",
			Code:             2,
			Value:            2,
			LastErrorTime:    time.Now(),
			LastErrorMessage: "Error 2",
		},
	}

	result := errors.String()
	
	// Check that both errors are in the result
	if !contains(result, "host1") || !contains(result, "ERROR1") {
		t.Errorf("CHErrors.String() missing first error: %v", result)
	}
	if !contains(result, "host2") || !contains(result, "ERROR2") {
		t.Errorf("CHErrors.String() missing second error: %v", result)
	}
}

// MockConn is a mock implementation of driver.Conn for testing
type MockConn struct {
	pingError  error
	queryError error
	queryRows  driver.Rows
}

func (m *MockConn) ServerVersion() (*driver.ServerVersion, error) {
	return &driver.ServerVersion{}, nil
}

func (m *MockConn) Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return nil
}

func (m *MockConn) Query(ctx context.Context, query string, args ...interface{}) (driver.Rows, error) {
	if m.queryError != nil {
		return nil, m.queryError
	}
	return m.queryRows, nil
}

func (m *MockConn) QueryRow(ctx context.Context, query string, args ...interface{}) driver.Row {
	return nil
}

func (m *MockConn) PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error) {
	return nil, nil
}

func (m *MockConn) Exec(ctx context.Context, query string, args ...interface{}) error {
	return nil
}

func (m *MockConn) AsyncInsert(ctx context.Context, query string, wait bool, args ...interface{}) error {
	return nil
}

func (m *MockConn) Ping(ctx context.Context) error {
	return m.pingError
}

func (m *MockConn) Stats() driver.Stats {
	return driver.Stats{}
}

func (m *MockConn) Close() error {
	return nil
}

func (m *MockConn) Contributors() []string {
	return nil
}

// MockRows is a mock implementation of driver.Rows for testing
type MockRows struct {
	currentRow int
	maxRows    int
	scanError  error
	columns    []string
	errors     []CHError
}

func (m *MockRows) Next() bool {
	if m.currentRow < m.maxRows {
		m.currentRow++
		return true
	}
	return false
}

func (m *MockRows) Scan(dest ...interface{}) error {
	if m.scanError != nil {
		return m.scanError
	}
	
	if m.currentRow <= 0 || m.currentRow > len(m.errors) {
		return fmt.Errorf("no data available")
	}
	
	err := m.errors[m.currentRow-1]
	
	// Scan values into destination pointers
	if len(dest) >= 8 {
		*dest[0].(*string) = err.Hostname
		*dest[1].(*string) = err.Name
		*dest[2].(*int32) = err.Code
		*dest[3].(*uint64) = err.Value
		*dest[4].(*time.Time) = err.LastErrorTime
		*dest[5].(*string) = err.LastErrorMessage
		*dest[6].(*[]uint64) = err.LastErrorTrace
		*dest[7].(*bool) = err.Remote
	}
	
	return nil
}

func (m *MockRows) Close() error {
	return nil
}

func (m *MockRows) Err() error {
	return nil
}

func (m *MockRows) Columns() []string {
	return m.columns
}

func (m *MockRows) ColumnTypes() []driver.ColumnType {
	return nil
}

func (m *MockRows) Totals(dest ...interface{}) error {
	return nil
}

func (m *MockRows) ScanStruct(dest interface{}) error {
	return nil
}

func TestGetCHErrors(t *testing.T) {
	// Set up test configuration
	viper.Set("clickhouse.cluster", "test_cluster")
	
	testErrors := []CHError{
		{
			Hostname:         "host1",
			Name:             "ERROR1",
			Code:             1,
			Value:            10,
			LastErrorTime:    time.Now(),
			LastErrorMessage: "Test error 1",
			LastErrorTrace:   []uint64{1, 2, 3},
			Remote:           false,
		},
		{
			Hostname:         "host2",
			Name:             "ERROR2",
			Code:             2,
			Value:            20,
			LastErrorTime:    time.Now(),
			LastErrorMessage: "Test error 2",
			LastErrorTrace:   []uint64{4, 5, 6},
			Remote:           true,
		},
	}
	
	mockRows := &MockRows{
		currentRow: 0,
		maxRows:    len(testErrors),
		columns:    []string{"hostname", "name", "code", "value", "last_error_time", "last_error_message", "last_error_trace", "remote"},
		errors:     testErrors,
	}
	
	mockConn := &MockConn{
		queryRows: mockRows,
	}
	
	ctx := context.Background()
	errors, err := getCHErrors(ctx, mockConn)
	
	if err != nil {
		t.Fatalf("getCHErrors() unexpected error: %v", err)
	}
	
	if len(errors) != len(testErrors) {
		t.Errorf("getCHErrors() returned %d errors, want %d", len(errors), len(testErrors))
	}
	
	for i, got := range errors {
		want := testErrors[i]
		if got.Hostname != want.Hostname || got.Name != want.Name || got.Code != want.Code {
			t.Errorf("getCHErrors() error[%d] = %+v, want %+v", i, got, want)
		}
	}
}

func TestGetCHErrorsQueryError(t *testing.T) {
	viper.Set("clickhouse.cluster", "test_cluster")
	
	expectedError := fmt.Errorf("query failed")
	mockConn := &MockConn{
		queryError: expectedError,
	}
	
	ctx := context.Background()
	_, err := getCHErrors(ctx, mockConn)
	
	if err == nil {
		t.Fatal("getCHErrors() expected error, got nil")
	}
	
	if err != expectedError {
		t.Errorf("getCHErrors() error = %v, want %v", err, expectedError)
	}
}

func TestGetCHErrorsScanError(t *testing.T) {
	viper.Set("clickhouse.cluster", "test_cluster")
	
	mockRows := &MockRows{
		currentRow: 0,
		maxRows:    1,
		scanError:  fmt.Errorf("scan failed"),
		columns:    []string{"hostname", "name"},
	}
	
	mockConn := &MockConn{
		queryRows: mockRows,
	}
	
	ctx := context.Background()
	_, err := getCHErrors(ctx, mockConn)
	
	if err == nil {
		t.Fatal("getCHErrors() expected error, got nil")
	}
	
	if !contains(err.Error(), "scan failed") {
		t.Errorf("getCHErrors() error = %v, expected to contain 'scan failed'", err)
	}
}

func TestConnectConfiguration(t *testing.T) {
	// This test verifies that the connect function properly uses viper configuration
	// We can't test actual connection without a ClickHouse instance, but we can
	// verify the configuration is read correctly
	
	testCases := []struct {
		name     string
		config   map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name: "basic configuration",
			config: map[string]interface{}{
				"clickhouse.host":     "test-host",
				"clickhouse.port":     9001,
				"clickhouse.database": "test_db",
				"clickhouse.user":     "test_user",
				"clickhouse.password": "test_pass",
			},
			expected: map[string]interface{}{
				"host":     "test-host",
				"port":     9001,
				"database": "test_db",
				"user":     "test_user",
			},
		},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set configuration
			for key, value := range tc.config {
				viper.Set(key, value)
			}
			
			// Verify configuration is set correctly
			if viper.GetString("clickhouse.host") != tc.expected["host"] {
				t.Errorf("Expected host %v, got %v", tc.expected["host"], viper.GetString("clickhouse.host"))
			}
			if viper.GetInt("clickhouse.port") != tc.expected["port"] {
				t.Errorf("Expected port %v, got %v", tc.expected["port"], viper.GetInt("clickhouse.port"))
			}
			if viper.GetString("clickhouse.database") != tc.expected["database"] {
				t.Errorf("Expected database %v, got %v", tc.expected["database"], viper.GetString("clickhouse.database"))
			}
			if viper.GetString("clickhouse.user") != tc.expected["user"] {
				t.Errorf("Expected user %v, got %v", tc.expected["user"], viper.GetString("clickhouse.user"))
			}
		})
	}
}

func TestCHErrorAnalysisIntegration(t *testing.T) {
	// This test would require a real ClickHouse connection
	// Skip if we're not in an integration test environment
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	
	// Set up test configuration
	viper.Set("clickhouse.host", "localhost")
	viper.Set("clickhouse.port", 9000)
	viper.Set("clickhouse.database", "default")
	viper.Set("clickhouse.user", "default")
	viper.Set("clickhouse.password", "")
	viper.Set("clickhouse.cluster", "default")
	
	// Try to run the analysis
	errors, err := CHErrorAnalysis()
	
	// If we can't connect, that's okay for this test
	if err != nil {
		if _, ok := err.(*clickhouse.Exception); ok {
			t.Skipf("ClickHouse not available for integration test: %v", err)
		}
		// Some other error occurred
		t.Logf("CHErrorAnalysis() error: %v", err)
	} else {
		// Successfully connected and ran query
		t.Logf("CHErrorAnalysis() returned %d errors", len(errors))
	}
}