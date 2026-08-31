package main

import (
	"log"
	"os"
	"strconv"

	"babylon/cmd"
)

func main() {
	appID := int64(0)
	if appIDStr := os.Getenv("GITHUB_APP_ID"); appIDStr != "" {
		parsed, err := strconv.ParseInt(appIDStr, 10, 64)
		if err != nil {
			log.Fatalf("Invalid GITHUB_APP_ID: %v", err)
		}
		appID = parsed
	}

	keyPath := os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH")
	if keyPath == "" {
		log.Fatalf("No private key file was found.")
	}

	webhookSecret := os.Getenv("WEBHOOK_SECRET")

	cmd.InitServer(appID, keyPath, webhookSecret)
}
