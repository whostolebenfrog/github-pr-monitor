# PR Monitor

A lightweight system tray app that monitors GitHub PRs from your colleagues that need your review. Works on macOS, Linux, and Windows.

## Features

- Polls multiple GitHub repositories for open PRs
- Filters PRs by specified authors (your colleagues)
- Detects PRs that need review (no approvals yet)
- Detects PRs that need re-approval (new commits after approval)
- Automatically skips draft PRs
- Shows PR count in system tray with icon
- Click any PR to open in browser
- Ignore PRs you don't want to review (persisted across restarts)
- Per-organization GitHub token support for fine-grained access

## Installation

### Build from source

```bash
go build -o pr-monitor .
```

### Install to PATH

```bash
go install .
```

## Configuration

1. Create the config directory:
   ```bash
   mkdir -p ~/.config/pr-monitor
   ```

2. Copy and edit the example config:
   ```bash
   cp config.example.yaml ~/.config/pr-monitor/config.yaml
   ```

3. Edit `~/.config/pr-monitor/config.yaml`:
   - Add your GitHub personal access token (needs `repo` scope)
   - List the repositories to monitor
   - List the GitHub usernames of colleagues whose PRs you want to track

### Getting a GitHub Token

PR Monitor needs a GitHub token to access the API. The app reads:
- Pull request metadata (title, author, URL)
- Pull request reviews (to check approval status)
- Pull request commits (to detect new commits after approval)

#### Option 1: Fine-grained personal access token (Recommended)

1. Go to https://github.com/settings/tokens?type=beta
2. Click "Generate new token"
3. Give it a name like "PR Monitor"
4. Set expiration as desired
5. Under "Repository access", select the repos you want to monitor
6. Under "Permissions" → "Repository permissions":
   - **Pull requests**: Read-only
   - **Metadata**: Read-only (automatically selected)
7. Click "Generate token" and copy it to your config file

#### Option 2: Classic personal access token

1. Go to https://github.com/settings/tokens
2. Click "Generate new token (classic)"
3. Give it a name like "PR Monitor"
4. Select scopes:
   - `repo` - for private repositories (grants full access)
   - `public_repo` - for public repositories only
5. Click "Generate token" and copy it to your config file

**Note**: Fine-grained tokens are more secure as they limit access to specific repositories with read-only permissions.

## Usage

Simply run:

```bash
./pr-monitor
```

The app will appear in your system tray with a PR icon. When PRs need attention, a count appears next to the icon.

### System Tray

- **Icon** - Shows a merge/PR symbol
- **Count** - Number appears next to icon when PRs need attention (hidden when zero)
- **Tooltip** - Hover to see count details (includes ignored count)
- **PR List** - Click to see list of PRs needing review
- Each PR has a submenu with:
  - **Open in Browser** - Opens the PR in your default browser
  - **Ignore** - Hides this PR from the list
- **Refresh Now** - Manually refresh the PR list
- **Clear Ignored PRs** - Restore all previously ignored PRs
- **Quit** - Exit PR Monitor

Ignored PRs are stored in `~/.config/pr-monitor/ignored.json` and persist across restarts.

## Running at Login

### macOS

1. Open System Preferences > Users & Groups
2. Select your user and click "Login Items"
3. Click "+" and add the pr-monitor binary

Or create a LaunchAgent plist in `~/Library/LaunchAgents/`.

### Linux

Add to your desktop environment's autostart, or create a systemd user service.

### Windows

Add a shortcut to `pr-monitor.exe` in your Startup folder (`shell:startup`).

## Configuration Options

```yaml
# Default GitHub token (fallback for orgs without specific tokens)
github_token: "ghp_your_token_here"

# Per-organization tokens (optional)
# Use this when you have fine-grained tokens scoped to specific orgs
# These take precedence over github_token for matching orgs
org_tokens:
  myorg: "ghp_token_for_myorg"
  anotherorg: "ghp_token_for_anotherorg"

# How often to check for new PRs (Go duration format: 1m, 5m, 30s, etc.)
poll_interval: 5m

# Only show PRs created within the last N days (default: 3)
max_age_days: 3

# Repositories to monitor (owner/repo format)
repos:
  - "myorg/repo1"
  - "myorg/repo2"
  - "anotherorg/project"

# GitHub usernames whose PRs you want to review
authors:
  - "colleague1"
  - "colleague2"
```

### Token Configuration Examples

**Single token (classic PAT with broad access):**
```yaml
github_token: "ghp_classic_token_with_repo_scope"
```

**Multiple orgs with fine-grained tokens:**
```yaml
org_tokens:
  mycompany: "github_pat_xxx"  # Fine-grained token for mycompany org
  opensource: "github_pat_yyy" # Fine-grained token for opensource org
```

**Mixed (fine-grained for some orgs, classic fallback for others):**
```yaml
github_token: "ghp_classic_fallback"  # Used for any org not in org_tokens
org_tokens:
  private-org: "github_pat_xxx"  # Fine-grained token for specific org
```
