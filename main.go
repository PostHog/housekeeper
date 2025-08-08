package main

import (
    "flag"
    "fmt"
    "log"

    "github.com/spf13/viper"
)

func main() {
    performanceMode := flag.Bool("performance", false, "Run query performance analysis instead of error analysis")
    mcpMode := flag.Bool("mcp", false, "Run MCP stdio server for ClickHouse system table queries")
    configPath := flag.String("config", "", "Path to YAML config (or set HOUSEKEEPER_CONFIG)")
    flag.Parse()

    if *mcpMode {
        if err := loadConfig(*configPath); err != nil {
            log.Fatal(err)
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
