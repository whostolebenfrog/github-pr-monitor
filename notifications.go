package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v57/github"
)

const (
	defaultNotificationInterval = 60 * time.Second
	defaultFullRefreshInterval  = 30 * time.Minute
)

// notificationLoop polls GitHub's notifications API as the primary update mechanism.
// Uses If-Modified-Since to avoid consuming rate limit when nothing changed.
func notificationLoop() {
	// Run initial cleanup if needed
	if err := initialNotificationCleanup(); err != nil {
		log.Printf("Warning: initial notification cleanup failed: %v", err)
	}

	// Do an immediate full refresh to populate from all repos
	refreshAllRepos()

	pollInterval := defaultNotificationInterval
	if stored := dbGetState("notifications_poll_interval"); stored != "" {
		if secs, err := strconv.Atoi(stored); err == nil && secs > 0 {
			pollInterval = time.Duration(secs) * time.Second
		}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for range ticker.C {
		newInterval, err := pollNotifications()
		if err != nil {
			log.Printf("Notification poll error: %v", err)
			continue
		}
		if newInterval > 0 && newInterval != pollInterval {
			pollInterval = newInterval
			ticker.Reset(pollInterval)
		}
	}
}

// fullRefreshLoop runs a complete repo scan as a safety net
func fullRefreshLoop() {
	interval := defaultFullRefreshInterval
	if config.FullRefreshInterval > 0 {
		interval = config.FullRefreshInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		refreshAllRepos()
	}
}

func pollNotifications() (newInterval time.Duration, err error) {
	if defaultClient == nil {
		return 0, fmt.Errorf("no default client configured")
	}

	ctx := context.Background()

	// Build request manually to set If-Modified-Since
	req, err := defaultClient.NewRequest("GET", "notifications", nil)
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}

	if lastMod := dbGetState("notifications_last_modified"); lastMod != "" {
		req.Header.Set("If-Modified-Since", lastMod)
	}

	var notifications []*github.Notification
	resp, err := defaultClient.Do(ctx, req, &notifications)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotModified {
			return 0, nil
		}
		return 0, fmt.Errorf("fetching notifications: %w", err)
	}

	// Store Last-Modified for next conditional request
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		dbSetState("notifications_last_modified", lm)
	}

	// Respect X-Poll-Interval from GitHub
	if pi := resp.Header.Get("X-Poll-Interval"); pi != "" {
		if secs, err := strconv.Atoi(pi); err == nil && secs > 0 {
			newInterval = time.Duration(secs) * time.Second
			dbSetState("notifications_poll_interval", pi)
		}
	}

	// For paginated results, fetch remaining pages
	if resp.NextPage != 0 {
		remaining, err := fetchRemainingNotificationPages(ctx, resp.NextPage)
		if err != nil {
			log.Printf("Warning: failed to fetch remaining notification pages: %v", err)
		}
		notifications = append(notifications, remaining...)
	}

	processNotifications(ctx, notifications)
	return newInterval, nil
}

