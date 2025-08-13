package main

import (
	"fmt"
	"log"

<<<<<<< Updated upstream
=======
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
>>>>>>> Stashed changes
	"github.com/spf13/viper"
)

func main() {
	// Define all flags using pflag
	performanceMode := pflag.Bool("performance", false, "Run query performance analysis instead of error analysis")
	mcpMode := pflag.Bool("mcp", false, "Run MCP stdio server for ClickHouse system table queries")
	configPath := pflag.String("config", "", "Path to YAML config (or set HOUSEKEEPER_CONFIG)")
	
	// ClickHouse flags
	pflag.String("ch-host", "127.0.0.1", "ClickHouse host")
	pflag.Int("ch-port", 9000, "ClickHouse port")
	pflag.String("ch-user", "default", "ClickHouse user")
	pflag.String("ch-password", "", "ClickHouse password")
	pflag.String("ch-database", "default", "ClickHouse database")
	pflag.String("ch-cluster", "default", "ClickHouse cluster name")
	
	// Prometheus/Victoria Metrics flags
	pflag.String("prom-host", "localhost", "Prometheus/Victoria Metrics host")
	pflag.Int("prom-port", 8481, "Prometheus/Victoria Metrics port")
	pflag.Bool("prom-vm-cluster", false, "Enable Victoria Metrics cluster mode")
	pflag.String("prom-vm-tenant", "0", "Victoria Metrics tenant ID")
	pflag.String("prom-vm-prefix", "", "Victoria Metrics path prefix")
	
	// Parse all flags
	pflag.Parse()
	
	// Bind pflags to viper
	viper.BindPFlag("clickhouse.host", pflag.Lookup("ch-host"))
	viper.BindPFlag("clickhouse.port", pflag.Lookup("ch-port"))
	viper.BindPFlag("clickhouse.user", pflag.Lookup("ch-user"))
	viper.BindPFlag("clickhouse.password", pflag.Lookup("ch-password"))
	viper.BindPFlag("clickhouse.database", pflag.Lookup("ch-database"))
	viper.BindPFlag("clickhouse.cluster", pflag.Lookup("ch-cluster"))
	
	viper.BindPFlag("prometheus.host", pflag.Lookup("prom-host"))
	viper.BindPFlag("prometheus.port", pflag.Lookup("prom-port"))
	viper.BindPFlag("prometheus.vm_cluster_mode", pflag.Lookup("prom-vm-cluster"))
	viper.BindPFlag("prometheus.vm_tenant_id", pflag.Lookup("prom-vm-tenant"))
	viper.BindPFlag("prometheus.vm_path_prefix", pflag.Lookup("prom-vm-prefix"))

	if *mcpMode {
		// Try to load config file if provided, but don't fail if it doesn't exist
		// Command-line flags will provide the values
		if err := loadConfig(*configPath); err != nil {
<<<<<<< Updated upstream
			log.Fatal(err)
=======
			// Only log debug message if config file wasn't found
			logrus.WithError(err).Debug("Config file not found, using command-line flags")
>>>>>>> Stashed changes
		}
		// Do not print to stdout in MCP mode; stdout is reserved for JSON-RPC
		if err := RunMCPServer(); err != nil {
			log.Fatal(err)
		}
		return
	}

	fmt.Println("Welcome to housekeeper, an AI CH Cluster Observer 👀")

	if err := loadConfig(*configPath); err != nil {
		log.Fatal(err)
	}
	apiKey := viper.GetString("gemini_key")
	if apiKey == "" {
		log.Fatal("Please set api_key in configs")
	}

	if *performanceMode {
		fmt.Println("Analyzing query performance...")
		summary := AnalyzeQueryPerformanceWithAgent()
		fmt.Println(summary)
		return
	}

	errors, err := CHErrorAnalysis()
	if err != nil {
		log.Fatal(err)
	}

	if len(errors) > 0 {
		fmt.Println("Errors found:")
		summary := AnalyzeErrorsWithAgent(errors)
		fmt.Println(summary)

		if err := SendSlackMessage(summary, len(errors)); err != nil {
			log.Printf("Failed to send Slack message: %v", err)
		}
	} else {
		fmt.Println("No errors found in the last hour")
	}
}
