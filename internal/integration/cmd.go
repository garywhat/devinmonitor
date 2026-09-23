// Package integration implements CLI commands, MCP server, web dashboard,
// notifications, and project management features for devinmonitor.
//
// All cobra commands are self-registered via cli.Register() in init() so
// they can be picked up by the main package without editing main.go.
package integration

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
)

// openReader opens a reader using the --data-dir persistent flag from cmd.
func openReader(cmd *cobra.Command) reader.Reader {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	r, err := reader.Open(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	return r
}

// dataDirFrom extracts the --data-dir flag value from cmd.
func dataDirFrom(cmd *cobra.Command) string {
	d, _ := cmd.Flags().GetString("data-dir")
	return d
}

// provenanceLabel returns "official" when cost comes from Devin's own
// accounting (credit/ACU), or "estimated" when derived from token pricing.
func provenanceLabel(creditCost, acuCost float64) string {
	if creditCost > 0 || acuCost > 0 {
		return "official"
	}
	return "estimated"
}

// saveConfig persists the global config, or fails loudly.
//
// A command whose entire purpose is to store a preference must not print
// "set" and exit 0 when the write did not happen: the user would believe a
// setting took effect that will be gone as soon as the process exits. Several
// call sites used to discard the error.
func saveConfig() {
	if err := config.SaveGlobal(); err != nil {
		fmt.Fprintf(os.Stderr, "save config: %v\n", err)
		os.Exit(1)
	}
}

// watchLoop re-runs fn every interval until the process is interrupted.
// It clears the screen between renders for a live-updating effect.
func watchLoop(interval time.Duration, fn func() error) {
	for {
		fmt.Print("\033[2J\033[H") // clear screen + home cursor
		if err := fn(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		time.Sleep(interval)
	}
}

// costSummary holds aggregated cost data for status/web/MCP.
type costSummary struct {
	TotalCost  float64 `json:"totalCost"`
	TodayCost  float64 `json:"todayCost"`
	WeekCost   float64 `json:"weekCost"`
	MonthCost  float64 `json:"monthCost"`
	TotalReqs  int     `json:"totalRequests"`
	TotalSess  int     `json:"totalSessions"`
	ActiveSess int     `json:"activeSessions"`
	Provenance string  `json:"provenance"`
}

// computeCostSummary aggregates cost data from sessions.
func computeCostSummary(ss []model.Session) costSummary {
	var sum costSummary
	now := time.Now()
	todayStart := model.DayStart(now)
	weekStart := todayStart.AddDate(0, 0, -int(now.Weekday()))
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	sum.TotalSess = len(ss)
	official, estimated := 0, 0
	for _, s := range ss {
		cost, _ := report.SessionCost(&s)
		sum.TotalCost += cost
		sum.TotalReqs += s.AssistantCount
		if s.LastActivityAt.After(todayStart) {
			sum.TodayCost += cost
		}
		if s.LastActivityAt.After(weekStart) {
			sum.WeekCost += cost
		}
		if s.LastActivityAt.After(monthStart) {
			sum.MonthCost += cost
		}
		// Active = last activity within 5 minutes.
		if now.Sub(s.LastActivityAt) < 5*time.Minute {
			sum.ActiveSess++
		}
		// Grade where the cost figure came from: Devin's own credit/ACU
		// accounting is authoritative; anything else is derived from the
		// built-in token pricing table.
		if s.CreditCost > 0 || s.ACUCost > 0 {
			official++
		} else {
			estimated++
		}
	}
	sum.Provenance = provenanceFrom(official, estimated)
	return sum
}

// provenanceFrom grades the aggregate cost provenance. The vocabulary
// ("official" / "estimated" / "mixed") matches provenanceLabel and
// provenanceTag so a single tool never speaks two dialects.
func provenanceFrom(official, estimated int) string {
	switch {
	case official == 0 && estimated == 0:
		return "unknown"
	case official > 0 && estimated > 0:
		return "mixed"
	case official > 0:
		return "official"
	default:
		return "estimated"
	}
}

func init() {
	// today/all/ls/whoami removed — merged into daily --today, monthly --all,
	// sessions --json, config show respectively.
	cli.Register(cmdSnapshot)
	cli.Register(cmdAlerts)
	cli.Register(cmdMCP)
	cli.Register(cmdWeb)
	cli.Register(cmdNotify)
	cli.Register(cmdAlias)
	cli.Register(cmdPricing)
	cli.Register(cmdConfig) // includes timezone, reset-hour, model-alias subcommands
	cli.Register(cmdWarehouse)
	cli.Register(cmdSessionsEnhanced)
	cli.Register(cmdDailyEnhanced)
}
