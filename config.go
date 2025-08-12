package main

import (
    "os"
    "path/filepath"
    "strings"
    "time"

    logrus "github.com/sirupsen/logrus"
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
        if err := viper.ReadInConfig(); err != nil {
            return err
        }
        applyLoggingConfig()
        logrus.WithField("file", viper.ConfigFileUsed()).Info("Loaded config")
        return nil
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
    applyLoggingConfig()
    logrus.WithField("file", viper.ConfigFileUsed()).Info("Loaded config")
    return nil
}

func applyLoggingConfig() {
    // Level
    levelStr := strings.ToLower(strings.TrimSpace(viper.GetString("log.level")))
    if levelStr == "" {
        levelStr = "info"
    }
    if lvl, err := logrus.ParseLevel(levelStr); err == nil {
        logrus.SetLevel(lvl)
    } else {
        logrus.WithError(err).Warn("Invalid log.level; defaulting to info")
        logrus.SetLevel(logrus.InfoLevel)
    }
    // Format
    formatStr := strings.ToLower(strings.TrimSpace(viper.GetString("log.format")))
    switch formatStr {
    case "json":
        logrus.SetFormatter(&logrus.JSONFormatter{TimestampFormat: time.RFC3339Nano})
    default:
        logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
    }
}
