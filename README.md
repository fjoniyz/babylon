# Babylon

**Babylon** is a lightweight, self-hosted GitOps automation server for **Pulumi** that enables teams to preview, apply, and destroy infrastructure changes directly from GitHub Pull Request comments.

---

## Capabilities

- **PR Comment-Driven Infrastructure Operations**:
  - `pulumi preview [--stack <name>]`: Runs a preview against the PR branch, acquires an S3 lock, and posts the resource diff.
  - `pulumi up [--stack <name>]`: Verifies active preview & commit SHA, auto-approves (`--yes`), and applies infrastructure updates.
  - `pulumi destroy [--stack <name>]`: Tears down infrastructure resources non-interactively and releases the S3 lock.
  - `pulumi unlock [--stack <name>]`: Manually clears the S3 stack lock and removes the local workspace directory.

- **S3-Based Stack Locking & Concurrency Control**:
  - Stores stack locks as JSON in S3 (`s3://<STATE_BUCKET>/.pulumi/locks/<owner>/<repo>/<stack>.lock.json`).
  - **Cross-PR Concurrency Protection**: Prevents multiple PRs from modifying the same stack simultaneously.
  - **Commit SHA Verification**: Blocks `pulumi up` if new commits were pushed after running `preview`.
  - **Zero Database Infrastructure**: Eliminates external databases by leveraging the existing S3 state storage bucket.
  - **Automatic PR Close Cleanup**: Automatically cleans up all S3 locks and workspace files when a PR is merged or closed.

- **GitHub App Native Integration**:
  - Authenticates dynamically per repository installation using RSA private keys and short-lived tokens.
  - Posts comments under a verified bot identity (`your-app[bot]`).
  - Securely clones and fetches private repositories using on-demand installation tokens.
  - Automatically filters out bot-generated comments to prevent recursive event loops.

- **Automatic Project Discovery**:
  - Recursively scans the cloned workspace to discover the active Pulumi project directory containing `Pulumi.yaml`.
  - Supports both root-level projects and nested directory structures without manual path configuration.

- **Automated Dependency Management**:
  - Detects Python Pulumi stacks and automatically initializes isolated `.venv` virtual environments.
  - Installs requirements (`requirements.txt`) and injects the virtualenv binary into `PULUMI_PYTHON_CMD` and `$PATH`.

- **State Backend & Passphrase Support**:
  - Works with self-managed cloud storage backends (AWS S3) and Pulumi Service.
  - Supports automated secrets decryption via `PULUMI_CONFIG_PASSPHRASE`.
