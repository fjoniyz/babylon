# 🏗️ Babylon

**Babylon** is a lightweight, self-hosted GitOps automation server for **Pulumi** that enables teams to preview, apply, and destroy infrastructure changes directly from GitHub Pull Request comments.

---

## ⚡ Capabilities

- **PR Comment-Driven Infrastructure Operations**:
  - `pulumi preview`: Runs a preview against the PR branch and posts the resource diff as a comment.
  - `pulumi up`: Executes and auto-approves infrastructure updates non-interactively.
  - `pulumi destroy`: Tears down infrastructure resources non-interactively with auto-approval.

- **GitHub App Native Integration**:
  - Authenticates dynamically per repository installation using RSA private keys and short-lived tokens.
  - Posts comments under a verified bot identity (`your-app[bot]`).
  - Securely clones private and public repositories using on-demand installation tokens.
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
