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
	CatCoding        = "Coding"
	CatDebugging     = "Debugging"
	CatTesting       = "Testing"
	CatExploration   = "Exploration"
	CatPlanning      = "Planning"
	CatDelegation    = "Delegation"
	CatGitOps        = "Git Ops"
	CatBuildDeploy   = "Build/Deploy"
	CatRefactoring   = "Refactoring"
	CatDocumentation = "Documentation"
	CatConversation  = "Conversation"
	CatGeneral       = "General"
)

// Title keywords for the last-resort classifier, checked in this order. They
// are the vocabulary the `activities` command used exclusively before it shared
// this taxonomy; they survive as a fallback because a session whose messages
// were pruned or never recorded still has a title (see classifyFromTitle).
var (
	titleTestKeywords     = []string{"test", "pytest", "jest"}
	titleDebugKeywords    = []string{"debug", "fix", "bug", "error"}
	titleRefactorKeywords = []string{"refactor", "rename", "restructure"}
	titleDocKeywords      = []string{"doc", "readme", "comment"}
	titleDeployKeywords   = []string{"deploy", "ci", "release", "goreleaser"}
	titleGitKeywords      = []string{"git", "commit"}
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
	//
	// "At all" has to mean EITHER source is empty, because the two are
	// independent: the steps above classify per-message m.ToolCalls, while
	// s.ToolCalls is a session-level aggregate map that can be populated on its
	// own. Consulting only one of them mislabels the other kind of session as
	// "Conversation" even though it clearly used tools.
	totalTools := 0
	for _, c := range s.ToolCalls {
		totalTools += c
	}

	// Last resort, and only then: if NOTHING in any message supplied evidence
	// (no tool call, no debug keyword), the title and the session-level tool
	// names are the only signals left, so they get a say before the answer
	// degrades to "Conversation" or "General".
	//
	// The guard is what keeps the title from overriding real evidence: a
	// session titled "Fix the login bug" whose only recorded activity is reads
	// still classifies as Exploration, because hasRead is evidence and the title
	// is not. It matters because Devin prunes session storage, so "records are
	// gone" is a state that really occurs, and reporting such a session as a
	// conversation is a worse answer than a title-derived guess.
	if !hasEdit && !hasRead && !hasExec && !hasSubAgent && !hasTodoWrite && !hasDebug {
		if cat := classifyFromTitle(s); cat != "" {
			return cat
		}
	}

	if totalTools == 0 && !hasEdit && !hasRead && !hasExec && !hasSubAgent && !hasTodoWrite {
		return CatConversation
	}
	// 9. General fallback
	return CatGeneral
}

// classifyFromTitle is the title-based last-resort classifier: it returns a
// category from the same taxonomy as everything else, or "" when the title and
// tool names say nothing useful.
//
// The matching is deliberately the loose substring matching the pre-unification
// `activities` classifier used, including the tool-name blob, so that the
// sessions it could name still get named. It is NOT consulted when any message
// supplied evidence.
func classifyFromTitle(s *model.Session) string {
	blob := strings.ToLower(s.Title)
	for tool := range s.ToolCalls {
		blob += " " + strings.ToLower(tool)
	}
	switch {
	case containsAny(blob, titleTestKeywords):
		return CatTesting
	case containsAny(blob, titleDebugKeywords):
		return CatDebugging
	case containsAny(blob, titleRefactorKeywords):
		return CatRefactoring
	case containsAny(blob, titleDocKeywords):
		return CatDocumentation
	case containsAny(blob, titleDeployKeywords):
		return CatBuildDeploy
	case containsAny(blob, titleGitKeywords):
		return CatGitOps
	}
	return ""
}

// TaskCategories classifies all sessions and returns aggregated category
// stats sorted by count descending, then by name.
//
// The name tie-break is not cosmetic: `out` is built by iterating a map, so a
// pure count sort left the order of equal-count categories to Go's map
// iteration, which varies. `activities` prints the same categories and is
// compared against this table next to it, so both now order ties the same
// deterministic way (see categorizeSession in internal/project).
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
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
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
