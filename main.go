package main

import (
	"context"
	"encoding/json"
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

const (
	maxMenuItems        = 20
	defaultPollInterval = 5 * time.Minute
	defaultMaxAgeDays   = 3
)

type Config struct {
	GitHubToken  string            `yaml:"github_token"`
	OrgTokens    map[string]string `yaml:"org_tokens"`
	PollInterval time.Duration     `yaml:"poll_interval"`
	MaxAgeDays   int               `yaml:"max_age_days"`
	Repos        []string          `yaml:"repos"`
	Authors      []string          `yaml:"authors"`
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
}

var (
	config         Config
	configDir      string
	defaultClient  *github.Client
	orgClients     map[string]*github.Client
	prs            []PRInfo
	prsMutex       sync.RWMutex
	menuItems      []PRMenuItem
	ignoredPRs     map[string]bool
	ignoreMutex    sync.RWMutex
	mClearIgnored  *systray.MenuItem
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

	if err := loadIgnored(); err != nil {
		log.Printf("Warning: Failed to load ignored PRs: %v", err)
		ignoredPRs = make(map[string]bool)
	}

	initClients()

	systray.Run(onReady, onExit)
}

func loadConfig() error {
	configPath := filepath.Join(configDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("config file not found at %s: %w", configPath, err)
	}

	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}

	if config.PollInterval == 0 {
		config.PollInterval = defaultPollInterval
	}

	if config.MaxAgeDays == 0 {
		config.MaxAgeDays = defaultMaxAgeDays
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

	// Create default client if token provided
	if config.GitHubToken != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: config.GitHubToken})
		tc := oauth2.NewClient(ctx, ts)
		defaultClient = github.NewClient(tc)
	}

	// Create org-specific clients
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
	// This shouldn't happen if initClients validated properly
	log.Printf("Warning: No client available for org %s", org)
	return nil
}

func ignoredFilePath() string {
	return filepath.Join(configDir, "ignored.json")
}

func loadIgnored() error {
	ignoredPRs = make(map[string]bool)

	data, err := os.ReadFile(ignoredFilePath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}

	for _, key := range keys {
		ignoredPRs[key] = true
	}
	return nil
}

func saveIgnored() error {
	ignoreMutex.RLock()
	keys := make([]string, 0, len(ignoredPRs))
	for key := range ignoredPRs {
		keys = append(keys, key)
	}
	ignoreMutex.RUnlock()

	sort.Strings(keys)
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(ignoredFilePath(), data, 0644)
}

func ignorePR(key string) {
	ignoreMutex.Lock()
	ignoredPRs[key] = true
	ignoreMutex.Unlock()

	if err := saveIgnored(); err != nil {
		log.Printf("Error saving ignored PRs: %v", err)
	}

	// Remove the PR from the list immediately
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
	ignoreMutex.Lock()
	ignoredPRs = make(map[string]bool)
	ignoreMutex.Unlock()

	if err := saveIgnored(); err != nil {
		log.Printf("Error saving ignored PRs: %v", err)
	}

	go refreshPRs()
}

func isIgnored(key string) bool {
	ignoreMutex.RLock()
	defer ignoreMutex.RUnlock()
	return ignoredPRs[key]
}

func ignoredCount() int {
	ignoreMutex.RLock()
	defer ignoreMutex.RUnlock()
	return len(ignoredPRs)
}

