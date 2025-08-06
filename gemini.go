package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"github.com/spf13/viper"
	"google.golang.org/api/option"
)

func AnalyzeErrors(chErrors CHErrors) string {
	ctx := context.Background()

	apiKey := viper.GetString("gemini_key")

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		log.Fatal("Error creating client:", err)
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-1.5-flash")

	model.SetTemperature(0.7)
	model.SetMaxOutputTokens(1000)

	prompt := "Summarize the following ClickHouse errors and prepare the summary for a slack channel message. \n" +
		"Contents are from system.errors table.\n" +
		"Contains error codes with the number of times they have been triggered.\n" +
		"Columns:\n" +
		"name (String) — name of the error (errorCodeToName).\n" +
		"code (Int32) — code number of the error.\n" +
		"value (UInt64) — the number of times this error happened.\n" +
		"last_error_time (DateTime) — the time when the last error happened.\n" +
		"last_error_message (String) — message for the last error.\n" +
		"last_error_trace (Array(UInt64)) — A stack trace that represents a list of physical addresses where the called methods are stored.\n" +
		"remote (UInt8) — remote exception (i.e. received during one of the distributed queries).\n" +
		"Be sure to ruthlessly prioritize the most important errors first.\n" +
		"Do not exaggerate the severity of the errors and suggest solutions.\n" +
		chErrors.String()

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		log.Fatal("Error generating content:", err)
	}

	fullResp := []string{}

	for _, candidate := range resp.Candidates {
		for _, part := range candidate.Content.Parts {
			fmt.Printf("%s", part)
			fullResp = append(fullResp, fmt.Sprintf("%s", part))
		}
	}
	fmt.Println()

	return strings.Join(fullResp, " ")
}
