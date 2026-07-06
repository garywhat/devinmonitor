package project

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// ---- Project Cost Attribution (#96) ----

var cmdProjects = func() *cobra.Command {
	var attribution bool
	c := &cobra.Command{
		Use:   "projects",
		Short: "Show per-project usage (with --attribution for cost breakdown)",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			rows := report.BuildProjectRows(ss)
			if attribution {
				printProjectAttribution(rows)
			} else {
				printProjectsBasic(rows)
			}
		},
	}
	c.Flags().BoolVar(&attribution, "attribution", false, "show cost attribution as percentage of total")
	return c
}

func printProjectsBasic(rows []report.ProjectRow) {
	t := ui.NewTable(
		"Project", "Sessions", "Reqs", "Input", "Output", "Total", "Cost", "Model",
	).RightAlign(1, 2, 3, 4, 5)
	for _, row := range rows {
		t.Row(
			row.Name,
			fmt.Sprintf("%d", row.Sessions),
			fmt.Sprintf("%d", row.Requests),
			report.FormatTok(row.InputTok),
			report.FormatTok(row.OutputTok),
			report.FormatTok(row.InputTok+row.OutputTok+row.CacheRead+row.CacheWrite),
			report.FormatCost(row.Cost, row.IsFree),
			compactModels(row.Models),
		)
	}
	fmt.Println(t.String())
}

func printProjectAttribution(rows []report.ProjectRow) {
	var totalCost float64
	for _, row := range rows {
		totalCost += row.Cost
	}
	t := ui.NewTable(
		"Project", "Sessions", "Cost", "Cost%", "Share",
	).RightAlign(1, 2, 3, 4)
	for _, row := range rows {
		pct := 0.0
		if totalCost > 0 {
			pct = row.Cost / totalCost * 100
		}
		bar := ui.ProgressBar(pct, 30)
		t.Row(
			row.Name,
			fmt.Sprintf("%d", row.Sessions),
			fmt.Sprintf("$%.2f", row.Cost),
			fmt.Sprintf("%.1f%%", pct),
			bar,
		)
	}
	t.Row("TOTAL", "", fmt.Sprintf("$%.2f", totalCost), "100.0%", "")
	fmt.Println(t.String())
}

func compactModels(models []string) string {
	if len(models) == 0 {
		return "-"
	}
	return strings.Join(models, ", ")
}

// ---- Tool Cost Attribution (#98) ----

var cmdTools = func() *cobra.Command {
	var sessionID string
	var all bool
	c := &cobra.Command{
		Use:   "tools [--session <id>|--all]",
		Short: "Show tool cost attribution (cost distributed across tools)",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			if sessionID != "" {
				s, err := r.Session(sessionID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					os.Exit(1)
				}
				printToolAttribution([]model.Session{*s})
				return
			}
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			printToolAttribution(ss)
		},
	}
	c.Flags().StringVar(&sessionID, "session", "", "attribution for a specific session")
	c.Flags().BoolVar(&all, "all", true, "attribution across all sessions")
	return c
}

func printToolAttribution(ss []model.Session) {
	// Aggregate tool calls across sessions.
	toolCalls := map[string]int{}
	toolTokens := map[string]int64{}
	var totalCalls int
	var totalCost float64

	for _, s := range ss {
		cost, _ := report.SessionCost(&s)
		totalCost += cost
		for tool, count := range s.ToolCalls {
			toolCalls[tool] += count
			totalCalls += count
		}
		// Estimate token contribution per tool (proportional to call count).
		totalTokens := s.InputTokens + s.OutputTokens
		for tool, count := range s.ToolCalls {
			if s.AssistantCount > 0 {
				toolTokens[tool] += totalTokens * int64(count) / int64(s.AssistantCount)
			}
		}
	}

	if totalCalls == 0 {
		fmt.Println("No tool calls found.")
		return
	}

	type row struct {
		tool     string
		calls    int
		costShare float64
		tokens   int64
	}
	var rows []row
	for tool, calls := range toolCalls {
		// Distribute cost proportionally by call count.
		costShare := 0.0
		if totalCalls > 0 {
			costShare = totalCost * float64(calls) / float64(totalCalls)
		}
		rows = append(rows, row{tool, calls, costShare, toolTokens[tool]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].calls > rows[j].calls })

	t := ui.NewTable("Tool", "Calls", "Cost Share", "Tokens (est)").RightAlign(1, 2, 3)
	for _, r := range rows {
		t.Row(r.tool,
			fmt.Sprintf("%d", r.calls),
			fmt.Sprintf("$%.4f", r.costShare),
			report.FormatTok(r.tokens))
	}
	fmt.Println(t.String())
}

