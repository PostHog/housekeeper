package main

import (
	"flag"
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

func main() {
	performanceMode := flag.Bool("performance", false, "Run query performance analysis instead of error analysis")
	mcpMode := flag.Bool("mcp", false, "Run MCP stdio server for ClickHouse system table queries")
	configPath := flag.String("config", "", "Path to YAML config (or set HOUSEKEEPER_CONFIG)")
	flag.Parse()

	if *mcpMode {
		if err := loadConfig(*configPath); err != nil {
			logrus.WithError(err).Fatal("Failed to load config")
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

	logrus.Info("Welcome to housekeeper, an AI CH Cluster Observer 👀")
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
