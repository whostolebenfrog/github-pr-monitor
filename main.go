package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/systray"
	"github.com/google/go-github/v57/github"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"
)

const maxMenuItems = 20

type Config struct {
	GitHubToken         string            `yaml:"github_token"`
	OrgTokens           map[string]string `yaml:"org_tokens"`
	MaxAgeDays          int               `yaml:"max_age_days"`
	Repos               []string          `yaml:"repos"`
	Authors             []string          `yaml:"authors"`
	FullRefreshInterval time.Duration     `yaml:"full_refresh_interval"`
}

type PRInfo struct {
	Repo            string
	Number          int
	Title           string
	Author          string
	URL             string
	NeedsReview     bool
	NeedsReapproval bool
}

func (pr PRInfo) Key() string {
	return fmt.Sprintf("%s#%d", pr.Repo, pr.Number)
}

type PRMenuItem struct {
	parent *systray.MenuItem
	open   *systray.MenuItem
	ignore *systray.MenuItem
	review *systray.MenuItem
}

var (
	config        Config
	configDir     string
	defaultClient *github.Client
	orgClients    map[string]*github.Client
	prs           []PRInfo
	prsMutex      sync.RWMutex
	menuItems     []PRMenuItem
	mClearIgnored *systray.MenuItem
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get home directory: %v", err)
	}
	configDir = filepath.Join(home, ".config", "pr-monitor")

	if err := loadConfig(); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if err := openDB(); err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	if err := importIgnoredJSON(); err != nil {
		log.Printf("Warning: Failed to import ignored.json: %v", err)
	}

	initClients()

	// Load cached PRs from DB for instant startup
	if cached, err := dbLoadActivePRs(); err == nil && len(cached) > 0 {
		prsMutex.Lock()
		prs = cached
		prsMutex.Unlock()
		log.Printf("Loaded %d cached PRs from database", len(cached))
	}

	systray.Run(onReady, onExit)
}

func loadConfig() error {
	configPath := filepath.Join(configDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("config file not found at %s: %w", configPath, err)
	}

	// Try to detect old map-style repos config
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err == nil {
		if repos, ok := raw["repos"]; ok {
			if _, isMap := repos.(map[string]any); isMap {
				return fmt.Errorf("config uses old map-style repos format. Please migrate to a flat list:\n\n  repos:\n    - owner/repo1\n    - owner/repo2\n\nSee config.example.yaml for the new format")
			}
		}
	}

	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}

	if config.MaxAgeDays == 0 {
		config.MaxAgeDays = 3
	}

	if len(config.Repos) == 0 {
		return fmt.Errorf("no repositories configured")
	}

	if len(config.Authors) == 0 {
		return fmt.Errorf("no authors configured")
	}

	for _, repo := range config.Repos {
		if !strings.Contains(repo, "/") {
			return fmt.Errorf("invalid repo format %q: expected owner/repo", repo)
		}
	}

	return nil
}

func initClients() {
	ctx := context.Background()
	orgClients = make(map[string]*github.Client)

	if config.GitHubToken != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: config.GitHubToken})
		tc := oauth2.NewClient(ctx, ts)
		defaultClient = github.NewClient(tc)
	}

	for org, token := range config.OrgTokens {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		tc := oauth2.NewClient(ctx, ts)
		orgClients[org] = github.NewClient(tc)
	}

	if defaultClient == nil && len(orgClients) == 0 {
		log.Fatal("No GitHub tokens configured. Set github_token or org_tokens in config.")
	}
}

func getClientForOrg(org string) *github.Client {
	if client, ok := orgClients[org]; ok {
		return client
	}
	if defaultClient != nil {
		return defaultClient
	}
	log.Printf("Warning: No client available for org %s", org)
	return nil
}

