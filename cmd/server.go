package cmd

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bradleyfalzon/ghinstallation/v2"
	ghwebhook "github.com/go-playground/webhooks/v6/github"
	"github.com/google/go-github/v90/github"

	"babylon/internal"
)

var (
	pulumiPreviewRegex = regexp.MustCompile(`(?m)^pulumi\s+preview$`)
	pulumiDestroyRegex = regexp.MustCompile(`(?m)^pulumi\s+destroy$`)
	pulumiUpRegex      = regexp.MustCompile(`(?m)^pulumi\s+up$`)
)

func parseCommand(body string) string {
	body = strings.TrimSpace(body)

	switch {
	case pulumiPreviewRegex.MatchString(body):
		return "preview"
	case pulumiUpRegex.MatchString(body):
		return "up"
	case pulumiDestroyRegex.MatchString(body):
		return "destroy"
	default:
		return ""
	}
}

func postStatusComment(
	client *github.Client,
	owner, repo string,
	prNum int,
	status string,
) error {
	_, _, err := client.Issues.CreateComment(
		context.Background(),
		owner,
		repo,
		prNum,
		&github.IssueComment{
			Body: github.Ptr(fmt.Sprintf("### 🏗️ Babylon Status\n%s", status)),
		},
	)
	return err
}

func cloneGhRepo(ctx context.Context, token, owner, repo string, prNum int, destDir string) error {
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

	// GitHub Git HTTP protocol requires Basic Auth: base64(x-access-token:<token>)
	basicAuth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("x-access-token:%s", token)))
	authHeader := fmt.Sprintf("http.extraHeader=AUTHORIZATION: basic %s", basicAuth)

	// Initialize git repo in destDir
	initCmd := exec.CommandContext(ctx, "git", "init", destDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init failed: %w: %s", err, string(out))
	}

	// Add remote origin
	remoteCmd := exec.CommandContext(ctx, "git", "-C", destDir, "remote", "add", "origin", repoURL)
	if out, err := remoteCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git remote add failed: %w: %s", err, string(out))
	}

	// Fetch the exact PR ref using Basic auth header
	fetchRef := fmt.Sprintf("pull/%d/head:pr-%d", prNum, prNum)
	fetchCmd := exec.CommandContext(
		ctx,
		"git",
		"-C",
		destDir,
		"-c",
		authHeader,
		"fetch",
		"--depth=1",
		"origin",
		fetchRef,
	)
	fetchCmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch PR failed: %w: %s", err, string(out))
	}

	// Checkout the PR branch
	checkoutCmd := exec.CommandContext(
		ctx,
		"git",
		"-C",
		destDir,
		"checkout",
		fmt.Sprintf("pr-%d", prNum),
	)
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout failed: %w: %s", err, string(out))
	}
	return nil
}

func executePulumi(
	ctx context.Context,
	ghClient *github.Client,
	appsTransport *ghinstallation.AppsTransport,
	installationID int64,
	owner, repo string,
	prNum int,
	command string,
) {
	// Fetch PR details to obtain the target branch name
	pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNum)
	if err != nil {
		log.Printf("Failed to get PR #%d: %v", prNum, err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf("Failed to get PR info: %v", err),
		)
		return
	}
	branchName := pr.GetHead().GetRef()
	log.Printf("PR #%d target branch: %s", prNum, branchName)

	// Verify repo access and permissions granted to this token
	repoInfo, _, repoErr := ghClient.Repositories.Get(ctx, owner, repo)
	if repoErr != nil {
		log.Printf("GitHub API Repo check error: %v", repoErr)
	} else {
		log.Printf("Repository: %s | Private: %v | Token Permissions: %+v", repoInfo.GetFullName(), repoInfo.GetPrivate(), repoInfo.GetPermissions())
	}

	// Generate GitHub App installation token for cloning
	installationTransport := ghinstallation.NewFromAppsTransport(appsTransport, installationID)
	token, err := installationTransport.Token(ctx)
	if err != nil {
		log.Printf("Failed to get installation token: %v", err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf("Failed to get auth token: %v", err),
		)
		return
	}

	// Create temporary workspace directory
	destDir, err := os.MkdirTemp("", fmt.Sprintf("babylon-%s-%s-pr%d-*", owner, repo, prNum))
	if err != nil {
		log.Printf("Failed to create temporary directory: %v", err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf("Failed to create workspace: %v", err),
		)
		return
	}
	defer os.RemoveAll(destDir)

	// Clone the repository at the PR branch
	log.Printf(
		"Cloning repository %s/%s PR #%d (%s) into %s",
		owner,
		repo,
		prNum,
		branchName,
		destDir,
	)
	if err := cloneGhRepo(ctx, token, owner, repo, prNum, destDir); err != nil {
		log.Printf("Git clone failed: %v", err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf("Git clone failed: %v", err),
		)
		return
	}
	log.Printf("Successfully cloned repository into %s", destDir)

	// Locate Pulumi CLI binary
	pulumiPath, pulumiBinDir, err := internal.LocatePulumiCLI()
	if err != nil {
		log.Printf("Pulumi executable not found: %v", err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			"Pulumi CLI not found on server\nPlease ensure Pulumi is installed.",
		)
		return
	}

	// Discover Pulumi project directory containing Pulumi.yaml
	workDir, err := internal.FindPulumiDir(destDir)
	if err != nil {
		log.Printf("Pulumi project discovery failed: %v", err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf(
				"Pulumi Project Discovery Failed\nCould not find `Pulumi.yaml` in the cloned repository:\n`%v`",
				err,
			),
		)
		return
	}
	log.Printf("Discovered Pulumi project directory: %s", workDir)

	// Install project dependencies (Python virtualenv / Node npm)
	extraEnv, err := internal.InstallProjectDependencies(ctx, workDir, pulumiBinDir)
	if err != nil {
		log.Printf("Dependency install failed: %v", err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf("Dependency Installation Failed\n```\n%v\n```", err),
		)
		return
	}

	// Execute pulumi preview in discovered project directory
	output, err := internal.RunPulumiCommand(ctx, pulumiPath, workDir, "dev", extraEnv, command)
	log.Printf(
		output,
	)

	if err != nil {
		log.Printf("Pulumi %s failed with error: %v", command, err)
		commentBody := fmt.Sprintf(
			"Pulumi %s Failed (stack: `dev`)\n```\n%s\n```\n**Error:** `%v`",
			command,
			output,
			err,
		)
		if postErr := postStatusComment(ghClient, owner, repo, prNum, commentBody); postErr != nil {
			log.Printf("Error posting failure comment: %v", postErr)
		}
	} else {
		log.Printf("Pulumi %s succeeded.", command)
		commentBody := fmt.Sprintf("### 🏗️ Pulumi %s Output (stack: `dev`)\n```\n%s\n```", command, output)
		if postErr := postStatusComment(ghClient, owner, repo, prNum, commentBody); postErr != nil {
			log.Printf("Error posting success comment: %v", postErr)
		}
	}

	// AWS S3 integration check (if AWS credentials exist)
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Printf("Note: AWS SDK config not loaded: %v", err)
	} else {
		s3Client := s3.NewFromConfig(cfg)
		result, err := s3Client.ListBuckets(ctx, &s3.ListBucketsInput{})
		if err != nil {
			log.Printf("Note: Could not list S3 buckets: %v", err)
		} else {
			for _, bucket := range result.Buckets {
				log.Printf("Found S3 Bucket: %s", *bucket.Name)
			}
		}
	}
}

