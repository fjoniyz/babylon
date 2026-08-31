package main

import (
	"context"
	"log"
	"os"
	"strconv"

	"babylon/internal/server"
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
	stateBucket := os.Getenv("STATE_BUCKET")
	if stateBucket == "" {
		stateBucket = os.Getenv("BABYLON_S3_BUCKET")
	}

	ctx := context.Background()
	srv, err := server.New(ctx, server.Config{
		AppID:          appID,
		PrivateKeyPath: keyPath,
		WebhookSecret:  webhookSecret,
		StateBucket:    stateBucket,
	})
	if err != nil {
		log.Fatalf("Failed to initialize server: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	addr := ":" + port

	if err := srv.Start(addr); err != nil {
		log.Fatalf("Server stopped: %v", err)
	}
}
