package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
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

	if err := viper.ReadInConfig(); err != nil {
		return err
	}

	// Configure logging after config is loaded
	configureLogging()
	return nil
}

// configureLogging sets up logrus based on configuration
func configureLogging() {
	// Set log level
	level := viper.GetString("logging.level")
	if level == "" {
		level = "info"
	}
	
	parsedLevel, err := logrus.ParseLevel(strings.ToLower(level))
	if err != nil {
		logrus.WithError(err).Warn("Invalid log level, defaulting to info")
		parsedLevel = logrus.InfoLevel
	}
	logrus.SetLevel(parsedLevel)

	// Set log format
	format := viper.GetString("logging.format")
	if strings.ToLower(format) == "json" {
		logrus.SetFormatter(&logrus.JSONFormatter{})
	} else {
		logrus.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
		})
	}

	logrus.WithFields(logrus.Fields{
		"level":  level,
		"format": format,
	}).Debug("Logging configured")
}
