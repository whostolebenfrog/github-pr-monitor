# Changelog

All notable changes to PR Monitor will be documented in this file.

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
