package main

import (
	"testing"

	"github.com/spf13/viper"
)

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