func ignorePR(key string) {
	repo, number := parsePRKey(key)
	if repo != "" && number > 0 {
		if err := dbIgnorePR(repo, number); err != nil {
			log.Printf("Error ignoring PR %s: %v", key, err)
		}
	}

	prsMutex.Lock()
	filtered := make([]PRInfo, 0, len(prs))
	for _, pr := range prs {
		if pr.Key() != key {
			filtered = append(filtered, pr)
		}
	}
	prs = filtered
	prsMutex.Unlock()

	updateMenu()
}

func clearIgnored() {
	if err := dbClearIgnored(); err != nil {
		log.Printf("Error clearing ignored PRs: %v", err)
	}

	go refreshAllRepos()
}

func onReady() {
	systray.SetIcon(getIcon(false))
	systray.SetTitle("")

	prsMutex.RLock()
	hasCached := len(prs) > 0
	prsMutex.RUnlock()
	if !hasCached {
		systray.SetTooltip("PR Monitor - Loading...")
	}

	mRefresh := systray.AddMenuItem("Refresh Now", "Check all repos now")
	systray.AddSeparator()

	for i := 0; i < maxMenuItems; i++ {
		parent := systray.AddMenuItem("", "")
		open := parent.AddSubMenuItem("Open in Browser", "Open this PR in your browser")
		ignore := parent.AddSubMenuItem("Ignore", "Hide this PR from the list")
		review := parent.AddSubMenuItem("Review with Claude", "Clone and review this PR with Claude Code")
		parent.Hide()
		menuItems = append(menuItems, PRMenuItem{parent: parent, open: open, ignore: ignore, review: review})
	}

	systray.AddSeparator()
	mClearIgnored = systray.AddMenuItem("Clear Ignored PRs", "Show all previously ignored PRs again")
	mClearConfirm := mClearIgnored.AddSubMenuItem("Yes, clear all ignored PRs", "This cannot be undone")
	mClearIgnored.Hide()
	mQuit := systray.AddMenuItem("Quit", "Quit PR Monitor")

	// If cached PRs were loaded, update the menu items now that they exist
	if hasCached {
		updateMenu()
	}

	// Choose polling strategy based on notification access
	if validateNotificationAccess() {
		log.Println("Notification access confirmed — using notification-driven polling")
		go notificationLoop()
		go fullRefreshLoop()
	} else {
		log.Println("Notification access unavailable — falling back to periodic polling")
		go legacySchedulerLoop()
	}

	resumeRechecks()

	go func() {
		for {
			select {
			case <-mRefresh.ClickedCh:
				go refreshAllRepos()
			case <-mClearConfirm.ClickedCh:
				clearIgnored()
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()

	for i, item := range menuItems {
		go handlePRMenuClicks(i, item)
	}
}

func handlePRMenuClicks(index int, item PRMenuItem) {
	for {
		select {
		case <-item.parent.ClickedCh:
			prsMutex.RLock()
			if index < len(prs) {
				pr := prs[index]
				openURL(pr.URL)
				go scheduleRecheck(pr)
			}
			prsMutex.RUnlock()
		case <-item.open.ClickedCh:
			prsMutex.RLock()
			if index < len(prs) {
				pr := prs[index]
				openURL(pr.URL)
				go scheduleRecheck(pr)
			}
			prsMutex.RUnlock()
		case <-item.ignore.ClickedCh:
			prsMutex.RLock()
			var key string
			if index < len(prs) {
				key = prs[index].Key()
			}
			prsMutex.RUnlock()
			if key != "" {
				ignorePR(key)
			}
		case <-item.review.ClickedCh:
			prsMutex.RLock()
			var pr PRInfo
			if index < len(prs) {
				pr = prs[index]
			}
			prsMutex.RUnlock()
			if pr.Repo != "" {
				go reviewPR(pr)
			}
		}
	}
}

func onExit() {
	if db != nil {
		db.Close()
	}
}

var recheckSchedule []time.Duration

func init() {
	for range 10 {
		recheckSchedule = append(recheckSchedule, 1*time.Minute)
	}
	for range 10 {
		recheckSchedule = append(recheckSchedule, 2*time.Minute)
	}
	for range 6 {
		recheckSchedule = append(recheckSchedule, 5*time.Minute)
	}
}

// recheckTotalDuration is how long the full schedule runs from start
func recheckTotalDuration() time.Duration {
	var total time.Duration
	for _, d := range recheckSchedule {
		total += d
	}
	return total
}

// activeRechecks tracks running goroutines so we don't double-up on the same PR
var activeRechecks sync.Map

// scheduleRecheck persists a recheck request and starts polling a single PR
// on an escalating schedule so the menu updates quickly after the user reviews it.
func scheduleRecheck(pr PRInfo) {
	key := pr.Key()

	if err := dbAddRecheck(pr.Repo, pr.Number); err != nil {
		log.Printf("Error scheduling recheck for %s: %v", key, err)
		return
	}

	startRecheck(pr.Repo, pr.Number, time.Now())
}

// startRecheck begins (or resumes) the recheck loop for a single PR.
func startRecheck(repo string, number int, startedAt time.Time) {
	key := fmt.Sprintf("%s#%d", repo, number)

	// Don't start a second goroutine for the same PR
	if _, loaded := activeRechecks.LoadOrStore(key, true); loaded {
		return
	}

	go func() {
		defer activeRechecks.Delete(key)
		defer dbRemoveRecheck(repo, number)

		runRecheckLoop(repo, number, startedAt)
	}()
}

func runRecheckLoop(repo string, number int, startedAt time.Time) {
	owner, repoName := parseRepo(repo)
	client := getClientForOrg(owner)
	if client == nil {
		return
	}

	ctx := context.Background()
	authorSet := make(map[string]bool)
	for _, a := range config.Authors {
		authorSet[a] = true
	}

	// Compute the absolute time of each check
	elapsed := time.Since(startedAt)
	var cumulative time.Duration
	didCatchUp := false

	for _, interval := range recheckSchedule {
		cumulative += interval

		if cumulative <= elapsed {
			// We've already passed this check time (e.g. after restart)
			// Do one catch-up poll for the last missed check
			if !didCatchUp && cumulative+interval > elapsed {
				didCatchUp = true
				if recheckPR(ctx, client, owner, repoName, repo, number, authorSet) {
					return
				}
			}
			continue
		}

		// Wait until this check is due
		waitTime := cumulative - elapsed
		time.Sleep(waitTime)
		elapsed = time.Since(startedAt)

		if recheckPR(ctx, client, owner, repoName, repo, number, authorSet) {
			return
		}
	}
}

// recheckPR checks a single PR's status. Returns true if the recheck loop should stop.
func recheckPR(ctx context.Context, client *github.Client, owner, repoName, repo string, number int, authorSet map[string]bool) bool {
	if dbIsIgnored(repo, number) {
		return true
	}

	ghPR, _, err := client.PullRequests.Get(ctx, owner, repoName, number)
	if err != nil {
		log.Printf("Recheck: error fetching %s#%d: %v", repo, number, err)
		return false
	}

	if ghPR.GetState() != "open" || ghPR.GetDraft() || !authorSet[ghPR.GetUser().GetLogin()] {
		dbRemovePR(repo, number)
		reloadPRsFromDB()
		return true
	}

	needsReview, needsReapproval := checkReviewStatus(ctx, client, owner, repoName, ghPR)
	if !needsReview && !needsReapproval {
		dbRemovePR(repo, number)
		reloadPRsFromDB()
		return true
	}

	dbSavePR(PRInfo{
		Repo:            repo,
		Number:          ghPR.GetNumber(),
		Title:           ghPR.GetTitle(),
		Author:          ghPR.GetUser().GetLogin(),
		URL:             ghPR.GetHTMLURL(),
		NeedsReview:     needsReview,
		NeedsReapproval: needsReapproval,
	})
	reloadPRsFromDB()
	return false
}

// resumeRechecks loads pending rechecks from the DB and resumes them.
func resumeRechecks() {
	entries, err := dbLoadRechecks()
	if err != nil {
		log.Printf("Error loading rechecks: %v", err)
		return
	}

	now := time.Now()
	for _, e := range entries {
		if now.Sub(e.StartedAt) > recheckTotalDuration() {
			// Schedule fully expired — do one final check then clean up
			go func(e recheckEntry) {
				defer dbRemoveRecheck(e.Repo, e.Number)

				owner, repoName := parseRepo(e.Repo)
				client := getClientForOrg(owner)
				if client == nil {
					return
				}
				authorSet := make(map[string]bool)
				for _, a := range config.Authors {
					authorSet[a] = true
				}
				recheckPR(context.Background(), client, owner, repoName, e.Repo, e.Number, authorSet)
			}(e)
		} else {
			startRecheck(e.Repo, e.Number, e.StartedAt)
		}
	}

	if len(entries) > 0 {
		log.Printf("Resumed %d pending rechecks", len(entries))
	}
}

func refreshAllRepos() {
	refreshRepos(config.Repos)
}

func refreshRepos(repos []string) {
	ctx := context.Background()

	authorSet := make(map[string]bool)
	for _, a := range config.Authors {
		authorSet[a] = true
	}

	maxAge := time.Duration(config.MaxAgeDays) * 24 * time.Hour
	cutoff := time.Now().Add(-maxAge)

	var newPRsFromRepos []PRInfo
	repoSet := make(map[string]bool)
	for _, repo := range repos {
		repoSet[repo] = true
		repoPRs := fetchRepoPRs(ctx, repo, authorSet, cutoff)
		newPRsFromRepos = append(newPRsFromRepos, repoPRs...)
	}

	// Persist to DB: clear old active PRs for refreshed repos, save new ones
	for _, repo := range repos {
		if err := dbRemoveRepoActivePRs(repo); err != nil {
			log.Printf("Error clearing DB PRs for %s: %v", repo, err)
		}
	}
	for _, pr := range newPRsFromRepos {
		if err := dbSavePR(pr); err != nil {
			log.Printf("Error saving PR %s#%d to DB: %v", pr.Repo, pr.Number, err)
		}
	}

	// Merge with existing PRs from repos we didn't refresh
	prsMutex.Lock()
	var mergedPRs []PRInfo
	for _, pr := range prs {
		if !repoSet[pr.Repo] {
			mergedPRs = append(mergedPRs, pr)
		}
	}
	mergedPRs = append(mergedPRs, newPRsFromRepos...)

	sort.Slice(mergedPRs, func(i, j int) bool {
		if mergedPRs[i].Repo != mergedPRs[j].Repo {
			return mergedPRs[i].Repo < mergedPRs[j].Repo
		}
		return mergedPRs[i].Number < mergedPRs[j].Number
	})

	prs = mergedPRs
	prsMutex.Unlock()

	updateMenu()
}

func fetchRepoPRs(ctx context.Context, repo string, authorSet map[string]bool, cutoff time.Time) []PRInfo {
	var result []PRInfo

	owner, repoName := parseRepo(repo)
	if owner == "" {
		return result
	}

	client := getClientForOrg(owner)
	if client == nil {
		log.Printf("No client available for %s", repo)
		return result
	}

	pulls, _, err := client.PullRequests.List(ctx, owner, repoName, &github.PullRequestListOptions{
		State:       "open",
		ListOptions: github.ListOptions{PerPage: 100},
	})
	if err != nil {
		log.Printf("Error fetching PRs for %s: %v", repo, err)
		return result
	}

	for _, pr := range pulls {
		author := pr.GetUser().GetLogin()
		if !authorSet[author] {
			continue
		}

		if pr.GetCreatedAt().Before(cutoff) {
			continue
		}

		if pr.GetDraft() {
			continue
		}

		if dbIsIgnored(repo, pr.GetNumber()) {
			continue
		}

		needsReview, needsReapproval := checkReviewStatus(ctx, client, owner, repoName, pr)
		if needsReview || needsReapproval {
			result = append(result, PRInfo{
				Repo:            repo,
				Number:          pr.GetNumber(),
				Title:           pr.GetTitle(),
				Author:          author,
				URL:             pr.GetHTMLURL(),
				NeedsReview:     needsReview,
				NeedsReapproval: needsReapproval,
			})
		}
	}

	return result
}

func checkReviewStatus(ctx context.Context, client *github.Client, owner, repo string, pr *github.PullRequest) (needsReview, needsReapproval bool) {
	reviews, _, err := client.PullRequests.ListReviews(ctx, owner, repo, pr.GetNumber(), &github.ListOptions{PerPage: 100})
	if err != nil {
		log.Printf("Error fetching reviews for %s#%d: %v", repo, pr.GetNumber(), err)
		return true, false
	}

	if len(reviews) == 0 {
		return true, false
	}

	latestReviews := make(map[string]*github.PullRequestReview)
	for _, review := range reviews {
		user := review.GetUser().GetLogin()
		existing, ok := latestReviews[user]
		if !ok || review.GetSubmittedAt().After(existing.GetSubmittedAt().Time) {
			latestReviews[user] = review
		}
	}

	var hasApproval bool
	var latestApprovalTime time.Time
	for _, review := range latestReviews {
		if review.GetState() == "APPROVED" {
			hasApproval = true
			if review.GetSubmittedAt().After(latestApprovalTime) {
				latestApprovalTime = review.GetSubmittedAt().Time
			}
		}
	}

	if !hasApproval {
		return true, false
	}

	commits, _, err := client.PullRequests.ListCommits(ctx, owner, repo, pr.GetNumber(), &github.ListOptions{PerPage: 100})
	if err != nil {
		log.Printf("Error fetching commits for %s#%d: %v", repo, pr.GetNumber(), err)
		return false, false
	}

	for _, commit := range commits {
		commitDate := commit.GetCommit().GetCommitter().GetDate()
		if commitDate.After(latestApprovalTime) {
			return false, true
		}
	}

	return false, false
}

func parseRepo(repo string) (owner, name string) {
	owner, name, _ = strings.Cut(repo, "/")
	return owner, name
}

func updateMenu() {
	prsMutex.RLock()
	defer prsMutex.RUnlock()

	count := len(prs)
	ignored := dbIgnoredCount()

	systray.SetIcon(getIcon(count > 0))

	if count == 0 {
		systray.SetTitle("")
		if ignored > 0 {
			systray.SetTooltip(fmt.Sprintf("No PRs need attention (%d ignored)", ignored))
		} else {
			systray.SetTooltip("No PRs need your attention")
		}
	} else {
		systray.SetTitle(fmt.Sprintf("%d", count))
		if ignored > 0 {
			systray.SetTooltip(fmt.Sprintf("%d PRs need attention (%d ignored)", count, ignored))
		} else {
			systray.SetTooltip(fmt.Sprintf("%d PRs need your attention", count))
		}
	}

	if ignored > 0 {
		mClearIgnored.SetTitle(fmt.Sprintf("Clear Ignored PRs (%d)", ignored))
		mClearIgnored.Show()
	} else {
		mClearIgnored.Hide()
	}

	for i, item := range menuItems {
		if i < len(prs) {
			pr := prs[i]
			status := "needs review"
			if pr.NeedsReapproval {
				status = "needs re-approval"
			}
			item.parent.SetTitle(fmt.Sprintf("[%s] #%d: %s (%s)", pr.Repo, pr.Number, truncate(pr.Title, 40), status))
			item.parent.SetTooltip(fmt.Sprintf("%s by @%s", pr.Title, pr.Author))
			item.parent.Show()
		} else {
			item.parent.Hide()
		}
	}
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}

func reviewPR(pr PRInfo) {
	if runtime.GOOS != "darwin" {
		log.Printf("Review with Claude is currently only supported on macOS")
		return
	}

	owner, repo := parseRepo(pr.Repo)
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("pr-review-%s-%s-%d-*", owner, repo, pr.Number))
	if err != nil {
		log.Printf("Failed to create temp dir for review: %v", err)
		return
	}

	status := "needs review"
	if pr.NeedsReapproval {
		status = "needs re-approval"
	}

	script := fmt.Sprintf(`#!/bin/bash
set -e

REPO='%s'
PR_NUM=%d
AUTHOR='%s'
STATUS='%s'
DIR='%s'

echo "==> Cloning $REPO (blobless for speed)..."
gh repo clone "$REPO" "$DIR/repo" -- --filter=blob:none
cd "$DIR/repo"

echo "==> Checking out PR #$PR_NUM..."
gh pr checkout "$PR_NUM"

echo "==> Gathering PR context..."
PR_JSON=$(gh pr view "$PR_NUM" --json title,body,baseRefName,headRefName,commits)
PR_TITLE=$(echo "$PR_JSON" | jq -r '.title')
PR_BODY=$(echo "$PR_JSON" | jq -r '.body')
BASE_REF=$(echo "$PR_JSON" | jq -r '.baseRefName')
HEAD_REF=$(echo "$PR_JSON" | jq -r '.headRefName')
NUM_COMMITS=$(echo "$PR_JSON" | jq '.commits | length')

DIFF_STAT=$(gh pr diff "$PR_NUM" --stat 2>/dev/null || echo "(could not fetch diff stat)")
COMMIT_LOG=$(git log --oneline "$BASE_REF..$HEAD_REF" 2>/dev/null || echo "(could not fetch commit log)")

echo "==> Building review prompt..."
cat > "$DIR/prompt.md" <<PROMPT_EOF
You are reviewing a pull request. Here is the context:

Repository: $REPO
PR #$PR_NUM: $PR_TITLE
Author: @$AUTHOR
Status: $STATUS

PR Description:
$PR_BODY

This PR contains $NUM_COMMITS commits:
$COMMIT_LOG

Files changed:
$DIFF_STAT

You are in a local checkout of this PR. The full source code is available to you.

Please review this PR by:
1. Reading the changed files to understand the full context of each change
2. Checking for correctness, bugs, edge cases, and error handling
3. Evaluating code style and consistency with the surrounding codebase
4. Looking for security issues (injection, auth, data leaks, etc.)
5. Assessing test coverage — are the changes adequately tested?
6. Noting any performance concerns

You have access to:
- The full repository source (use Read/Grep/Glob to explore)
- `+"`"+`gh`+"`"+` CLI for GitHub context (e.g. `+"`"+`gh pr view`+"`"+`, `+"`"+`gh pr checks`+"`"+`, linked issues)

Provide a structured review with:
- A summary of what the PR does
- Issues found (critical, suggestions, nits) with file:line references
- Questions for the author
- Overall assessment
PROMPT_EOF

echo "==> Launching Claude Code for review..."
echo ""
exec claude "$(cat "$DIR/prompt.md")"
`, pr.Repo, pr.Number, pr.Author, status, tempDir)

	scriptPath := filepath.Join(tempDir, "review.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		log.Printf("Failed to write review script: %v", err)
		return
	}

	openTerminalWithCommand(scriptPath)
}

func openTerminalWithCommand(scriptPath string) {
	appleScript := fmt.Sprintf(`tell app "Terminal"
	activate
	do script "exec bash '%s'"
end tell`, scriptPath)
	cmd := exec.Command("osascript", "-e", appleScript)
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open terminal: %v", err)
		return
	}
	go cmd.Wait()
}

func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		log.Printf("Unsupported platform for opening URLs: %s", runtime.GOOS)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open URL %s: %v", url, err)
		return
	}
	go cmd.Wait()
}
