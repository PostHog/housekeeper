package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/generative-ai-go/genai"
	"github.com/spf13/viper"
	"google.golang.org/api/option"
)

func main() {
	fmt.Println("Welcome to shitpost")

	loadConfig()
	apiKey := viper.GetString("gemini_key")
	if apiKey == "" {
		log.Fatal("Please set api_key in configs")
	}

	ch()

	// ~~~ start ~~~
	fmt.Println("What is your name?")

	var input string
	fmt.Scanln(&input)

	fmt.Println("Hello, " + input + "!")

	ipAddress := fetchIPAddress()
	fmt.Println("Your IP address is:", ipAddress)

	fetchGeminiResponse(apiKey)
}

func fetchIPAddress() string {
	resp, err := http.Get("https://ifconfig.me")
	if err != nil {
		return "Error fetching IP address"
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "Error reading response body"
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return "Error parsing HTML"
	}

	ipAddress := strings.TrimSpace(doc.Find("#ip_address_cell").Text())
	return ipAddress
}

func fetchGeminiResponse(apiKey string) string {
	ctx := context.Background()

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		log.Fatal("Error creating client:", err)
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-1.5-flash")

	model.SetTemperature(0.7)
	model.SetMaxOutputTokens(1000)

	prompt := "Explain the difference between Go channels and mutexes in 2-3 sentences"

	fmt.Println("Sending prompt:", prompt)
	fmt.Println("---")

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
