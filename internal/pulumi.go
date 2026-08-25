package internal

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

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
func InstallProjectDependencies(ctx context.Context, workDir, pulumiBinDir string) ([]string, error) {
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
	log.Printf("Successfully prepared Python virtualenv at %s (PULUMI_PYTHON_CMD=%s)", venvDir, pythonBin)
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
