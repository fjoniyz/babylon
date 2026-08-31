package internal

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var pulumiCmdRegex = regexp.MustCompile(
	`(?m)^pulumi\s+(preview|up|destroy|unlock)(?:.*?(?:--stack|-s)[=\s]([a-zA-Z0-9_\-\/]+))?\s*$`,
)

// ParsePulumiCommand extracts the action (preview, up, destroy, unlock) and optional stack (default "dev").
func ParsePulumiCommand(body string) (action, stack string) {
	body = strings.TrimSpace(body)
	matches := pulumiCmdRegex.FindStringSubmatch(body)
	if len(matches) < 2 {
		return "", ""
	}

	action = matches[1]
	if len(matches) > 2 && matches[2] != "" {
		stack = matches[2]
	} else {
		stack = "dev"
	}
	return action, stack
}

// SyncGhRepo clones or updates a repository branch/ref in the target workspace.
// Returns isNew=true if the repo was freshly initialized.
func SyncGhRepo(
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

// LocatePulumiCLI searches for the pulumi binary in PATH and standard fallback locations.
func LocatePulumiCLI() (pulumiPath, pulumiBinDir string, err error) {
	pulumiPath, err = exec.LookPath("pulumi")
	home, _ := os.UserHomeDir()
	pulumiBinDir = filepath.Join(home, ".pulumi", "bin")

	if err != nil {
		candidate := filepath.Join(pulumiBinDir, "pulumi")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, pulumiBinDir, nil
		}
		return "", "", fmt.Errorf("pulumi executable not found in PATH or %s", pulumiBinDir)
	}

	return pulumiPath, pulumiBinDir, nil
}

// FindPulumiDir searches for the first directory containing Pulumi.yaml (skipping .git, node_modules, .venv, etc.)
// Not sure if Atlantis also does it like this but I know that in Atlantis one way is to declare the workspaces in a yaml file and then it will simply read that file
func FindPulumiDir(root string) (string, error) {
	// First check root directory directly
	if _, err := os.Stat(filepath.Join(root, "Pulumi.yaml")); err == nil {
		return root, nil
	}
	if _, err := os.Stat(filepath.Join(root, "Pulumi.yml")); err == nil {
		return root, nil
	}

	var foundDir string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() &&
			(d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".pulumi" || d.Name() == ".venv" || d.Name() == "venv") {
			return filepath.SkipDir
		}
		if !d.IsDir() && (d.Name() == "Pulumi.yaml" || d.Name() == "Pulumi.yml") {
			foundDir = filepath.Dir(path)
			return filepath.SkipAll // Stop searching once found
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if foundDir == "" {
		return "", fmt.Errorf("no Pulumi.yaml found in repository")
	}
	return foundDir, nil
}

// InstallProjectDependencies sets up a Python virtual environment and installs requirements.
func InstallProjectDependencies(
	ctx context.Context,
	workDir, pulumiBinDir string,
) ([]string, error) {
	passphrase := os.Getenv("PULUMI_CONFIG_PASSPHRASE") // Defaults to "" if unset
	extraEnv := []string{
		fmt.Sprintf("PULUMI_CONFIG_PASSPHRASE=%s", passphrase),
	}

	log.Printf("Setting up Python virtual environment in %s...", workDir)
	venvDir := filepath.Join(workDir, ".venv")
	venvCmd := exec.CommandContext(ctx, "python3", "-m", "venv", venvDir)
	venvCmd.Dir = workDir
	if out, err := venvCmd.CombinedOutput(); err != nil {
		log.Printf("Warning: failed to create venv: %v (%s)", err, string(out))
	}

	pythonBin := filepath.Join(venvDir, "bin", "python3")
	if _, err := os.Stat(pythonBin); err != nil {
		pythonBin = filepath.Join(venvDir, "bin", "python")
	}

	// Also symlink venv -> .venv in case virtualenv is referenced as venv
	_ = os.Symlink(venvDir, filepath.Join(workDir, "venv"))

	// Assuming that the project uses requirements.txt. We should also have a case where the project has pyproject.toml file
	reqFile := filepath.Join(workDir, "requirements.txt")
	if _, err := os.Stat(reqFile); err == nil {
		pipCmd := exec.CommandContext(
			ctx,
			pythonBin,
			"-m",
			"pip",
			"install",
			"-r",
			"requirements.txt",
		)
		pipCmd.Dir = workDir
		if out, err := pipCmd.CombinedOutput(); err != nil {
			log.Printf("pip install error: %v (%s)", err, string(out))
			return nil, fmt.Errorf("pip install failed: %w: %s", err, string(out))
		}
	} else {
		// Fallback: install pulumi SDK directly
		pipCmd := exec.CommandContext(ctx, pythonBin, "-m", "pip", "install", "pulumi", "pulumi-aws")
		pipCmd.Dir = workDir
		_ = pipCmd.Run()
	}

	venvBin := filepath.Join(venvDir, "bin")
	pathEnv := fmt.Sprintf("PATH=%s:%s:%s", venvBin, pulumiBinDir, os.Getenv("PATH"))
	extraEnv = append(extraEnv,
		pathEnv,
		fmt.Sprintf("VIRTUAL_ENV=%s", venvDir),
		fmt.Sprintf("PULUMI_PYTHON_CMD=%s", pythonBin),
	)
	log.Printf(
		"Successfully prepared Python virtualenv at %s (PULUMI_PYTHON_CMD=%s)",
		venvDir,
		pythonBin,
	)
	return extraEnv, nil
}

// RunPulumiCommand executes a Pulumi CLI command (preview, up, destroy) with appropriate flags.
func RunPulumiCommand(
	ctx context.Context,
	pulumiPath, workDir, stack string,
	extraEnv []string,
	command string,
) (string, error) {
	args := []string{command, "--stack", stack, "--non-interactive"}
	if command == "up" || command == "destroy" {
		args = append(args, "--yes")
	}

	log.Printf("Executing: %s %v in %s", pulumiPath, args, workDir)
	cmd := exec.CommandContext(ctx, pulumiPath, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), extraEnv...)

	output, err := cmd.CombinedOutput()
	log.Printf(
		"Pulumi %s finished. Exit error: %v | Output length: %d bytes",
		command,
		err,
		len(output),
	)
	return string(output), err
}
