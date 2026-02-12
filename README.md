# PR Monitor

A lightweight system tray app that monitors GitHub PRs from your colleagues that need your review. Works on macOS, Linux, and Windows.

## Features

- **Notification-driven updates** — uses GitHub's Notifications API with conditional requests (`If-Modified-Since`) so idle polls are free (304 Not Modified, no rate limit consumed)
- **SQLite persistence** — PR state is cached in a local database so the menu populates instantly on restart
- **Fallback full refresh** — periodic full scan (default 30min) catches anything notifications miss
- **Smart recheck after opening** — when you open a PR, it's rechecked on an escalating schedule (1min/2min/5min) for up to an hour so it disappears quickly once reviewed
- Filters PRs by specified authors (your colleagues)
- Detects PRs that need review (no approvals yet)
- Detects PRs that need re-approval (new commits after approval)
- Automatically skips draft PRs
- White system tray icon with red notification dot when PRs need attention
- Shows PR count next to icon
- Click any PR to open in browser
- Ignore PRs you don't want to review (persisted in database)
- Review with Claude — clone the PR and launch an interactive Claude Code review session
- Per-organization GitHub token support for fine-grained access
- Graceful degradation — falls back to periodic polling if the token lacks `notifications` scope
- One-time notification cleanup on first run (marks all existing notifications as read)

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
   - Add your GitHub personal access token (needs `repo` and `notifications` scopes)
   - List the repositories to monitor
   - List the GitHub usernames of colleagues whose PRs you want to track

### Getting a GitHub Token

PR Monitor needs a GitHub token with these scopes:
- **`repo`** — access to private repository PRs, reviews, and commits
- **`notifications`** — for notification-driven polling (optional, falls back to periodic polling without it)

#### Classic personal access token

1. Go to https://github.com/settings/tokens
2. Click "Generate new token (classic)"
3. Give it a name like "PR Monitor"
4. Select scopes: `repo`, `notifications`
5. Click "Generate token" and copy it to your config file
6. If your org uses SSO, click "Configure SSO" and authorize the token

## Usage

Simply run:

```bash
./pr-monitor
```

The app will appear in your system tray with a PR icon. When PRs need attention, a count appears next to the icon.

### How Polling Works

1. **Startup** — loads cached PRs from SQLite for instant display, then does a full refresh
2. **Notification polling** (~60s) — checks GitHub's notifications endpoint. Returns 304 (free) when nothing changed. When a notification arrives for a configured repo, fetches that specific PR's details and updates the database.
3. **Full refresh** (every 30min) — scans all configured repos as a safety net for anything notifications missed
4. **Recheck after open** — when you click a PR to open in browser, it's rechecked on a schedule (10x at 1min, 10x at 2min, 6x at 5min) so it disappears quickly once you've reviewed it. This schedule persists across restarts.
5. All notification threads are marked as read to keep the `If-Modified-Since` mechanism working

### System Tray Icon

The app displays a white merge/PR icon in your system tray, designed for visibility on dark menu bars.

**Visual indicators:**
- **No PRs waiting** - White PR icon only
- **PRs need attention** - White PR icon with red notification dot + count

**Menu items:**
- **Refresh Now** - Manually refresh the PR list
- **PR List** - Shows PRs needing review (click to expand submenu)
  - **Open in Browser** - Opens the PR in your default browser
  - **Ignore** - Hides this PR from the list
  - **Review with Claude** - Clones the repo into a temp directory, checks out the PR branch, and opens a Terminal window with Claude Code pre-loaded with a review prompt. Requires `gh` and `claude` on your PATH. (macOS only)
- **Clear Ignored PRs (N)** - Shows count; requires confirmation click to clear
- **Quit** - Exit PR Monitor

**Tooltip** - Hover over the icon to see count details including ignored PRs.

### Data Storage

All persistent state is stored in `~/.config/pr-monitor/`:
- `config.yaml` — configuration
- `pr-monitor.db` — SQLite database (PR cache, ignored PRs, notification state, recheck queue)

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
# GitHub token (needs 'repo' and 'notifications' scopes)
github_token: "ghp_your_token_here"

# Per-organization tokens (optional)
# These take precedence over github_token for matching orgs
org_tokens:
  myorg: "ghp_token_for_myorg"

# Only show PRs created within the last N days (default: 3)
max_age_days: 3

# Repositories to monitor (owner/repo format)
repos:
  - "myorg/critical-service"
  - "myorg/api"
  - "anotherorg/docs"

# How often to do a full refresh as a safety net (default: 30m)
# full_refresh_interval: 30m

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

**Multiple orgs with different tokens:**
```yaml
org_tokens:
  mycompany: "ghp_xxx"
  opensource: "ghp_yyy"
```

**Mixed (org-specific + fallback):**
```yaml
github_token: "ghp_classic_fallback"  # Used for any org not in org_tokens
org_tokens:
  private-org: "ghp_xxx"  # Token for specific org
```
