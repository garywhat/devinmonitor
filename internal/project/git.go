package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// ---- Git Integration (#102) ----

var cmdGit = func() *cobra.Command {
	var sessionID string
	var days int
	c := &cobra.Command{
		Use:   "git [--session <id>|--days 30]",
		Short: i18n.T("cmd.git"),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()

			if sessionID != "" {
				s, err := r.Session(sessionID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					os.Exit(1)
				}
				printSessionGitCommits(s)
				return
			}

			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			cutoff := time.Now().AddDate(0, 0, -days)
			var recent []model.Session
			for _, s := range ss {
				if s.LastActivityAt.After(cutoff) {
					recent = append(recent, s)
				}
			}
			printGitOverview(recent, days)
		},
	}
	c.Flags().StringVar(&sessionID, "session", "", "show commits for a specific session")
	c.Flags().IntVar(&days, "days", 30, "number of days to look back")
	return c
}

// gitCommit is a parsed git log entry.
type gitCommit struct {
	Hash      string
	Author    string
	Date      time.Time
	Subject   string
}

// readGitLog reads the git log for a repository at dir, since the given time.
// Returns nil if dir is not a git repo.
func readGitLog(dir string, since time.Time) []gitCommit {
	// Check if dir is a git repo.
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		// Try git rev-parse as a more reliable check.
		cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
		if err := cmd.Run(); err != nil {
			return nil
		}
	}

	sinceStr := since.Format("2006-01-02")
	cmd := exec.Command("git", "-C", dir, "log", "--since="+sinceStr,
		"--pretty=format:%H|%an|%aI|%s", "--no-merges")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	var commits []gitCommit
	for _, line := range strings.Split(string(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		date, err := time.Parse(time.RFC3339, parts[2])
		if err != nil {
			continue
		}
		commits = append(commits, gitCommit{
			Hash:    parts[0],
			Author:  parts[1],
			Date:    date,
			Subject: parts[3],
		})
	}
	return commits
}

// printSessionGitCommits shows commits that overlap with a session's time range.
func printSessionGitCommits(s *model.Session) {
	if s.WorkingDir == "" {
		fmt.Printf("Session %s has no working directory.\n", s.ID)
		return
	}
	commits := readGitLog(s.WorkingDir, s.CreatedAt)
	if commits == nil {
		fmt.Printf("Working directory is not a git repo: %s\n", s.WorkingDir)
		return
	}

	// Filter commits to those within the session's time range.
	var overlapping []gitCommit
	for _, c := range commits {
		if (c.Date.After(s.CreatedAt) || c.Date.Equal(s.CreatedAt)) &&
			c.Date.Before(s.LastActivityAt.Add(1*time.Hour)) {
			overlapping = append(overlapping, c)
		}
	}

	cost, _ := report.SessionCost(s)
	fmt.Println(ui.Panel("Session Git Correlation", fmt.Sprintf(
		"Session:     %s\nTitle:       %s\nWorking Dir: %s\nTime Range:  %s ~ %s\nCost:        $%.2f\nCommits:     %d",
		s.ID, s.Title, s.WorkingDir,
		s.CreatedAt.Format("2006-01-02 15:04"),
		s.LastActivityAt.Format("2006-01-02 15:04"),
		cost, len(overlapping),
	), 70))

	if len(overlapping) == 0 {
		fmt.Println("\nNo commits found during this session (possibly abandoned).")
		return
	}

	fmt.Println("\nCommits:")
	t := ui.NewTable("Hash", "Author", "Date", "Subject")
	for _, c := range overlapping {
		t.Row(c.Hash[:8], c.Author, c.Date.Format("2006-01-02 15:04"),
			truncateStr(c.Subject, 50))
	}
	fmt.Println(t.String())

	if len(overlapping) > 0 {
		fmt.Printf("\nProductive session: %d commit(s) made.\n", len(overlapping))
	}
}

// printGitOverview shows git activity across all sessions.
func printGitOverview(ss []model.Session, days int) {
	type sessionCommits struct {
		session model.Session
		commits []gitCommit
	}
	var results []sessionCommits
	totalCommits := 0
	productive := 0
	abandoned := 0

	for _, s := range ss {
		if s.WorkingDir == "" {
			continue
		}
		commits := readGitLog(s.WorkingDir, s.CreatedAt)
		if commits == nil {
			continue
		}
		// Filter to session time range.
		var overlapping []gitCommit
		for _, c := range commits {
			if (c.Date.After(s.CreatedAt) || c.Date.Equal(s.CreatedAt)) &&
				c.Date.Before(s.LastActivityAt.Add(1*time.Hour)) {
				overlapping = append(overlapping, c)
			}
		}
		results = append(results, sessionCommits{s, overlapping})
		if len(overlapping) > 0 {
			productive++
			totalCommits += len(overlapping)
		} else if s.AssistantCount > 5 {
			abandoned++
		}
	}

	fmt.Println(ui.Panel("Git Integration Overview", fmt.Sprintf(
		"Period:           last %d days\nSessions checked: %d\nProductive:       %d (with commits)\nAbandoned:        %d (no commits, >5 requests)\nTotal commits:    %d",
		days, len(results), productive, abandoned, totalCommits,
	), 60))

	if len(results) == 0 {
		fmt.Println("\nNo git repositories found in session working directories.")
		return
	}

	// Sort by commit count descending.
	sort.Slice(results, func(i, j int) bool {
		return len(results[i].commits) > len(results[j].commits)
	})

	fmt.Println("\nPer-Session Commits:")
	t := ui.NewTable("Session", "Title", "Project", "Commits", "Cost")
	for _, r := range results {
		cost, _ := report.SessionCost(&r.session)
		status := fmt.Sprintf("%d", len(r.commits))
		if len(r.commits) == 0 {
			status = "-"
		}
		t.Row(r.session.ID, truncateStr(r.session.Title, 30),
			baseProject(r.session.WorkingDir), status,
			fmt.Sprintf("$%.2f", cost))
	}
	fmt.Println(t.String())
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
