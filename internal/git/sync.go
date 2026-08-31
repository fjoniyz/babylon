package git

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// SyncRepo clones or updates a repository branch/ref in the target workspace.
// Returns isNew=true if the repo was freshly initialized.
func SyncRepo(
	ctx context.Context,
	token, owner, repo string,
	prNum int,
	destDir string,
) (isNew bool, err error) {
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	basicAuth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("x-access-token:%s", token)))
	authHeader := fmt.Sprintf("http.extraHeader=AUTHORIZATION: basic %s", basicAuth)

	gitDir := filepath.Join(destDir, ".git")
	fetchRef := fmt.Sprintf("pull/%d/head", prNum)

	if _, statErr := os.Stat(gitDir); statErr == nil {
		// Existing workspace: fetch latest commit and update branch
		log.Printf("Updating existing workspace at %s for PR #%d...", destDir, prNum)
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
		if out, fetchErr := fetchCmd.CombinedOutput(); fetchErr != nil {
			return false, fmt.Errorf("git fetch update failed: %w: %s", fetchErr, string(out))
		}

		checkoutCmd := exec.CommandContext(
			ctx,
			"git",
			"-C",
			destDir,
			"checkout",
			"-B",
			fmt.Sprintf("pr-%d", prNum),
			"FETCH_HEAD",
		)
		if out, chkErr := checkoutCmd.CombinedOutput(); chkErr != nil {
			return false, fmt.Errorf("git checkout failed: %w: %s", chkErr, string(out))
		}

		resetCmd := exec.CommandContext(ctx, "git", "-C", destDir, "reset", "--hard", "FETCH_HEAD")
		if out, rstErr := resetCmd.CombinedOutput(); rstErr != nil {
			return false, fmt.Errorf("git reset failed: %w: %s", rstErr, string(out))
		}

		return false, nil
	}

	// Fresh clone
	log.Printf("Creating fresh workspace at %s for PR #%d...", destDir, prNum)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return false, fmt.Errorf("mkdir workspace failed: %w", err)
	}

	initCmd := exec.CommandContext(ctx, "git", "init", destDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git init failed: %w: %s", err, string(out))
	}

	remoteCmd := exec.CommandContext(ctx, "git", "-C", destDir, "remote", "add", "origin", repoURL)
	if out, err := remoteCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git remote add failed: %w: %s", err, string(out))
	}

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
		return false, fmt.Errorf("git fetch PR failed: %w: %s", err, string(out))
	}

	checkoutCmd := exec.CommandContext(
		ctx,
		"git",
		"-C",
		destDir,
		"checkout",
		"-B",
		fmt.Sprintf("pr-%d", prNum),
		"FETCH_HEAD",
	)
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git checkout failed: %w: %s", err, string(out))
	}

	return true, nil
}