func onReady() {
	systray.SetIcon(getIcon(false))
	systray.SetTitle("")
	systray.SetTooltip("PR Monitor - Loading...")

	mRefresh := systray.AddMenuItem("Refresh Now", "Check for PRs now")
	systray.AddSeparator()

	// Pre-allocate menu items for PRs with submenus
	for i := 0; i < maxMenuItems; i++ {
		parent := systray.AddMenuItem("", "")
		open := parent.AddSubMenuItem("Open in Browser", "Open this PR in your browser")
		ignore := parent.AddSubMenuItem("Ignore", "Hide this PR from the list")
		parent.Hide()
		menuItems = append(menuItems, PRMenuItem{parent: parent, open: open, ignore: ignore})
	}

	systray.AddSeparator()
	mClearIgnored = systray.AddMenuItem("Clear Ignored PRs", "Show all previously ignored PRs again")
	mClearConfirm := mClearIgnored.AddSubMenuItem("Yes, clear all ignored PRs", "This cannot be undone")
	mClearIgnored.Hide()
	mQuit := systray.AddMenuItem("Quit", "Quit PR Monitor")

	// Start polling
	go pollLoop()

	// Handle menu clicks
	go func() {
		for {
			select {
			case <-mRefresh.ClickedCh:
				go refreshPRs()
			case <-mClearConfirm.ClickedCh:
				clearIgnored()
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()

	// Handle PR item clicks
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
				openURL(prs[index].URL)
			}
			prsMutex.RUnlock()
		case <-item.open.ClickedCh:
			prsMutex.RLock()
			if index < len(prs) {
				openURL(prs[index].URL)
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
		}
	}
}

func onExit() {
	// Cleanup if needed
}

func pollLoop() {
	refreshPRs()
	ticker := time.NewTicker(config.PollInterval)
	for range ticker.C {
		refreshPRs()
	}
}

func refreshPRs() {
	ctx := context.Background()
	var newPRs []PRInfo

	authorSet := make(map[string]bool)
	for _, a := range config.Authors {
		authorSet[a] = true
	}

	maxAge := time.Duration(config.MaxAgeDays) * 24 * time.Hour
	cutoff := time.Now().Add(-maxAge)

	for _, repo := range config.Repos {
		owner, repoName := parseRepo(repo)
		if owner == "" {
			continue
		}

		client := getClientForOrg(owner)
		if client == nil {
			log.Printf("No client available for %s", repo)
			continue
		}

		pulls, _, err := client.PullRequests.List(ctx, owner, repoName, &github.PullRequestListOptions{
			State:       "open",
			ListOptions: github.ListOptions{PerPage: 100},
		})
		if err != nil {
			log.Printf("Error fetching PRs for %s: %v", repo, err)
			continue
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

			needsReview, needsReapproval := checkReviewStatus(ctx, client, owner, repoName, pr)
			if needsReview || needsReapproval {
				prInfo := PRInfo{
					Repo:            repo,
					Number:          pr.GetNumber(),
					Title:           pr.GetTitle(),
					Author:          author,
					URL:             pr.GetHTMLURL(),
					NeedsReview:     needsReview,
					NeedsReapproval: needsReapproval,
				}

				if !isIgnored(prInfo.Key()) {
					newPRs = append(newPRs, prInfo)
				}
			}
		}
	}

	// Sort by repo then number
	sort.Slice(newPRs, func(i, j int) bool {
		if newPRs[i].Repo != newPRs[j].Repo {
			return newPRs[i].Repo < newPRs[j].Repo
		}
		return newPRs[i].Number < newPRs[j].Number
	})

	prsMutex.Lock()
	prs = newPRs
	prsMutex.Unlock()

	updateMenu()
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

	// Get the latest review state per user
	latestReviews := make(map[string]*github.PullRequestReview)
	for _, review := range reviews {
		user := review.GetUser().GetLogin()
		existing, ok := latestReviews[user]
		if !ok || review.GetSubmittedAt().After(existing.GetSubmittedAt().Time) {
			latestReviews[user] = review
		}
	}

	// Check if there's an approval
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

	// Check if there are commits after the latest approval
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
	ignored := ignoredCount()

	// Update icon based on whether there are PRs needing attention
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

	// Update clear ignored menu item
	if ignored > 0 {
		mClearIgnored.SetTitle(fmt.Sprintf("Clear Ignored PRs (%d)", ignored))
		mClearIgnored.Show()
	} else {
		mClearIgnored.Hide()
	}

	// Update menu items
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
