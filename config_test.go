package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/viper"
)

func TestAllowedDatabasesFromEnv(t *testing.T) {
	tests := []struct {
		env  string
		want []string
	}{
		{"system,models", []string{"system", "models"}},
		{"system models", []string{"system", "models"}},
		{" system , models ,, ", []string{"system", "models"}},
		{"system", []string{"system"}},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv("HOUSEKEEPER_CLICKHOUSE_ALLOWED_DATABASES", tt.env)
			if err := loadConfig(""); err != nil {
				t.Fatal(err)
			}
			if got := getAllowedDatabases(); !slices.Equal(got, tt.want) {
				t.Errorf("getAllowedDatabases() = %q, want %q", got, tt.want)
			}
		})
	}
	t.Cleanup(func() { viper.Set("clickhouse.allowed_databases", nil) })
}

func TestAllowedDatabasesFromConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("clickhouse:\n  allowed_databases: [system, models]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetConfig)
	if err := loadConfig(path); err != nil {
		t.Fatal(err)
	}
	if got := getAllowedDatabases(); !slices.Equal(got, []string{"system", "models"}) {
		t.Errorf("getAllowedDatabases() = %q, want [system models]", got)
	}
}

// resetConfig clears viper, including a config file path an explicit load
// left behind, and reloads the defaults.
func resetConfig() {
	viper.Reset()
	_ = loadConfig("")
}

func TestLoadConfigExplicitPathErrors(t *testing.T) {
	t.Cleanup(resetConfig)
	dir := t.TempDir()
	if err := loadConfig(filepath.Join(dir, "missing.yml")); err == nil {
		t.Error("a missing explicit config file loaded without error")
	}

	bad := filepath.Join(dir, "bad.yml")
	if err := os.WriteFile(bad, []byte("clickhouse: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadConfig(bad); err == nil {
		t.Error("an unparsable explicit config file loaded without error")
	}

	t.Setenv("HOUSEKEEPER_CONFIG", filepath.Join(dir, "missing-env.yml"))
	if err := loadConfig(""); err == nil {
		t.Error("a missing HOUSEKEEPER_CONFIG file loaded without error")
	}
}
