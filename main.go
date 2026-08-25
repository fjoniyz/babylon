package main

import (
	"log"
	"os"
	"strconv"

	"babylon/cmd"
)

func main() {
	appID := int64(4700308)
	if appIDStr := os.Getenv("GITHUB_APP_ID"); appIDStr != "" {
		parsed, err := strconv.ParseInt(appIDStr, 10, 64)
		if err != nil {
			log.Fatalf("Invalid GITHUB_APP_ID: %v", err)
		}
		appID = parsed
	}

	keyPath := os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH")
	if keyPath == "" {
		keyPath = "/home/d0sta/Downloads/fjoniyz-babylon.2026-08-24.private-key.pem"
	}

	webhookSecret := os.Getenv("WEBHOOK_SECRET")

	cmd.InitServer(appID, keyPath, webhookSecret)
}
