package analytics

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// Category names for task classification.
const (
	CatCoding       = "Coding"
	CatDebugging    = "Debugging"
	CatTesting      = "Testing"
	CatExploration  = "Exploration"
	CatPlanning     = "Planning"
	CatDelegation   = "Delegation"
	CatGitOps       = "Git Ops"
	CatBuildDeploy  = "Build/Deploy"
	CatConversation = "Conversation"
	CatGeneral      = "General"
)

// debugKeywords are content keywords that indicate debugging activity.
var debugKeywords = []string{
	"error", "fix", "bug", "fail", "crash", "traceback", "exception",
	"panic", "nil pointer", "stack trace", "debug",
}

// testCommands are exec command substrings that indicate testing.
var testCommands = []string{
	"pytest", "vitest", "jest", "go test", "npm test", "cargo test",
	"rspec", "mocha", "unittest",
}

// buildCommands are exec command substrings that indicate build/deploy.
var buildCommands = []string{
	"npm build", "npm run build", "docker", "go build", "cargo build",
	"make", "tsc", "webpack", "vite build", "deploy", "kubectl",
	"terraform", "ansible",
}

// gitCommands are exec command substrings that indicate git operations.
var gitCommands = []string{
	"git commit", "git push", "git pull", "git merge", "git rebase",
	"git checkout", "git branch", "git stash", "git tag", "git log",
	"git diff", "git status", "git add", "git reset", "git revert",
	"git cherry-pick",
}

// ClassifySession determines the primary task category for a session based
// on tool usage patterns and message content keywords.
func ClassifySession(s *model.Session) string {
	hasEdit := false
	hasRead := false
	hasExec := false
	hasSubAgent := false
	hasTodoWrite := false
	execCmds := []string{}

	for _, m := range s.Messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			switch tc.Name {
			case "edit", "write", "notebook_edit":
				hasEdit = true
			case "read", "grep", "find_file_by_name":
				hasRead = true
			case "exec", "shell_command":
				hasExec = true
				cmd := extractCommand(tc.Arguments)
				if cmd != "" {
					execCmds = append(execCmds, strings.ToLower(cmd))
				}
			case "run_subagent":
				hasSubAgent = true
			case "todo_write":
				hasTodoWrite = true
			}
		}
	}

	// Check exec commands for git, test, build patterns.
	hasGit := false
	hasTest := false
	hasBuild := false
	for _, cmd := range execCmds {
		if containsAny(cmd, gitCommands) {
			hasGit = true
		}
		if containsAny(cmd, testCommands) {
			hasTest = true
		}
		if containsAny(cmd, buildCommands) {
			hasBuild = true
		}
	}

	// Check content for debug keywords.
	hasDebug := false
	for _, m := range s.Messages {
		if m.Role != "assistant" && m.Role != "user" {
			continue
		}
		lc := strings.ToLower(m.Content)
		if containsAny(lc, debugKeywords) {
			hasDebug = true
			break
		}
	}

	// Check for plan mode.
	if s.AgentMode == "plan" || hasTodoWrite {
		// Planning takes priority if no edits.
		if !hasEdit {
			return CatPlanning
		}
	}

	// Priority order:
	// 1. Delegation (sub-agent heavy)
	if hasSubAgent && !hasEdit {
		return CatDelegation
	}
	// 2. Git ops
	if hasGit && !hasEdit {
		return CatGitOps
	}
	// 3. Build/Deploy
	if hasBuild && !hasEdit {
		return CatBuildDeploy
	}
	// 4. Testing
	if hasTest {
		if hasEdit && hasDebug {
			return CatDebugging
		}
		return CatTesting
	}
	// 5. Debugging (edit + debug keywords)
	if hasEdit && hasDebug {
		return CatDebugging
	}
	// 6. Coding (edit present)
	if hasEdit {
		return CatCoding
	}
	// 7. Exploration (read without edits)
	if hasRead && !hasExec {
		return CatExploration
	}
	// 8. Conversation (no tools at all)
	totalTools := 0
	for _, c := range s.ToolCalls {
		totalTools += c
	}
	if totalTools == 0 {
		return CatConversation
	}
	// 9. General fallback
	return CatGeneral
}

// TaskCategories classifies all sessions and returns aggregated category
// stats sorted by count descending.
func TaskCategories(ss []model.Session) []model.TaskCategory {
	byCat := map[string]*model.TaskCategory{}
	for i := range ss {
		s := &ss[i]
		cat := ClassifySession(s)
		tc := byCat[cat]
		if tc == nil {
			tc = &model.TaskCategory{Name: cat}
			byCat[cat] = tc
		}
		tc.Count++
		cost, _ := report.SessionCost(s)
		tc.Cost += cost
	}
	out := make([]model.TaskCategory, 0, len(byCat))
	for _, tc := range byCat {
		out = append(out, *tc)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Count > out[j].Count
	})
	return out
}

// extractCommand parses a tool call's JSON arguments and returns the
// command field if present.
func extractCommand(arguments string) string {
	if arguments == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(arguments), &m); err != nil {
		return ""
	}
	v, ok := m["command"]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// containsAny checks if s contains any of the substrings.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
