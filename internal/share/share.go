// Package share builds the aggregate-only, sanitised usage report behind the
// `devinmonitor share` command.
//
// The command exists so a user can paste a report into an issue without
// thinking about it, which is why it is deliberately blunt about what it will
// not emit: no absolute paths, no session IDs, no raw error text, and no
// message content. It also never touches the network — TestNoNetworkImports in
// share_test.go parses this package's own imports to keep that promise honest.
package share

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/errscan"
	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
)

// SchemaVersion is the share-schema version; bump on breaking changes.
const SchemaVersion = 1

// UnknownLabel replaces anything that would otherwise leak structure: an empty
// or separator-only working directory, a missing model name.
const UnknownLabel = "(unknown)"

// Notice is the fixed sentence carried by every report.
const Notice = "This report is aggregate-only: it contains no local paths, no session IDs, " +
	"no message content and no raw error text. It was produced locally by devinmonitor " +
	"and nothing was uploaded."

// Cost provenance values. Exactly one of these is reported.
const (
	ProvenanceOfficial  = "official"
	ProvenanceEstimated = "estimated"
	ProvenanceMixed     = "mixed"
	ProvenanceUnknown   = "unknown"
)

// Redaction records one sanitisation decision so the user can audit exactly
// what leaves their machine before they send it anywhere.
type Redaction struct {
	Field  string `json:"field"`
	Action string `json:"action"` // "removed" | "reduced" | "aggregated"
	Reason string `json:"reason"`
}

