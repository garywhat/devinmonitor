// Package errscan categorises tool errors found in session history, so a user
// can see what is actually going wrong — and how often — instead of grepping
// messages by hand.
//
// The classification table is adapted from sniffly's ERROR_PATTERNS
// (core/constants.py) to the errors a coding agent actually emits. It is an
// ORDERED table: the first category with a matching pattern wins, so specific
// and interruption classes precede generic ones.
//
// The package self-registers its "errors" cobra command via cli.Register in
// init(); main.go wires the package in during integration.
package errscan

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// other is the bucket Categorize returns when no category pattern matches. It
// is deliberately NOT an entry in Categories.
const other = "Other"

// maxContentRunes caps Finding.Content so a giant tool output cannot bloat the
// JSON report. The ellipsis is part of the budget.
const maxContentRunes = 200

// maxExampleLineWidth is the display-column budget for one example line
// printed by the text renderer.
const maxExampleLineWidth = 120

// Category is one error class: a name plus the case-insensitive regular
// expressions that identify it.
type Category struct {
	Name     string
	Patterns []string
}

// Categories is the ordered classification table. ORDER IS SEMANTIC: the first
// category with a matching pattern wins, so specific classes must precede
// general ones.
//
// A message that matches nothing is classified as "Other" by Categorize; that
// bucket is not listed here.
var Categories = []Category{
	{
		Name: "User Interruption",
		Patterns: []string{
			`user doesn't want to proceed`,
			`user doesn't want to take this action`,
			`\[Request interrupted`,
		},
	},
	{
		Name:     "Command Timeout",
		Patterns: []string{`Command timed out`},
	},
	{
		Name:     "File Not Read",
		Patterns: []string{`File has not been read yet`},
	},
	{
		// A file that exists but could not be used. Deliberately placed BEFORE
		// "Tool Validation Error": a read failure usually also carries
		// "validation failed", and the file-level cause is the more actionable
		// classification.
		Name: "File Read Error",
		Patterns: []string{
			`Failed to read file`,
			`Offset \d+ is beyond end of file`,
			`is a directory`,
		},
	},
	{
		Name:     "File Modified",
		Patterns: []string{`File has been modified since read`},
	},
	{
		Name:     "File Too Large",
		Patterns: []string{`exceeds maximum allowed`},
	},
	{
		Name: "Permission Error",
		Patterns: []string{
			`Permission denied`,
			// Devin emits localized error text too; the classification table
			// was English-only. These are the specific localized permission
			// phrasings, not a generic "任何含错误二字的消息".
			`许可错误|权限不足|没有权限|拒绝访问`,
			// Two independent conditions, kept as ONE regex on purpose: split
			// into two patterns, a plain "cd to" anywhere would be
			// misclassified as a permission error.
			`(?=.*cd to)(?=.*was blocked)`,
		},
	},
	{
		Name: "Content Not Found",
		Patterns: []string{
			`String to replace not found`,
			`String not found in file`,
			`No module named`,
			`No such file or directory`,
			`File does not exist`,
		},
	},
	{
		Name:     "Tool Not Found",
		Patterns: []string{`command not found`},
	},
	{
		Name:     "No Changes",
		Patterns: []string{`No changes to make`},
	},
	{
		// The tool rejected the call before running it (argument shape, option
		// counts, offsets). Distinct from a tool that ran and failed.
		Name: "Tool Validation Error",
		Patterns: []string{
			`Tool '[^']*' validation failed`,
			`validation failed`,
		},
	},
	{
		// Structured failures reported by a tool or a remote service: JSON
		// error objects, HTTP codes, non-zero exit statuses. Kept last of the
		// specific categories because these markers are the most generic.
		// Only 4xx/5xx are matched so a successful "status code 200" is not
		// mistaken for an error.
		Name: "Tool Reported Error",
		Patterns: []string{
			`"error(Code|Message)"`,
			`\(code \d+\)`,
			`\bcode [45]\d\d\b`,
			`exited with (code|status) [1-9]`,
			`UNAVAILABLE`,
		},
	},
	{
		// The fallback tier: a body carrying a POSITIVE, ANCHORED error signal
		// that matches no named category - a Python traceback, a `fatal:` line,
		// a CLI printing `Error: ...`.
		//
		// Every pattern is anchored to the start of the body, because position
		// is what makes the signal trustworthy. A generic "contains the word
		// error" test was measured against a real database and classified
		// `<file-view path=...>` listings, `Found 30 match(es)` grep output and
		// `✓ ... started` success messages as errors: file contents and match
		// results routinely contain those words. Precision is worth more than
		// recall here - an inflated error rate is worse than a conservative one.
		Name: "Unclassified Error",
		Patterns: []string{
			`(?i)^\s*(error|failed|fatal|exception|traceback|panic)[:.\s]`,
			`(?i)^output from .{0,80}?:\s*(error|fatal|traceback)`,
			`(?i)^\s*\{\s*"(error|errorCode|errorMessage|errorType)"\s*:`,
			`(?i)^\s*(cannot|unable to|permission denied|access denied|not found|invalid |unexpected |no such )`,
			`(?i)^\s*(exit(ed)? (with )?(code|status) [1-9]|command failed|command not found)`,
			`^\s*(错误|失败|异常|无法|拒绝|超时)[:：]`,
		},
	},
}