// ---- MCP Server Breakdown (#99) ----

var cmdMCPUsage = func() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp-stats",
		Short: "Break down usage by MCP server (mcp_call_tool, mcp_list_tools, mcp_read_resource)",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			mcpTools := []string{"mcp_call_tool", "mcp_list_tools", "mcp_read_resource"}
			serverCalls := map[string]int{}
			serverByTool := map[string]map[string]int{}

			for _, s := range ss {
				for _, m := range s.Messages {
					if m.Role != "assistant" {
						continue
					}
					for _, tc := range m.ToolCalls {
						if !contains(mcpTools, tc.Name) {
							continue
						}
						server := extractMCPServerName(tc.Arguments)
						if server == "" {
							server = "(unknown)"
						}
						serverCalls[server]++
						if serverByTool[server] == nil {
							serverByTool[server] = map[string]int{}
						}
						serverByTool[server][tc.Name]++
					}
				}
			}

			if len(serverCalls) == 0 {
				fmt.Println("No MCP tool calls found.")
				return
			}

			type row struct {
				server string
				calls  int
				byTool map[string]int
			}
			var rows []row
			for srv, calls := range serverCalls {
				rows = append(rows, row{srv, calls, serverByTool[srv]})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].calls > rows[j].calls })

			t := ui.NewTable("MCP Server", "Total Calls", "call_tool", "list_tools", "read_resource").RightAlign(1, 2, 3, 4)
			for _, r := range rows {
				t.Row(r.server,
					fmt.Sprintf("%d", r.calls),
					fmt.Sprintf("%d", r.byTool["mcp_call_tool"]),
					fmt.Sprintf("%d", r.byTool["mcp_list_tools"]),
					fmt.Sprintf("%d", r.byTool["mcp_read_resource"]))
			}
			fmt.Println(t.String())
		},
	}
}

// extractMCPServerName parses tool call arguments JSON to find server_name.
func extractMCPServerName(argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	// Try common field names.
	for _, key := range []string{"server_name", "serverName", "server", "name"} {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// ---- Shell Commands Breakdown (#100) ----

var cmdShellUsage = func() *cobra.Command {
	return &cobra.Command{
		Use:   "shell-usage",
		Short: "Break down exec/shell_command usage by command category",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			shellTools := []string{"exec", "shell_command", "run_command"}
			categoryCalls := map[string]int{}
			categoryCommands := map[string]map[string]bool{}

			for _, s := range ss {
				for _, m := range s.Messages {
					if m.Role != "assistant" {
						continue
					}
					for _, tc := range m.ToolCalls {
						if !contains(shellTools, tc.Name) {
							continue
						}
						cmdStr := extractShellCommand(tc.Arguments)
						category := categorizeCommand(cmdStr)
						categoryCalls[category]++
						if categoryCommands[category] == nil {
							categoryCommands[category] = map[string]bool{}
						}
						if cmdStr != "" {
							categoryCommands[category][firstWord(cmdStr)] = true
						}
					}
				}
			}

			if len(categoryCalls) == 0 {
				fmt.Println("No shell/exec tool calls found.")
				return
			}

			type row struct {
				category string
				calls    int
				commands []string
			}
			var rows []row
			for cat, calls := range categoryCalls {
				var cmds []string
				for c := range categoryCommands[cat] {
					cmds = append(cmds, c)
				}
				sort.Strings(cmds)
				rows = append(rows, row{cat, calls, cmds})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].calls > rows[j].calls })

			t := ui.NewTable("Category", "Calls", "Commands").RightAlign(1)
			for _, r := range rows {
				cmdList := strings.Join(r.commands, ", ")
				if len(cmdList) > 50 {
					cmdList = cmdList[:47] + "..."
				}
				t.Row(r.category, fmt.Sprintf("%d", r.calls), cmdList)
			}
			fmt.Println(t.String())
		},
	}
}