// ModelUsage is one model's aggregate. No session or path data.
type ModelUsage struct {
	Model        string  `json:"model"`
	Requests     int     `json:"requests"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	Cost         float64 `json:"cost"`
}

// ProjectUsage is one project's aggregate, keyed by the LAST path segment only.
type ProjectUsage struct {
	Project  string  `json:"project"`
	Sessions int     `json:"sessions"`
	Cost     float64 `json:"cost"`
}

// Usage is the aggregate usage section.
type Usage struct {
	TotalSessions  int            `json:"totalSessions"`
	TotalRequests  int            `json:"totalRequests"`
	TotalTokens    int64          `json:"totalTokens"`
	TotalCost      float64        `json:"totalCost"`
	CostProvenance string         `json:"costProvenance"` // official | estimated | mixed | unknown
	ByModel        []ModelUsage   `json:"byModel"`
	ByProject      []ProjectUsage `json:"byProject"`
}

// Report is the sanitised, shareable document.
type Report struct {
	SchemaVersion int         `json:"schemaVersion"`
	GeneratedAt   string      `json:"generatedAt"` // RFC3339 UTC
	Usage         Usage       `json:"usage"`
	Errors        interface{} `json:"errors,omitempty"` // *errscan.Report, aggregates only
	Redactions    []Redaction `json:"redactions"`
	Notice        string      `json:"notice"`
}

// Build assembles the sanitised report. includeErrors adds the error
// classification aggregates (never the raw finding text).
func Build(ss []model.Session, includeErrors bool, now time.Time) Report {
	rep := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   now.UTC().Format(time.RFC3339),
		Redactions:    Manifest(),
		Notice:        Notice,
	}

	byModel := map[string]*ModelUsage{}
	byProject := map[string]*ProjectUsage{}
	sawOfficial, sawEstimated := false, false

	for i := range ss {
		s := &ss[i]

		cost, source := sessionCost(s)
		switch source {
		case ProvenanceOfficial:
			sawOfficial = true
		case ProvenanceEstimated:
			sawEstimated = true
		}

		requests := sessionRequests(s)
		name := modelLabel(s)
		mu := byModel[name]
		if mu == nil {
			mu = &ModelUsage{Model: name}
			byModel[name] = mu
		}
		mu.Requests += requests
		mu.InputTokens += s.InputTokens
		mu.OutputTokens += s.OutputTokens
		mu.Cost += cost

		label := SanitizeProject(s.WorkingDir)
		pu := byProject[label]
		if pu == nil {
			pu = &ProjectUsage{Project: label}
			byProject[label] = pu
		}
		pu.Sessions++
		pu.Cost += cost

		rep.Usage.TotalSessions++
		rep.Usage.TotalRequests += requests
		rep.Usage.TotalTokens += s.InputTokens + s.OutputTokens
		rep.Usage.TotalCost += cost
	}

	switch {
	case sawOfficial && sawEstimated:
		rep.Usage.CostProvenance = ProvenanceMixed
	case sawOfficial:
		rep.Usage.CostProvenance = ProvenanceOfficial
	case sawEstimated:
		rep.Usage.CostProvenance = ProvenanceEstimated
	default:
		rep.Usage.CostProvenance = ProvenanceUnknown
	}

	rep.Usage.ByModel = sortedModels(byModel)
	rep.Usage.ByProject = sortedProjects(byProject)

	if includeErrors {
		// Passing the empty session ID means "aggregate everything".
		if er := errscan.Scan(ss, ""); er != nil {
			// Drop the findings outright: category counts and the error rate
			// are aggregate, but a finding may quote the message body.
			er.Findings = nil
			rep.Errors = er
		}
	}

	return rep
}

// SanitizeProject reduces a working directory to a shareable label: the last
// path segment only.
func SanitizeProject(workingDir string) string {
	// The data can come from Windows, so both separators count.
	s := strings.ReplaceAll(workingDir, `\`, "/")
	// Trailing separators are ignored: "/a/b/" and "/a/b" label identically.
	s = strings.TrimRight(s, "/")
	if s == "" {
		return UnknownLabel
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	if s == "" || s == "." {
		return UnknownLabel
	}
	return s
}

// Manifest lists every sanitisation rule that is applied, in a stable order.
func Manifest() []Redaction {
	return []Redaction{
		{
			Field:  "usage.byProject[].project",
			Action: "reduced",
			Reason: "Working directories are reduced to their last path segment; empty, \".\", \"/\" or separator-only paths become " + UnknownLabel + ".",
		},
		{
			Field:  "session.id",
			Action: "removed",
			Reason: "Session IDs are never emitted; sessions appear only as aggregate counts.",
		},
		{
			Field:  "errors.findings[].message",
			Action: "removed",
			Reason: "Raw error text is never emitted; only per-category counts and the error rate are included.",
		},
		{
			Field:  "messages[].content",
			Action: "removed",
			Reason: "Prompts, tool output and file contents are never emitted.",
		},
	}
}

// sessionCost returns the best available cost for a session and how it was
// obtained: official (Devin's own accounting), estimated (token math against
// the built-in pricing table), or "" when the session carries no usable cost
// signal at all.
func sessionCost(s *model.Session) (float64, string) {
	if c := s.CreditCost + s.ACUCost; c > 0 {
		return c, ProvenanceOfficial
	}
	if s.InputTokens+s.OutputTokens+s.CacheRead+s.CacheWrite == 0 {
		return 0, ""
	}
	p := model.LookupPricing(s.Model)
	if p.Free || (p.InputPerM == 0 && p.OutputPerM == 0) {
		// Priced at zero (free model) or unknown model: nothing to estimate,
		// and calling that "estimated" would overstate our knowledge.
		return 0, ""
	}
	return model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite), ProvenanceEstimated
}

// sessionRequests counts assistant turns, falling back to the message list when
// the session-level counter was not populated.
func sessionRequests(s *model.Session) int {
	if s.AssistantCount > 0 {
		return s.AssistantCount
	}
	n := 0
	for _, m := range s.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

// modelLabel prefers the most recent generation model over the creation-time
// session model, matching how the rest of the CLI attributes usage.
func modelLabel(s *model.Session) string {
	if s.LatestModel != "" {
		return s.LatestModel
	}
	if s.Model != "" {
		return s.Model
	}
	return UnknownLabel
}

// sortedModels flattens the model aggregates in a deterministic order.
func sortedModels(m map[string]*ModelUsage) []ModelUsage {
	out := make([]ModelUsage, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// sortedProjects flattens the project aggregates in a deterministic order.
func sortedProjects(m map[string]*ProjectUsage) []ProjectUsage {
	out := make([]ProjectUsage, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Project < out[j].Project
	})
	return out
}

// ---- command ----

// openReader opens the session DB using the inherited --data-dir flag.
func openReader(cmd *cobra.Command) reader.Reader {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	r, err := reader.Open(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open reader: %v\n", err)
		os.Exit(1)
	}
	return r
}

var cmdShare = func() *cobra.Command {
	c := &cobra.Command{
		Use:   "share",
		Short: i18n.T("cmd.share"),
		Run: func(cmd *cobra.Command, args []string) {
			output, _ := cmd.Flags().GetString("output")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			includeErrors, _ := cmd.Flags().GetBool("include-errors")
			format, _ := cmd.Flags().GetString("format")

			// A bad format is a usage error, checked before anything is read
			// or written, so it fails the same way with --dry-run too.
			if err := checkFormat(format); err != nil {
				fmt.Fprintf(os.Stderr, "share: %v\n", err)
				os.Exit(1)
			}
			format = normalizeFormat(format)

			if dryRun {
				// Nothing is scanned and nothing is written: the manifest is
				// static, so the user can audit the rules before any read.
				writeDryRun(os.Stdout)
				return
			}

			r := openReader(cmd)
			defer r.Close()

			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "read sessions: %v\n", err)
				os.Exit(1)
			}

			rep := Build(ss, includeErrors, time.Now())

			out := os.Stdout
			if output != "" {
				f, err := os.Create(output)
				if err != nil {
					fmt.Fprintf(os.Stderr, "create output: %v\n", err)
					os.Exit(1)
				}
				defer f.Close()
				out = f
			}

			if format == FormatHTML {
				if err := RenderHTML(out, rep); err != nil {
					fmt.Fprintf(os.Stderr, "write report: %v\n", err)
					os.Exit(1)
				}
			} else {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(rep); err != nil {
					fmt.Fprintf(os.Stderr, "write report: %v\n", err)
					os.Exit(1)
				}
			}

			// stderr, so a user redirecting stdout into a file still sees it.
			fmt.Fprintln(os.Stderr, confirmation(output, len(rep.Redactions)))
		},
	}
	c.Flags().String("output", "", i18n.T("help.shareOutput"))
	c.Flags().Bool("dry-run", false, i18n.T("help.shareDryRun"))
	c.Flags().Bool("include-errors", false, "Include error classification aggregates (never raw messages)")
	c.Flags().String("format", FormatJSON, i18n.T("help.shareFormat"))
	return c
}

func init() { cli.Register(cmdShare) }

// confirmation renders the post-write one-liner. It is factored out of Run so
// the redaction count it reports can be asserted in tests.
func confirmation(destination string, redactions int) string {
	where := destination
	if where == "" {
		where = "stdout"
	}
	return fmt.Sprintf("Wrote sanitised share report to %s (%d sanitisation rules applied; nothing was uploaded).",
		where, redactions)
}

// writeDryRun prints the sanitisation manifest without touching the database.
func writeDryRun(w io.Writer) {
	fmt.Fprintln(w, "Dry run: no report was written and the local database was not read.")
	fmt.Fprintln(w, "Every share report is sanitised with these rules:")
	for i, r := range Manifest() {
		fmt.Fprintf(w, "  %d. %s — %s: %s\n", i+1, r.Field, r.Action, r.Reason)
	}
}
