package main

import (
	"fmt"
	"log"

	"github.com/spf13/viper"
)

func main() {
	fmt.Println("Welcome to housekeeper, an AI CH Cluster Observer 👀")

	loadConfig()
	apiKey := viper.GetString("gemini_key")
	if apiKey == "" {
		log.Fatal("Please set api_key in configs")
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
