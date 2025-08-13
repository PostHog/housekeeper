package main

import (
	"flag"
	"fmt"
	"log"

	logrus "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

func main() {
	performanceMode := flag.Bool("performance", false, "Run query performance analysis instead of error analysis")
	mcpMode := flag.Bool("mcp", false, "Run MCP stdio server for ClickHouse system table queries")
	sseMode := flag.Bool("sse", false, "Run MCP HTTP SSE server for ClickHouse system table queries")
	tsnetMode := flag.Bool("tsnet", false, "Run MCP server on Tailscale network using tsnet")
	configPath := flag.String("config", "", "Path to YAML config (or set HOUSEKEEPER_CONFIG)")
	flag.Parse()

	// Logger configured by config (log.level, log.format) after loadConfig

	if *mcpMode && !*sseMode {
		if err := loadConfig(*configPath); err != nil {
			log.Fatal(err)
		}
		logrus.WithFields(logrus.Fields{"mode": "stdio", "config": viper.ConfigFileUsed()}).Info("Starting MCP server")
		// Do not print to stdout in MCP mode; stdout is reserved for JSON-RPC
		if err := RunMCPServer(); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *mcpMode && *sseMode {
		if err := loadConfig(*configPath); err != nil {
			log.Fatal(err)
		}
		port := viper.GetInt("sse.port")
		if port == 0 {
			port = 3333
		}
		logrus.WithFields(logrus.Fields{"mode": "sse", "port": port, "config": viper.ConfigFileUsed()}).Info("Starting MCP SSE server")
		if err := RunMCPSSEServer(port); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *tsnetMode {
		if err := loadConfig(*configPath); err != nil {
			log.Fatal(err)
		}
		if !viper.GetBool("tsnet.enabled") {
			log.Fatal("tsnet mode requested but tsnet.enabled is false in config")
		}
		logrus.WithFields(logrus.Fields{"mode": "tsnet", "config": viper.ConfigFileUsed()}).Info("Starting MCP tsnet server")
		if err := RunMCPTsnetServer(); err != nil {
			log.Fatal(err)
		}
		return
	}

	logrus.Info("Welcome to housekeeper, an AI CH Cluster Observer 👀")

	if err := loadConfig(*configPath); err != nil {
		log.Fatal(err)
	}
	apiKey := viper.GetString("gemini_key")
	if apiKey == "" {
		log.Fatal("Please set api_key in configs")
	}

	if *performanceMode {
		logrus.Info("Analyzing query performance...")
		summary := AnalyzeQueryPerformanceWithAgent()
		fmt.Println(summary)
		return
	}

	errors, err := CHErrorAnalysis()
	if err != nil {
		log.Fatal(err)
	}

	if len(errors) > 0 {
		logrus.WithField("count", len(errors)).Info("Errors found in last hour")
		summary := AnalyzeErrorsWithAgent(errors)
		fmt.Println(summary)

		if err := SendSlackMessage(summary, len(errors)); err != nil {
			logrus.WithError(err).Warn("Failed to send Slack message")
		}
	} else {
		logrus.Info("No errors found in the last hour")
	}
}
