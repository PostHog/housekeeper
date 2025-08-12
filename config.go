package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// loadConfig loads configuration from an explicit path if provided, otherwise
// searches several conventional locations to work when launched by external hosts (e.g., MCP clients).
func loadConfig(explicitPath string) error {
	if explicitPath == "" {
		if env := os.Getenv("HOUSEKEEPER_CONFIG"); env != "" {
			explicitPath = env
		}
	}

	if explicitPath != "" {
		viper.SetConfigFile(explicitPath)
		return viper.ReadInConfig()
	}

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")

	// Relative to working dir
	viper.AddConfigPath(".")
	viper.AddConfigPath("configs")

	// Relative to executable dir
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		viper.AddConfigPath(dir)
		viper.AddConfigPath(filepath.Join(dir, "configs"))
	}

	// XDG/home
	if home, err := os.UserHomeDir(); err == nil {
		viper.AddConfigPath(filepath.Join(home, ".config", "housekeeper"))
	}

	// System path
	viper.AddConfigPath("/etc/housekeeper")

	return viper.ReadInConfig()
}
