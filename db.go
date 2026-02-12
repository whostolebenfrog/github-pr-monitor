package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB

func openDB() error {
	dbPath := filepath.Join(configDir, "pr-monitor.db")

	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}

	// WAL mode for better concurrent read performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("setting WAL mode: %w", err)
	}

	if err := runMigrations(); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	return nil
}

func runMigrations() error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS prs (
			repo TEXT NOT NULL,
			number INTEGER NOT NULL,
			title TEXT NOT NULL,
			author TEXT NOT NULL,
			url TEXT NOT NULL,
			needs_review INTEGER NOT NULL DEFAULT 0,
			needs_reapproval INTEGER NOT NULL DEFAULT 0,
			ignored INTEGER NOT NULL DEFAULT 0,
			last_checked TEXT NOT NULL,
			PRIMARY KEY (repo, number)
		);

		CREATE TABLE IF NOT EXISTS state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS rechecks (
			repo TEXT NOT NULL,
			number INTEGER NOT NULL,
			started_at TEXT NOT NULL,
			PRIMARY KEY (repo, number)
		);
	`)
	if err != nil {
		return err
	}

	// Add muted column if it doesn't exist
	_, err = db.Exec(`ALTER TABLE prs ADD COLUMN muted INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}

	return nil
}

func dbSavePR(pr PRInfo) error {
	_, err := db.Exec(`
		INSERT INTO prs (repo, number, title, author, url, needs_review, needs_reapproval, ignored, last_checked)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT (repo, number) DO UPDATE SET
			title = excluded.title,
			author = excluded.author,
			url = excluded.url,
			needs_review = excluded.needs_review,
			needs_reapproval = excluded.needs_reapproval,
			last_checked = excluded.last_checked
	`, pr.Repo, pr.Number, pr.Title, pr.Author, pr.URL,
		boolToInt(pr.NeedsReview), boolToInt(pr.NeedsReapproval),
		time.Now().Format(time.RFC3339))
	return err
}

func dbRemovePR(repo string, number int) error {
	_, err := db.Exec("DELETE FROM prs WHERE repo = ? AND number = ?", repo, number)
	return err
}

func dbRemoveRepoActivePRs(repo string) error {
	_, err := db.Exec("DELETE FROM prs WHERE repo = ? AND ignored = 0 AND muted = 0", repo)
	return err
}

func dbLoadActivePRs() ([]PRInfo, error) {
	rows, err := db.Query(`
		SELECT repo, number, title, author, url, needs_review, needs_reapproval
		FROM prs WHERE ignored = 0 AND muted = 0
		ORDER BY repo, number
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []PRInfo
	for rows.Next() {
		var pr PRInfo
		var needsReview, needsReapproval int
		if err := rows.Scan(&pr.Repo, &pr.Number, &pr.Title, &pr.Author, &pr.URL,
			&needsReview, &needsReapproval); err != nil {
			return nil, err
		}
		pr.NeedsReview = needsReview != 0
		pr.NeedsReapproval = needsReapproval != 0
		result = append(result, pr)
	}
	return result, rows.Err()
}

func dbIgnorePR(repo string, number int) error {
	_, err := db.Exec(`
		INSERT INTO prs (repo, number, title, author, url, ignored, last_checked)
		VALUES (?, ?, '', '', '', 1, ?)
		ON CONFLICT (repo, number) DO UPDATE SET ignored = 1
	`, repo, number, time.Now().Format(time.RFC3339))
	return err
}

func dbClearIgnored() error {
	_, err := db.Exec("DELETE FROM prs WHERE ignored = 1")
	return err
}

func dbIsIgnored(repo string, number int) bool {
	var ignored int
	err := db.QueryRow("SELECT ignored FROM prs WHERE repo = ? AND number = ?", repo, number).Scan(&ignored)
	if err != nil {
		return false
	}
	return ignored != 0
}

func dbIgnoredCount() int {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM prs WHERE ignored = 1").Scan(&count)
	return count
}

func dbMutePR(repo string, number int) error {
	_, err := db.Exec(`
		INSERT INTO prs (repo, number, title, author, url, muted, last_checked)
		VALUES (?, ?, '', '', '', 1, ?)
		ON CONFLICT (repo, number) DO UPDATE SET muted = 1
	`, repo, number, time.Now().Format(time.RFC3339))
	return err
}

func dbUnmutePR(repo string, number int) error {
	_, err := db.Exec("UPDATE prs SET muted = 0 WHERE repo = ? AND number = ?", repo, number)
	return err
}

func dbIsMuted(repo string, number int) bool {
	var muted int
	err := db.QueryRow("SELECT muted FROM prs WHERE repo = ? AND number = ?", repo, number).Scan(&muted)
	if err != nil {
		return false
	}
	return muted != 0
}

func dbMutedCount() int {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM prs WHERE muted = 1").Scan(&count)
	return count
}

func dbClearMuted() error {
	_, err := db.Exec("DELETE FROM prs WHERE muted = 1")
	return err
}

func dbGetState(key string) string {
	var value string
	db.QueryRow("SELECT value FROM state WHERE key = ?", key).Scan(&value)
	return value
}

func dbSetState(key, value string) error {
	_, err := db.Exec(`
		INSERT INTO state (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// importIgnoredJSON migrates ignored.json into the database (one-time)
func importIgnoredJSON() error {
	if dbGetState("ignored_json_imported") == "true" {
		return nil
	}

	ignoredPath := filepath.Join(configDir, "ignored.json")
	data, err := os.ReadFile(ignoredPath)
	if os.IsNotExist(err) {
		return dbSetState("ignored_json_imported", "true")
	}
	if err != nil {
		return err
	}

	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}

	for _, key := range keys {
		repo, number := parsePRKey(key)
		if repo != "" && number > 0 {
			if err := dbIgnorePR(repo, number); err != nil {
				log.Printf("Warning: failed to import ignored PR %s: %v", key, err)
			}
		}
	}

	log.Printf("Imported %d ignored PRs from ignored.json", len(keys))
	return dbSetState("ignored_json_imported", "true")
}

func parsePRKey(key string) (repo string, number int) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '#' {
			repo = key[:i]
			fmt.Sscanf(key[i+1:], "%d", &number)
			return
		}
	}
	return "", 0
}

type recheckEntry struct {
	Repo      string
	Number    int
	StartedAt time.Time
}

func dbAddRecheck(repo string, number int) error {
	_, err := db.Exec(`
		INSERT INTO rechecks (repo, number, started_at) VALUES (?, ?, ?)
		ON CONFLICT (repo, number) DO UPDATE SET started_at = excluded.started_at
	`, repo, number, time.Now().Format(time.RFC3339))
	return err
}

func dbRemoveRecheck(repo string, number int) error {
	_, err := db.Exec("DELETE FROM rechecks WHERE repo = ? AND number = ?", repo, number)
	return err
}

func dbLoadRechecks() ([]recheckEntry, error) {
	rows, err := db.Query("SELECT repo, number, started_at FROM rechecks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []recheckEntry
	for rows.Next() {
		var e recheckEntry
		var ts string
		if err := rows.Scan(&e.Repo, &e.Number, &ts); err != nil {
			return nil, err
		}
		e.StartedAt, _ = time.Parse(time.RFC3339, ts)
		result = append(result, e)
	}
	return result, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