// Finding is one categorised error occurrence.
type Finding struct {
	Category  string    `json:"category"`
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"sessionId"`
	Model     string    `json:"model"`
	Content   string    `json:"content"`
}

// CategoryCount aggregates one category.
type CategoryCount struct {
	Category string  `json:"category"`
	Count    int     `json:"count"`
	Percent  float64 `json:"percent"` // share of all errors, 1 decimal
}

// Report is the analysis result.
type Report struct {
	Total             int             `json:"total"`
	AssistantMessages int             `json:"assistantMessages"`
	ErrorRate         float64         `json:"errorRate"` // errors / assistant messages, 1 decimal percent
	ByCategory        []CategoryCount `json:"byCategory"`
	Findings          []Finding       `json:"findings"`
}

// Categorize returns the category name for a message body, or "Other" when
// nothing matches.
func Categorize(content string) string {
	for i := range Categories {
		c := Categories[i]
		for _, p := range c.Patterns {
			if matcherFor(p)(content) {
				return c.Name
			}
		}
	}
	return other
}

// ---- pattern matching ----
//
// Patterns are cached per pattern string so a mutated Categories table is
// picked up on the next call rather than pinned at init time.

var patternCache sync.Map // pattern string -> func(string) bool

// matcherFor returns a cached case-insensitive matcher for one pattern.
func matcherFor(pattern string) func(string) bool {
	if m, ok := patternCache.Load(pattern); ok {
		return m.(func(string) bool)
	}
	m := compilePattern(pattern)
	patternCache.Store(pattern, m)
	return m
}

// compilePattern builds the matcher for one pattern. It never fails: a pattern
// Go's regexp cannot compile degrades to the strongest equivalent test we can
// build.
func compilePattern(pattern string) func(string) bool {
	if re, err := regexp.Compile("(?i)" + pattern); err == nil {
		return re.MatchString
	}
	// Go's regexp is RE2, which has no lookahead assertions — so a pattern
	// like (?=.*cd to)(?=.*was blocked) cannot compile as written. Such a
	// pattern is a pure conjunction of independent "appears somewhere" tests,
	// which is exactly what we evaluate instead. This keeps the frozen,
	// sniffly-compatible table intact without weakening the check.
	if parts, ok := splitLookaheads(pattern); ok {
		res := make([]*regexp.Regexp, 0, len(parts))
		for _, p := range parts {
			re, err := regexp.Compile("(?i)" + p)
			if err != nil {
				res = nil
				break
			}
			res = append(res, re)
		}
		if res != nil {
			return func(s string) bool {
				for _, re := range res {
					if !re.MatchString(s) {
						return false
					}
				}
				return true
			}
		}
	}
	// Last resort for an unparseable pattern: case-insensitive literal search.
	lower := strings.ToLower(pattern)
	return func(s string) bool { return strings.Contains(strings.ToLower(s), lower) }
}

// lookaheadHeadRe matches one leading `(?=.*…)` group.
var lookaheadHeadRe = regexp.MustCompile(`^\(\?=\.\*(.+?)\)`)

// splitLookaheads decomposes a pattern made only of leading (?=.*…) groups
// into its inner expressions. It reports false for anything else, so callers
// fall back rather than silently mis-matching a mixed pattern.
func splitLookaheads(pattern string) ([]string, bool) {
	rest := pattern
	var parts []string
	for rest != "" {
		if !strings.HasPrefix(rest, "(?=") {
			return nil, false
		}
		m := lookaheadHeadRe.FindStringSubmatch(rest)
		if m == nil {
			return nil, false
		}
		parts = append(parts, m[1])
		rest = rest[len(m[0]):]
	}
	if len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

// ---- scan ----

// Scan analyses errors across the given sessions. When sessionID is non-empty
// only that session is considered.
//
// Every non-empty message body is a classification candidate regardless of
// role — different agents put error text in different places (tool results,
// user turns, assistant narration). A body classified as Other matches no
// known pattern, so it is not an error occurrence and is not reported.
func Scan(ss []model.Session, sessionID string) *Report {
	rep := &Report{
		ByCategory: []CategoryCount{},
		Findings:   []Finding{},
	}
	counts := map[string]int{}

	for _, s := range ss {
		if sessionID != "" && s.ID != sessionID {
			continue
		}
		// Session-level fallback: LatestModel is more accurate than Model
		// (which is stamped at creation and goes stale after a model switch).
		sessionModel := s.LatestModel
		if sessionModel == "" {
			sessionModel = s.Model
		}
		for _, m := range s.Messages {
			if m.Role == "assistant" {
				rep.AssistantMessages++
			}
			if strings.TrimSpace(m.Content) == "" {
				continue
			}
			cat := Categorize(m.Content)
			if cat == other {
				continue
			}
			modelName := sessionModel
			if m.GenerationModel != "" {
				modelName = m.GenerationModel
			}
			rep.Findings = append(rep.Findings, Finding{
				Category:  cat,
				Timestamp: m.CreatedAt,
				SessionID: s.ID,
				Model:     modelName,
				Content:   truncateContent(m.Content),
			})
			counts[cat]++
		}
	}

	rep.Total = len(rep.Findings)
	for name, n := range counts {
		rep.ByCategory = append(rep.ByCategory, CategoryCount{
			Category: name,
			Count:    n,
			Percent:  percentOf(n, rep.Total),
		})
	}
	// Deterministic order: most frequent first, ties broken by name.
	sort.SliceStable(rep.ByCategory, func(i, j int) bool {
		a, b := rep.ByCategory[i], rep.ByCategory[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Category < b.Category
	})
	rep.ErrorRate = percentOf(rep.Total, rep.AssistantMessages)
	return rep
}

// truncateContent caps a body at maxContentRunes runes, ellipsis included, so
// the cut can never land inside a multi-byte character.
func truncateContent(s string) string {
	rs := []rune(s)
	if len(rs) <= maxContentRunes {
		return s
	}
	return string(rs[:maxContentRunes-1]) + "…"
}

// percentOf returns part/whole as a percentage rounded to 1 decimal. A
// non-positive whole yields 0 rather than NaN.
func percentOf(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return math.Round(float64(part)/float64(whole)*1000) / 10
}

// ---- JSON ----

// jsonReport projects a Report for --json output. Findings are capped at
// examples entries per category, keeping scan order; examples <= 0 drops them
// to an empty (never null) array. The original Report is not modified.
func jsonReport(r *Report, examples int) *Report {
	out := *r
	if out.ByCategory == nil {
		out.ByCategory = []CategoryCount{}
	}
	out.Findings = []Finding{}
	if examples > 0 {
		seen := map[string]int{}
		for _, f := range r.Findings {
			if seen[f.Category] >= examples {
				continue
			}
			seen[f.Category]++
			out.Findings = append(out.Findings, f)
		}
	}
	return &out
}

// writeJSON writes the indented JSON report plus a trailing newline.
func writeJSON(w io.Writer, r *Report, examples int) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(jsonReport(r, examples))
}

// ---- command ----

var cmdErrors = func() *cobra.Command {
	c := &cobra.Command{
		Use:   "errors",
		Short: i18n.T("cmd.errors"),
		Run: func(cmd *cobra.Command, args []string) {
			sessionID, _ := cmd.Flags().GetString("session")
			asJSON, _ := cmd.Flags().GetBool("json")
			examples, _ := cmd.Flags().GetInt("examples")
			if examples < 0 {
				examples = 0
			}

			dataDir, _ := cmd.Flags().GetString("data-dir")
			r, err := reader.Open(dataDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "open reader: %v\n", err)
				os.Exit(1)
			}
			defer r.Close()

			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "read sessions: %v\n", err)
				os.Exit(1)
			}

			rep := Scan(ss, sessionID)

			if asJSON {
				if err := writeJSON(os.Stdout, rep, examples); err != nil {
					fmt.Fprintf(os.Stderr, "write json: %v\n", err)
					os.Exit(1)
				}
				return
			}

			if rep.Total == 0 {
				if sessionID != "" {
					fmt.Printf("No errors found in session %s.\n", sessionID)
					return
				}
				fmt.Println("No errors found.")
				return
			}
			renderReport(rep, examples)
		},
	}
	c.Flags().String("session", "", i18n.T("help.errSession"))
	c.Flags().Bool("json", false, i18n.T("help.errJSON"))
	c.Flags().Int("examples", 0, i18n.T("help.errExamples"))
	return c
}

func init() { cli.Register(cmdErrors) }

// renderReport prints the category table, a summary line, and — when
// examples > 0 — up to that many example messages per category.
func renderReport(rep *Report, examples int) {
	t := ui.NewTable(i18n.T("common.category"), i18n.T("common.count"), i18n.T("common.percent"))
	t.RightAlign(1, 2)
	for _, c := range rep.ByCategory {
		t.Row(c.Category, strconv.Itoa(c.Count), fmt.Sprintf("%.1f%%", c.Percent))
	}
	fmt.Println(t.String())
	fmt.Printf("Total: %d errors across %d assistant messages (error rate %.1f%%)\n",
		rep.Total, rep.AssistantMessages, rep.ErrorRate)

	if examples <= 0 {
		return
	}
	for _, c := range rep.ByCategory {
		shown := 0
		for _, f := range rep.Findings {
			if f.Category != c.Category || shown >= examples {
				continue
			}
			if shown == 0 {
				fmt.Printf("\n%s\n", c.Category)
			}
			for _, line := range exampleLines(f, maxExampleLineWidth) {
				fmt.Println(line)
			}
			shown++
		}
	}
}

// exampleLines renders one finding as a wrapped block: the timestamp and
// session ID prefix the first line, continuation lines are aligned under the
// message body. Each line stays within width display columns.
func exampleLines(f Finding, width int) []string {
	prefix := fmt.Sprintf("  %s  %s  ", f.Timestamp.Format("2006-01-02 15:04"), f.SessionID)
	// Collapse newlines/tabs so one finding cannot break the layout.
	body := strings.Join(strings.Fields(f.Content), " ")

	avail := width - len(prefix) // prefix is ASCII, so len == display width
	if avail < 20 {
		avail = 20
	}
	lines := wrapText(body, avail)
	if len(lines) == 0 {
		lines = []string{""}
	}

	out := make([]string, 0, len(lines))
	out = append(out, ui.Truncate(prefix+lines[0], width))
	pad := strings.Repeat(" ", len(prefix))
	for _, l := range lines[1:] {
		out = append(out, ui.Truncate(pad+l, width))
	}
	return out
}

// wrapText greedily wraps s into lines fitting max display columns, treating
// whitespace-separated words as atoms and hard-splitting an over-long word.
func wrapText(s string, max int) []string {
	if s == "" {
		return nil
	}
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case fitsWidth(cur+" "+word, max):
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
		for !fitsWidth(cur, max) {
			head, tail := splitAtWidth(cur, max)
			lines = append(lines, head)
			cur = tail
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// fitsWidth reports whether s fits within max display columns. ui.Truncate
// returns its input unchanged when it already fits, which gives us a
// width-correct check without another dependency.
func fitsWidth(s string, max int) bool {
	return ui.Truncate(s, max) == s
}

// splitAtWidth splits s into the longest prefix fitting max display columns
// and the remainder, never inside a rune.
func splitAtWidth(s string, max int) (string, string) {
	rs := []rune(s)
	for i := 1; i <= len(rs); i++ {
		if !fitsWidth(string(rs[:i]), max) {
			if i == 1 {
				// A single rune wider than the budget: keep it rather than
				// spin forever.
				return string(rs[:1]), string(rs[1:])
			}
			return string(rs[:i-1]), string(rs[i-1:])
		}
	}
	return s, ""
}
