package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/bradleyfalzon/ghinstallation/v2"
	ghwebhook "github.com/go-playground/webhooks/v6/github"
	"github.com/google/go-github/v90/github"

	"babylon/internal"
)

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

func executePulumi(
	ctx context.Context,
	ghClient *github.Client,
	appsTransport *ghinstallation.AppsTransport,
	lockManager *internal.S3LockManager,
	installationID int64,
	owner, repo string,
	prNum int,
	command string,
	stack string,
	commenter string,
) {
	// Fetch PR details to obtain the target branch and commit SHA
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
	headSHA := pr.GetHead().GetSHA()
	log.Printf("PR #%d target branch: %s (SHA: %s)", prNum, branchName, headSHA)

	// Check existing S3 Lock for this stack
	existingLock, err := lockManager.GetLock(ctx, owner, repo, stack)
	if err != nil {
		log.Printf("Warning: failed to query S3 lock: %v", err)
	}

	// Concurrency check: Is this stack locked by another PR?
	if existingLock != nil && existingLock.PRNumber != prNum {
		log.Printf(
			"Stack %s is locked by PR #%d (request from PR #%d)",
			stack,
			existingLock.PRNumber,
			prNum,
		)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf(
				"🔒 **Stack `%s` is currently locked by PR #%d** (branch `%s`) by @%s.\nPlease unlock or merge PR #%d before running operations on this stack.",
				stack,
				existingLock.PRNumber,
				existingLock.Branch,
				existingLock.LockedBy,
				existingLock.PRNumber,
			),
		)
		return
	}

	// Handle 'unlock' command
	if command == "unlock" {
		if existingLock == nil {
			_ = postStatusComment(
				ghClient,
				owner,
				repo,
				prNum,
				fmt.Sprintf("ℹ️ **Stack `%s` is not currently locked**.", stack),
			)
			return
		}
		if err := lockManager.DeleteLock(ctx, owner, repo, prNum, stack); err != nil {
			log.Printf("Error unlocking stack %s: %v", stack, err)
		}
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf(
				"🔓 **Stack `%s` Unlocked**\nDeleted S3 lock and cleaned up workspace.",
				stack,
			),
		)
		return
	}

	// Safety check for 'up' (apply) command
	if command == "up" {
		if existingLock == nil || existingLock.Status != "previewed" {
			_ = postStatusComment(
				ghClient,
				owner,
				repo,
				prNum,
				fmt.Sprintf(
					"⚠️ **No active preview found for stack `%s`**\nPlease run `pulumi preview --stack %s` before applying changes.",
					stack,
					stack,
				),
			)
			return
		}
		if existingLock.HeadSHA != headSHA {
			_ = postStatusComment(
				ghClient,
				owner,
				repo,
				prNum,
				fmt.Sprintf(
					"⚠️ **New commits detected since last preview**\n- Previewed SHA: `%s`\n- Current PR SHA: `%s`\n\nPlease run `pulumi preview --stack %s` again before applying.",
					existingLock.HeadSHA[:7],
					headSHA[:7],
					stack,
				),
			)
			return
		}
	}

	// Generate GitHub App installation token for cloning/fetching
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

	// Locate persistent workspace directory for this PR & stack
	destDir := lockManager.GetWorkspacePath(owner, repo, prNum, stack)

	// Synchronize Git workspace
	log.Printf(
		"Syncing workspace for %s/%s PR #%d (%s) into %s",
		owner,
		repo,
		prNum,
		branchName,
		destDir,
	)
	isNew, err := internal.SyncGhRepo(ctx, token, owner, repo, prNum, destDir)
	if err != nil {
		log.Printf("Git sync failed: %v", err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf("Git sync failed: %v", err),
		)
		return
	}
	log.Printf("Successfully synchronized workspace at %s (isNew: %v)", destDir, isNew)

	// 9. Locate Pulumi CLI binary
	pulumiPath, pulumiBinDir, err := internal.LocatePulumiCLI()
	if err != nil {
		log.Printf("Pulumi executable not found: %v", err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			"### ❌ Pulumi CLI not found on server\nPlease ensure Pulumi is installed.",
		)
		return
	}

	// 10. Discover Pulumi project directory containing Pulumi.yaml
	workDir, err := internal.FindPulumiDir(destDir)
	if err != nil {
		log.Printf("Pulumi project discovery failed: %v", err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf(
				"### ❌ Pulumi Project Discovery Failed\nCould not find `Pulumi.yaml` in the cloned repository:\n`%v`",
				err,
			),
		)
		return
	}
	log.Printf("Discovered Pulumi project directory: %s", workDir)

	// 11. Install Python virtualenv & dependencies
	extraEnv, err := internal.InstallProjectDependencies(ctx, workDir, pulumiBinDir)
	if err != nil {
		log.Printf("Dependency install failed: %v", err)
		_ = postStatusComment(
			ghClient,
			owner,
			repo,
			prNum,
			fmt.Sprintf("### ❌ Dependency Installation Failed\n```\n%v\n```", err),
		)
		return
	}

	// 12. Execute pulumi command in discovered project directory
	output, err := internal.RunPulumiCommand(ctx, pulumiPath, workDir, stack, extraEnv, command)
	log.Printf(
		"--- PULUMI OUTPUT START ---\n%s\n--- PULUMI OUTPUT END ---",
		output,
	)

	if err != nil {
		log.Printf("Pulumi %s failed with error: %v", command, err)
		commentBody := fmt.Sprintf(
			"Pulumi %s Failed (stack: `%s`)\n```\n%s\n```\n**Error:** `%v`",
			command,
			stack,
			output,
			err,
		)
		if postErr := postStatusComment(ghClient, owner, repo, prNum, commentBody); postErr != nil {
			log.Printf("Error posting failure comment: %v", postErr)
		}
	} else {
		log.Printf("Pulumi %s succeeded.", command)
		commentBody := fmt.Sprintf("### 🏗️ Pulumi %s Output (stack: `%s`)\n```\n%s\n```", command, stack, output)
		if postErr := postStatusComment(ghClient, owner, repo, prNum, commentBody); postErr != nil {
			log.Printf("Error posting success comment: %v", postErr)
		}

		switch command {
		case "preview":
			if saveErr := lockManager.SaveLock(ctx, &internal.Lock{
				Owner:         owner,
				Repo:          repo,
				PRNumber:      prNum,
				Stack:         stack,
				Branch:        branchName,
				HeadSHA:       headSHA,
				LockedBy:      commenter,
				Status:        "previewed",
				WorkspacePath: destDir,
			}); saveErr != nil {
				log.Printf("ERROR: Failed to save S3 lock for stack %s: %v", stack, saveErr)
			}
		case "up", "destroy":
			if delErr := lockManager.DeleteLock(ctx, owner, repo, prNum, stack); delErr != nil {
				log.Printf("ERROR: Failed to release S3 lock for stack %s: %v", stack, delErr)
			} else {
				log.Printf("Released S3 lock and cleaned up workspace for stack %s after %s", stack, command)
			}
		}
	}
}

