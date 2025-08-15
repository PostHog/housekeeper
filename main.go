package main

import (
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func main() {
	// Define all flags using pflag
	analyzeMode := pflag.Bool("analyze", false, "Run in analysis mode (error/performance analysis with Gemini AI) instead of MCP server")
	performanceMode := pflag.Bool("performance", false, "Run query performance analysis (requires --analyze)")
	slackBotMode := pflag.Bool("slack-bot", false, "Run as an interactive Slack bot that queries the MCP server")
	configPath := pflag.String("config", "", "Path to YAML config (or set HOUSEKEEPER_CONFIG)")
	
	// ClickHouse flags
	pflag.String("ch-host", "127.0.0.1", "ClickHouse host")
	pflag.Int("ch-port", 9000, "ClickHouse port")
	pflag.String("ch-user", "default", "ClickHouse user")
	pflag.String("ch-password", "", "ClickHouse password")
	pflag.String("ch-database", "default", "ClickHouse database")
	pflag.String("ch-cluster", "default", "ClickHouse cluster name")
	pflag.StringSlice("ch-allowed-databases", []string{"system"}, "Comma-separated list of databases the MCP server can query")
	
	// Prometheus/Victoria Metrics flags
	pflag.String("prom-host", "localhost", "Prometheus/Victoria Metrics host")
	pflag.Int("prom-port", 8481, "Prometheus/Victoria Metrics port")
	pflag.Bool("prom-vm-cluster", false, "Enable Victoria Metrics cluster mode")
	pflag.String("prom-vm-tenant", "0", "Victoria Metrics tenant ID")
	pflag.String("prom-vm-prefix", "", "Victoria Metrics path prefix")
	
	// Parse all flags
	pflag.Parse()
	
	// Bind pflags to viper
	_ = viper.BindPFlag("clickhouse.host", pflag.Lookup("ch-host"))
	_ = viper.BindPFlag("clickhouse.port", pflag.Lookup("ch-port"))
	_ = viper.BindPFlag("clickhouse.user", pflag.Lookup("ch-user"))
	_ = viper.BindPFlag("clickhouse.password", pflag.Lookup("ch-password"))
	_ = viper.BindPFlag("clickhouse.database", pflag.Lookup("ch-database"))
	_ = viper.BindPFlag("clickhouse.cluster", pflag.Lookup("ch-cluster"))
	_ = viper.BindPFlag("clickhouse.allowed_databases", pflag.Lookup("ch-allowed-databases"))
	
	_ = viper.BindPFlag("prometheus.host", pflag.Lookup("prom-host"))
	_ = viper.BindPFlag("prometheus.port", pflag.Lookup("prom-port"))
	_ = viper.BindPFlag("prometheus.vm_cluster_mode", pflag.Lookup("prom-vm-cluster"))
	_ = viper.BindPFlag("prometheus.vm_tenant_id", pflag.Lookup("prom-vm-tenant"))
	_ = viper.BindPFlag("prometheus.vm_path_prefix", pflag.Lookup("prom-vm-prefix"))

	// Handle Slack bot mode
	if *slackBotMode {
		if err := loadConfig(*configPath); err != nil {
			logrus.WithError(err).Fatal("Failed to load config for Slack bot")
		}
		
		logrus.Info("Starting Slack bot with MCP integration")
		bot, err := NewSlackBot()
		if err != nil {
			logrus.WithError(err).Fatal("Failed to create Slack bot")
		}
		defer bot.Close()
		
		if err := bot.Run(); err != nil {
			logrus.WithError(err).Fatal("Failed to run Slack bot")
		}
		return
	}

	// Default to MCP mode unless analysis mode is explicitly requested
	if !*analyzeMode {
		// Try to load config file if provided, but don't fail if it doesn't exist
		// Command-line flags will provide the values
		if err := loadConfig(*configPath); err != nil {
			// Only log debug message if config file wasn't found
			logrus.WithError(err).Debug("Config file not found, using command-line flags")

		}
		// Do not print to stdout in MCP mode; stdout is reserved for JSON-RPC
		logrus.Info("Starting MCP server")
		if err := RunMCPServer(); err != nil {
			logrus.WithError(err).Fatal("Failed to run MCP server")
		}
		return
	}

	if err := loadConfig(*configPath); err != nil {
		logrus.WithError(err).Fatal("Failed to load config")
	}

	logrus.Info("Running in analysis mode (AI-powered ClickHouse monitoring)")
	apiKey := viper.GetString("gemini_key")
	if apiKey == "" {
		logrus.Fatal("Please set gemini_key in configs")
	}
	logrus.Debug("Gemini API key loaded")

	if *performanceMode {
		logrus.Info("Analyzing query performance...")
		summary := AnalyzeQueryPerformanceWithAgent()
		logrus.Info("Performance analysis complete")
		fmt.Println(summary)
		return
	}

	logrus.Info("Starting ClickHouse error analysis")
	errors, err := CHErrorAnalysis()
	if err != nil {
		logrus.WithError(err).Fatal("Failed to analyze ClickHouse errors")
	}

	if len(errors) > 0 {
		logrus.WithField("error_count", len(errors)).Info("Errors found, analyzing with Gemini")
		summary := AnalyzeErrorsWithAgent(errors)
		fmt.Println(summary)

		if err := SendSlackMessage(summary, len(errors)); err != nil {
			logrus.WithError(err).Error("Failed to send Slack message")
		} else {
			logrus.Info("Slack notification sent successfully")
		}
	} else {
		logrus.Info("No errors found in the last hour")
	}
}
