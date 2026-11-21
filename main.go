package main

import (
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func main() {
	// Define all flags using pflag
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

	// HTTP server flags
	pflag.String("http-addr", ":8080", "Address for the HTTP MCP server (e.g. :8080)")
	pflag.String("http-auth-token", "", "Bearer token for HTTP authentication (empty = no auth)")

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

	_ = viper.BindPFlag("http.addr", pflag.Lookup("http-addr"))
	_ = viper.BindPFlag("http.auth_token", pflag.Lookup("http-auth-token"))

	// Try to load config file if provided, but don't fail if it doesn't exist
	// Command-line flags will provide the values
	if err := loadConfig(*configPath); err != nil {
		logrus.WithError(err).Debug("Config file not found, using command-line flags")
	}
	logrus.Info("Starting MCP server")
	if err := RunMCPServer(); err != nil {
		logrus.WithError(err).Fatal("Failed to run MCP server")
	}
}
