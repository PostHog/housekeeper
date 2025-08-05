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

func AnalyzeErrors(chError CHError) string {
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

	prompt := "Summarize the following error and prepare the summary for a slack channel message: \n" + chError.String()

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