func fetchRemainingNotificationPages(ctx context.Context, startPage int) ([]*github.Notification, error) {
	var all []*github.Notification
	opts := &github.NotificationListOptions{
		ListOptions: github.ListOptions{PerPage: 50, Page: startPage},
	}

	for {
		notifications, resp, err := defaultClient.Activity.ListNotifications(ctx, opts)
		if err != nil {
			return all, err
		}
		all = append(all, notifications...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func processNotifications(ctx context.Context, notifications []*github.Notification) {
	repoSet := makeRepoSet()
	authorSet := make(map[string]bool)
	for _, a := range config.Authors {
		authorSet[a] = true
	}
	var updated bool

	for _, n := range notifications {
		if n.GetSubject().GetType() != "PullRequest" || !repoSet[n.GetRepository().GetFullName()] {
			markThreadRead(ctx, n.GetID())
			continue
		}

		repo := n.GetRepository().GetFullName()
		prNumber, err := extractPRNumber(n.GetSubject().GetURL())
		if err != nil {
			log.Printf("Warning: couldn't extract PR number from %s: %v", n.GetSubject().GetURL(), err)
			markThreadRead(ctx, n.GetID())
			continue
		}

		if dbIsIgnored(repo, prNumber) {
			markThreadRead(ctx, n.GetID())
			continue
		}

		owner, repoName := parseRepo(repo)
		client := getClientForOrg(owner)
		if client == nil {
			markThreadRead(ctx, n.GetID())
			continue
		}

		pr, _, err := client.PullRequests.Get(ctx, owner, repoName, prNumber)
		if err != nil {
			log.Printf("Error fetching PR %s#%d: %v", repo, prNumber, err)
			markThreadRead(ctx, n.GetID())
			continue
		}

		if dbIsMuted(repo, prNumber) {
			if isReviewRequestedForUser(pr) {
				log.Printf("Un-muting %s#%d: review re-requested", repo, prNumber)
				dbUnmutePR(repo, prNumber)
			} else {
				markThreadRead(ctx, n.GetID())
				continue
			}
		}

		if pr.GetState() != "open" || pr.GetDraft() || !authorSet[pr.GetUser().GetLogin()] {
			dbRemovePR(repo, prNumber)
			updated = true
			markThreadRead(ctx, n.GetID())
			continue
		}

		needsReview, needsReapproval := checkReviewStatus(ctx, client, owner, repoName, pr)
		if needsReview || needsReapproval {
			prInfo := PRInfo{
				Repo:            repo,
				Number:          pr.GetNumber(),
				Title:           pr.GetTitle(),
				Author:          pr.GetUser().GetLogin(),
				URL:             pr.GetHTMLURL(),
				NeedsReview:     needsReview,
				NeedsReapproval: needsReapproval,
			}
			if err := dbSavePR(prInfo); err != nil {
				log.Printf("Error saving PR %s#%d: %v", repo, prNumber, err)
			}
			updated = true
		} else {
			dbRemovePR(repo, prNumber)
			updated = true
		}

		markThreadRead(ctx, n.GetID())
	}

	if updated {
		reloadPRsFromDB()
	}
}

func initialNotificationCleanup() error {
	if dbGetState("initial_cleanup_done") == "true" {
		return nil
	}

	if defaultClient == nil {
		return fmt.Errorf("no default client for notification cleanup")
	}

	log.Println("Running initial notification cleanup...")
	ctx := context.Background()

	// Fetch all notifications (including read ones)
	var all []*github.Notification
	opts := &github.NotificationListOptions{
		All:         true,
		ListOptions: github.ListOptions{PerPage: 50},
	}
	for {
		notifications, resp, err := defaultClient.Activity.ListNotifications(ctx, opts)
		if err != nil {
			return fmt.Errorf("fetching all notifications: %w", err)
		}
		all = append(all, notifications...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	log.Printf("Found %d total notifications", len(all))

	// Process PR notifications for configured repos
	repoSet := makeRepoSet()
	var prCount int
	for _, n := range all {
		if n.GetSubject().GetType() != "PullRequest" {
			continue
		}
		repo := n.GetRepository().GetFullName()
		if !repoSet[repo] {
			continue
		}
		prCount++
	}
	log.Printf("Found %d PR notifications for configured repos", prCount)

	// Mark all notifications as read
	now := time.Now()
	ts := github.Timestamp{Time: now}
	_, err := defaultClient.Activity.MarkNotificationsRead(ctx, ts)
	if err != nil {
		log.Printf("Warning: failed to mark all notifications as read: %v", err)
	} else {
		log.Println("Marked all notifications as read")
	}

	return dbSetState("initial_cleanup_done", "true")
}

// reloadPRsFromDB refreshes the in-memory PR list from the database
func reloadPRsFromDB() {
	dbPRs, err := dbLoadActivePRs()
	if err != nil {
		log.Printf("Error loading PRs from DB: %v", err)
		return
	}

	prsMutex.Lock()
	prs = dbPRs
	prsMutex.Unlock()

	updateMenu()
}

func makeRepoSet() map[string]bool {
	set := make(map[string]bool)
	for _, repo := range config.Repos {
		set[repo] = true
	}
	return set
}

func extractPRNumber(apiURL string) (int, error) {
	// Format: https://api.github.com/repos/owner/repo/pulls/123
	parts := strings.Split(apiURL, "/")
	if len(parts) < 2 {
		return 0, fmt.Errorf("unexpected URL format: %s", apiURL)
	}
	return strconv.Atoi(parts[len(parts)-1])
}

func markThreadRead(ctx context.Context, threadID string) {
	if _, err := defaultClient.Activity.MarkThreadRead(ctx, threadID); err != nil {
		log.Printf("Warning: failed to mark thread %s as read: %v", threadID, err)
	}
}

// validateNotificationAccess checks if the token has notification scope
func validateNotificationAccess() bool {
	if defaultClient == nil {
		return false
	}

	ctx := context.Background()
	opts := &github.NotificationListOptions{
		ListOptions: github.ListOptions{PerPage: 1},
	}
	_, resp, err := defaultClient.Activity.ListNotifications(ctx, opts)
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) {
			log.Println("GitHub token needs 'notifications' scope. Update your token at https://github.com/settings/tokens")
			return false
		}
		log.Printf("Warning: notification access check failed: %v", err)
		return false
	}

	// Store initial poll interval from response
	if pi := resp.Header.Get("X-Poll-Interval"); pi != "" {
		dbSetState("notifications_poll_interval", pi)
	}

	return true
}

// legacySchedulerLoop is the fallback when notifications aren't available
func legacySchedulerLoop() {
	refreshAllRepos()

	interval := defaultFullRefreshInterval
	if config.FullRefreshInterval > 0 {
		interval = config.FullRefreshInterval
	}

	ticker := time.NewTicker(interval)
	for range ticker.C {
		refreshAllRepos()
	}
}
