package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "time"

    "github.com/spf13/viper"
    logrus "github.com/sirupsen/logrus"
)

type SlackMessage struct {
	Blocks []SlackBlock `json:"blocks"`
}

type SlackBlock struct {
	Type     string            `json:"type"`
	Text     *SlackText        `json:"text,omitempty"`
	Elements []SlackElement    `json:"elements,omitempty"`
}

type SlackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type SlackElement struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func SendSlackMessage(summary string, errorCount int) error {
	webhookURL := viper.GetString("slack.webhook_url")
	if webhookURL == "" {
		return fmt.Errorf("slack webhook URL not configured")
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05 MST")
	
	message := SlackMessage{
		Blocks: []SlackBlock{
			{
				Type: "header",
				Text: &SlackText{
					Type: "plain_text",
					Text: "🔍 ClickHouse Error Analysis",
				},
			},
			{
				Type: "section",
				Text: &SlackText{
					Type: "mrkdwn",
					Text: summary,
				},
			},
			{
				Type: "context",
				Elements: []SlackElement{
					{
						Type: "mrkdwn",
						Text: fmt.Sprintf("*Errors Found:* %d", errorCount),
					},
					{
						Type: "mrkdwn",
						Text: fmt.Sprintf("*Time:* %s", timestamp),
					},
				},
			},
			{
				Type: "divider",
			},
		},
	}

	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("error marshaling slack message: %v", err)
	}

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("error sending slack message: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack API returned status %d: %s", resp.StatusCode, string(body))
	}

    logrus.Info("Slack message sent successfully")
    return nil
}
