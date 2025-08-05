package main

import (
	"fmt"
	"log"

	"github.com/spf13/viper"
)

func main() {
	fmt.Println("Welcome to shitpost")

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
		for _, chError := range errors {
			AnalyzeErrors(chError)
		}
	}

}