// extractShellCommand parses tool call arguments to find the command string.
func extractShellCommand(argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	for _, key := range []string{"command", "cmd", "shell_command", "script"} {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// categorizeCommand categorizes a shell command into a tool category.
func categorizeCommand(cmd string) string {
	cmd = strings.TrimSpace(strings.ToLower(cmd))
	// Strip leading env vars (e.g. "FOO=bar baz").
	for strings.Contains(cmd, "=") && !strings.HasPrefix(cmd, " ") {
		parts := strings.SplitN(cmd, " ", 2)
		if strings.Contains(parts[0], "=") && !isKnownCommand(parts[0]) {
			if len(parts) > 1 {
				cmd = parts[1]
			}
		} else {
			break
		}
	}
	first := firstWord(cmd)
	switch first {
	case "git":
		return "git"
	case "npm", "npx", "yarn", "pnpm", "bun", "node":
		return "node/js"
	case "go":
		return "go"
	case "python", "python3", "pip", "pip3", "poetry", "uv":
		return "python"
	case "docker", "docker-compose":
		return "docker"
	case "make":
		return "make"
	case "cargo", "rustc":
		return "rust"
	case "kubectl", "helm":
		return "k8s"
	case "terraform":
		return "terraform"
	case "ssh", "scp":
		return "ssh"
	case "curl", "wget":
		return "http"
	case "grep", "rg", "find", "fd", "sed", "awk":
		return "search"
	case "cat", "ls", "dir", "cp", "mv", "rm", "mkdir", "touch", "chmod":
		return "filesystem"
	case "echo", "printf", "export":
		return "shell-builtin"
	default:
		return "other"
	}
}

func isKnownCommand(s string) bool {
	known := []string{"git", "npm", "go", "python", "docker", "make", "cargo", "kubectl", "ssh", "curl"}
	for _, k := range known {
		if s == k {
			return true
		}
	}
	return false
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// ---- Per-Activity Breakdown (#101) ----

var cmdActivities = func() *cobra.Command {
	return &cobra.Command{
		Use:   "activities",
		Short: "Break down usage by activity type (coding, debugging, testing, etc.)",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			categoryStats := map[string]*model.TaskCategory{}
			for _, s := range ss {
				cat := categorizeSession(s)
				st := categoryStats[cat]
				if st == nil {
					st = &model.TaskCategory{Name: cat}
					categoryStats[cat] = st
				}
				st.Count++
				cost, _ := report.SessionCost(&s)
				st.Cost += cost
			}

			if len(categoryStats) == 0 {
				fmt.Println("No sessions found.")
				return
			}

			type row struct {
				category string
				count    int
				cost     float64
			}
			var rows []row
			for _, st := range categoryStats {
				rows = append(rows, row{st.Name, st.Count, st.Cost})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].cost > rows[j].cost })

			var totalCost float64
			var totalSessions int
			t := ui.NewTable("Activity", "Sessions", "Cost", "Avg/Sess").RightAlign(1, 2, 3)
			for _, r := range rows {
				avg := 0.0
				if r.count > 0 {
					avg = r.cost / float64(r.count)
				}
				t.Row(r.category,
					fmt.Sprintf("%d", r.count),
					fmt.Sprintf("$%.2f", r.cost),
					fmt.Sprintf("$%.2f", avg))
				totalCost += r.cost
				totalSessions += r.count
			}
			t.Row("TOTAL",
				fmt.Sprintf("%d", totalSessions),
				fmt.Sprintf("$%.2f", totalCost), "")
			fmt.Println(t.String())
		},
	}
}

// categorizeSession classifies a session by its work type based on tool usage
// and title/content keywords.
func categorizeSession(s model.Session) string {
	// Build a text blob from title and tool calls for keyword matching.
	blob := strings.ToLower(s.Title)
	for tool := range s.ToolCalls {
		blob += " " + tool
	}

	// Check for testing-related indicators.
	if strings.Contains(blob, "test") || strings.Contains(blob, "pytest") || strings.Contains(blob, "jest") {
		return "Testing"
	}
	// Check for debugging.
	if strings.Contains(blob, "debug") || strings.Contains(blob, "fix") || strings.Contains(blob, "bug") || strings.Contains(blob, "error") {
		return "Debugging"
	}
	// Check for refactoring.
	if strings.Contains(blob, "refactor") || strings.Contains(blob, "rename") || strings.Contains(blob, "restructure") {
		return "Refactoring"
	}
	// Check for documentation.
	if strings.Contains(blob, "doc") || strings.Contains(blob, "readme") || strings.Contains(blob, "comment") {
		return "Documentation"
	}
	// Check for deployment/CI.
	if strings.Contains(blob, "deploy") || strings.Contains(blob, "ci") || strings.Contains(blob, "release") || strings.Contains(blob, "goreleaser") {
		return "DevOps"
	}
	// Check for git/commit work.
	if strings.Contains(blob, "git") || strings.Contains(blob, "commit") {
		return "Git/VCS"
	}
	// Default: coding.
	return "Coding"
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