func handleIssueCommentEvent(
	payload ghwebhook.IssueCommentPayload,
	appsTransport *ghinstallation.AppsTransport,
	lockManager *internal.S3LockManager,
) {
	if payload.Issue.PullRequest == nil {
		return
	}

	commenter := payload.Comment.User.Login

	// Ignore comments posted by bots to prevent infinite reply loops
	if payload.Sender.Type == "Bot" || payload.Comment.User.Type == "Bot" {
		return
	}

	prNum := int(payload.Issue.Number)
	owner := payload.Repository.Owner.Login
	repo := payload.Repository.Name

	commentBody := payload.Comment.Body

	log.Printf("Processing comment #%d on PR #%d by @%s", payload.Comment.ID, prNum, commenter)

	// Parse command from comment
	cmd, stack := internal.ParsePulumiCommand(commentBody)
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
		lockManager,
		payload.Installation.ID,
		owner,
		repo,
		prNum,
		cmd,
		stack,
		commenter,
	)
}

func handlePullRequestEvent(
	payload ghwebhook.PullRequestPayload,
	lockManager *internal.S3LockManager,
) {
	// If PR is closed or merged, clean up all S3 locks and workspace files for this PR
	if payload.Action == "closed" {
		prNum := int(payload.PullRequest.Number)
		owner := payload.Repository.Owner.Login
		repo := payload.Repository.Name

		log.Printf(
			"PR #%d on %s/%s was closed. Cleaning up S3 locks and workspaces...",
			prNum,
			owner,
			repo,
		)
		ctx := context.Background()
		if err := lockManager.DeleteAllPRLocks(ctx, owner, repo, prNum); err != nil {
			log.Printf("Error cleaning up S3 locks for closed PR #%d: %v", prNum, err)
		}
	}
}

func InitServer(appID int64, privateKeyPath, webhookSecret string) {
	ctx := context.Background()

	// Initialize GitHub App Transport with private key
	appsTransport, err := ghinstallation.NewAppsTransportKeyFromFile(
		http.DefaultTransport,
		appID,
		privateKeyPath,
	)
	if err != nil {
		log.Fatalf("Failed to initialize GitHub App transport: %v", err)
	}

	// Initialize S3 Lock Manager
	bucketName := os.Getenv("STATE_BUCKET")
	if bucketName == "" {
		bucketName = os.Getenv("BABYLON_S3_BUCKET")
	}
	if bucketName == "" {
		log.Printf(
			"⚠️ WARNING: Neither STATE_BUCKET nor BABYLON_S3_BUCKET is set. S3 locking will be disabled.",
		)
	}
	lockManager, err := internal.NewS3LockManager(ctx, bucketName, "")
	if err != nil {
		log.Fatalf("Failed to initialize S3 Lock Manager: %v", err)
	}
	log.Printf(
		"S3 Lock Manager initialized with bucket: '%s' (AWS Region: %s)",
		lockManager.Bucket(),
		lockManager.Region(),
	)

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
		event, err := hook.Parse(r, ghwebhook.IssueCommentEvent, ghwebhook.PullRequestEvent)
		if err != nil {
			log.Printf("Webhook parse error: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "Webhook parse error: %v\n", err)
			return
		}

		log.Printf("Successfully parsed event: %T", event)
		switch e := event.(type) {
		case ghwebhook.IssueCommentPayload:
			handleIssueCommentEvent(e, appsTransport, lockManager)
		case ghwebhook.PullRequestPayload:
			handlePullRequestEvent(e, lockManager)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "OK")
	})

	log.Println("Babylon listening on :8090")
	log.Fatal(http.ListenAndServe(":8090", nil))
}
