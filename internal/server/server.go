package server

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/bradleyfalzon/ghinstallation/v2"
	ghwebhook "github.com/go-playground/webhooks/v6/github"

	"babylon/internal/lock"
)

// Config holds configuration parameters for the Babylon server.
type Config struct {
	AppID          int64
	PrivateKeyPath string
	WebhookSecret  string
	StateBucket    string
	BaseDir        string
}

// Server is the HTTP server for Babylon.
type Server struct {
	cfg           Config
	appsTransport *ghinstallation.AppsTransport
	lockManager   *lock.Manager
	hook          *ghwebhook.Webhook
}

// New creates and initializes a new Babylon Server instance.
func New(ctx context.Context, cfg Config) (*Server, error) {
	appsTransport, err := ghinstallation.NewAppsTransportKeyFromFile(
		http.DefaultTransport,
		cfg.AppID,
		cfg.PrivateKeyPath,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize GitHub App transport: %w", err)
	}

	if cfg.StateBucket == "" {
		log.Printf("⚠️ WARNING: State bucket is not set. S3 locking will be disabled.")
	}

	lockManager, err := lock.NewManager(ctx, cfg.StateBucket, cfg.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize S3 Lock Manager: %w", err)
	}
	log.Printf(
		"S3 Lock Manager initialized with bucket: '%s' (AWS Region: %s)",
		lockManager.Bucket(),
		lockManager.Region(),
	)

	var opts []ghwebhook.Option
	if cfg.WebhookSecret != "" {
		opts = append(opts, ghwebhook.Options.Secret(cfg.WebhookSecret))
	}
	hook, err := ghwebhook.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize GitHub webhook receiver: %w", err)
	}

	return &Server{
		cfg:           cfg,
		appsTransport: appsTransport,
		lockManager:   lockManager,
		hook:          hook,
	}, nil
}

// Start launches the HTTP server and blocks until an error occurs.
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", s.handleWebhook)

	log.Printf("Babylon listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received %s request on /webhook", r.Method)
	event, err := s.hook.Parse(r, ghwebhook.IssueCommentEvent, ghwebhook.PullRequestEvent)
	if err != nil {
		log.Printf("Webhook parse error: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "Webhook parse error: %v\n", err)
		return
	}

	log.Printf("Successfully parsed event: %T", event)
	switch e := event.(type) {
	case ghwebhook.IssueCommentPayload:
		s.handleIssueCommentEvent(e)
	case ghwebhook.PullRequestPayload:
		s.handlePullRequestEvent(e)
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "OK")
}
