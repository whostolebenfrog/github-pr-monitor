# Changelog

All notable changes to PR Monitor will be documented in this file.

## [0.2.0] - 2025-01-20

### Added
- Priority-based polling: repos can be assigned high, medium, or low priority
- Configurable poll intervals per priority level (defaults: 2m, 15m, 2h)
- Jittered poll times (±20%) to avoid thundering herd and API rate limit spikes
- Staggered initial polls for repos in the same priority level

### Changed
- Config format: `repos` is now grouped by priority (`high`, `medium`, `low`)
- Config format: `poll_interval` replaced by `poll_intervals` with per-priority durations
- Larger red notification dot for better visibility

## [0.1.0] - 2025-01-19

### Added
- Initial release
- Monitor multiple GitHub repositories for open PRs from specified authors
- Detect PRs needing review (no approvals) or re-approval (new commits after approval)
- Automatically skip draft PRs
- System tray icon with white merge/PR symbol
- Red notification dot appears when PRs need attention
- PR count displayed next to icon
- Click PRs to open in browser
- Ignore PRs with confirmation prompt to clear
- Per-organization GitHub token support for fine-grained access tokens
- Configurable poll interval and PR age filter
- Cross-platform support (macOS, Linux, Windows)
- Persistent ignored PR list stored in `~/.config/pr-monitor/ignored.json`

### Technical
- Built with Go using getlantern/systray for system tray integration
- Uses google/go-github for GitHub API access
- Configuration via YAML file at `~/.config/pr-monitor/config.yaml`