func handleIssueCommentEvent(
	payload ghwebhook.IssueCommentPayload,
	appsTransport *ghinstallation.AppsTransport,
) {
	// Verify it's a PR comment (not Issue)
	if payload.Issue.PullRequest == nil {
		return // Skip non-PR comments
	}

	commenter := payload.Comment.User.Login

	// Ignore comments posted by bots to prevent infinite reply loops
	if payload.Sender.Type == "Bot" || strings.HasSuffix(commenter, "[bot]") {
		return
	}

	prNum := int(payload.Issue.Number)
	owner := payload.Repository.Owner.Login
	repo := payload.Repository.Name

	commentBody := payload.Comment.Body

	log.Printf("Processing comment #%d on PR #%d by @%s", payload.Comment.ID, prNum, commenter)

	// Parse command from comment
	cmd := parseCommand(commentBody)
	if cmd == "" {
		return // Not a Pulumi command, ignore
	}

	if payload.Installation.ID == 0 {
		log.Printf("Warning: installation ID is 0, cannot authenticate as GitHub App")
		return
	}

	ctx := context.Background()

	// Create GitHub client authenticated as the GitHub App installation for this repo
	installationTransport := ghinstallation.NewFromAppsTransport(
		appsTransport,
		payload.Installation.ID,
	)
	ghClient, err := github.NewClient(github.WithTransport(installationTransport))
	if err != nil {
		log.Printf("Error creating GitHub client: %v", err)
		return
	}

	executePulumi(
		ctx,
		ghClient,
		appsTransport,
		payload.Installation.ID,
		owner,
		repo,
		prNum,
		cmd,
	)
}

func InitServer(appID int64, privateKeyPath, webhookSecret string) {
	// Initialize GitHub App Transport with private key
	appsTransport, err := ghinstallation.NewAppsTransportKeyFromFile(
		http.DefaultTransport,
		appID,
		privateKeyPath,
	)
	if err != nil {
		log.Fatalf("Failed to initialize GitHub App transport: %v", err)
	}

	// Create webhook receiver (with secret if configured)
	var opts []ghwebhook.Option
	if webhookSecret != "" {
		opts = append(opts, ghwebhook.Options.Secret(webhookSecret))
	}
	hook, err := ghwebhook.New(opts...)
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Received %s request on /webhook", r.Method)
		event, err := hook.Parse(r, ghwebhook.IssueCommentEvent)
		if err != nil {
			log.Printf("Webhook parse error: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "Webhook parse error: %v\n", err)
			return
		}

		log.Printf("Successfully parsed event: %T", event)
		switch e := event.(type) {
		case ghwebhook.IssueCommentPayload:
			handleIssueCommentEvent(e, appsTransport)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "OK")
	})

	log.Println("Babylon listening on :8090")
	log.Fatal(http.ListenAndServe(":8090", nil))
}
