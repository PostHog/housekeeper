package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func sayHello() {
	fmt.Println("What is your name?")

	var input string
	fmt.Scanln(&input)

	fmt.Println("Hello, " + input + "!")

	ipAddress := fetchIPAddress()
	fmt.Println("Your IP address is:", ipAddress)

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
