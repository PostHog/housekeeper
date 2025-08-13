package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// loadConfig loads configuration from an explicit path if provided, otherwise
// searches several conventional locations to work when launched by external hosts (e.g., MCP clients).
// Note: Command-line flags bound to viper will override config file values
func loadConfig(explicitPath string) error {
	// Set defaults for all configuration values
	// These can be overridden by config file or command-line flags
	viper.SetDefault("clickhouse.host", "127.0.0.1")
	viper.SetDefault("clickhouse.port", 9000)
	viper.SetDefault("clickhouse.user", "default")
	viper.SetDefault("clickhouse.password", "")
	viper.SetDefault("clickhouse.database", "default")
	viper.SetDefault("clickhouse.cluster", "default")
	
	viper.SetDefault("prometheus.host", "localhost")
	viper.SetDefault("prometheus.port", 8481)
	viper.SetDefault("prometheus.vm_cluster_mode", false)
	viper.SetDefault("prometheus.vm_tenant_id", "0")
	viper.SetDefault("prometheus.vm_path_prefix", "")
	
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.format", "text")

	if explicitPath == "" {
		if env := os.Getenv("HOUSEKEEPER_CONFIG"); env != "" {
			explicitPath = env
		}
	}

	if explicitPath != "" {
		viper.SetConfigFile(explicitPath)
		if err := viper.ReadInConfig(); err != nil {
			// Don't fail if config file doesn't exist when flags are provided
			logrus.WithError(err).Debug("Could not read config file, using defaults and flags")
		} else {
			logrus.WithField("config_file", viper.ConfigFileUsed()).Debug("Loaded config file")
		}
	} else {
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

<<<<<<< Updated upstream
	// System path
	viper.AddConfigPath("/etc/housekeeper")

	return viper.ReadInConfig()
=======
		// Try to read config, but don't fail if not found
		if err := viper.ReadInConfig(); err != nil {
			logrus.WithError(err).Debug("No config file found, using defaults and flags")
		} else {
			logrus.WithField("config_file", viper.ConfigFileUsed()).Debug("Loaded config file")
		}
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
>>>>>>> Stashed changes
}
