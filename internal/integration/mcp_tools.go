package integration

// MCP tool registry and dispatch. Kept separate from mcp.go so the transport
// and the tool surface can evolve independently.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/export"
	"github.com/garywhat/devinmonitor/internal/limit"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/trends"
)

func mcpTools() []mcpTool {
	var tools []mcpTool

	t1 := mcpTool{Name: "get_sessions", Description: "List all Devin CLI sessions with cost and token usage"}
	t1.InputSchema.Type = "object"
	t1.InputSchema.Properties = map[string]interface{}{}
	tools = append(tools, t1)

	t2 := mcpTool{Name: "get_session", Description: "Get detailed info for a single session by ID"}
	t2.InputSchema.Type = "object"
	t2.InputSchema.Properties = map[string]interface{}{
		"id": map[string]interface{}{"type": "string", "description": "Session ID"},
	}
	t2.InputSchema.Required = []string{"id"}
	tools = append(tools, t2)

	t3 := mcpTool{Name: "get_cost_summary", Description: "Get aggregated cost summary (today, week, month, total)"}
	t3.InputSchema.Type = "object"
	t3.InputSchema.Properties = map[string]interface{}{}
	tools = append(tools, t3)

	t4 := mcpTool{Name: "get_alerts", Description: "Get current alerts (budget thresholds, idle/ghost sessions)"}
	t4.InputSchema.Type = "object"
	t4.InputSchema.Properties = map[string]interface{}{}
	tools = append(tools, t4)

	t5 := mcpTool{Name: "get_blocks", Description: "List usage grouped by 5-hour billing window (blocks)"}
	t5.InputSchema.Type = "object"
	t5.InputSchema.Properties = map[string]interface{}{
		"window": map[string]interface{}{
			"type":        "string",
			"description": "Billing window length as a Go duration (e.g. \"5h\"); defaults to 5h",
		},
		"limitTokens": map[string]interface{}{
			"type":        "number",
			"description": "Token limit used to compute usedPercent; 0 or omitted means unset",
		},
		"activeOnly": map[string]interface{}{
			"type":        "boolean",
			"description": "Return only the currently open billing window",
		},
	}
	tools = append(tools, t5)

	t6 := mcpTool{Name: "get_limits", Description: "Get the current limit status: windows, usage percentage, pace and forecast"}
	t6.InputSchema.Type = "object"
	t6.InputSchema.Properties = map[string]interface{}{
		"limitTokens": map[string]interface{}{
			"type":        "number",
			"description": "Token limit applied to the local five-hour window; 0 or omitted means unset",
		},
	}
	tools = append(tools, t6)

	t7 := mcpTool{Name: "compare_periods", Description: "Compare usage between two periods (defaults to month over month)"}
	t7.InputSchema.Type = "object"
	t7.InputSchema.Properties = map[string]interface{}{
		"current": map[string]interface{}{
			"type":        "string",
			"description": "Current period as YYYY-MM; omit current and previous together for month over month",
		},
		"previous": map[string]interface{}{
			"type":        "string",
			"description": "Previous period as YYYY-MM; required whenever current is given",
		},
	}
	tools = append(tools, t7)

	t8 := mcpTool{Name: "list_reports", Description: "List the available report types and export formats"}
	t8.InputSchema.Type = "object"
	t8.InputSchema.Properties = map[string]interface{}{}
	tools = append(tools, t8)

	return tools
}

func handleToolCall(cmd *cobra.Command, req *rpcRequest) *rpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	_ = json.Unmarshal(req.Params, &params)

	r := openReader(cmd)
	defer r.Close()

	switch params.Name {
	case "get_sessions":
		ss, err := r.Sessions()
		if err != nil {
			return rpcErrorResp(req.ID, -32603, err.Error())
		}
		rows := report.BuildSessionRows(ss)
		items := make([]model.SessionListItem, 0, len(rows))
		for _, row := range rows {
			items = append(items, model.SessionListItem{
				ID:       row.ID,
				Title:    row.Title,
				Model:    row.Model,
				Project:  row.Project,
				Cost:     row.Cost,
				Tokens:   row.InputTok + row.OutputTok,
				Duration: report.FormatDur(row.Duration),
			})
		}
		return rpcResult(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": toJSON(items)},
			},
		})

	case "get_session":
		var args struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		if args.ID == "" {
			return rpcErrorResp(req.ID, -32602, "missing required parameter: id")
		}
		s, err := r.Session(args.ID)
		if err != nil {
			return rpcErrorResp(req.ID, -32603, err.Error())
		}
		return rpcResult(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": toJSON(s)},
			},
		})

	case "get_cost_summary":
		ss, err := r.Sessions()
		if err != nil {
			return rpcErrorResp(req.ID, -32603, err.Error())
		}
		sum := computeCostSummary(ss)
		return rpcResult(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": toJSON(sum)},
			},
		})

	case "get_alerts":
		ss, err := r.Sessions()
		if err != nil {
			return rpcErrorResp(req.ID, -32603, err.Error())
		}
		alerts := detectAlerts(ss)
		return rpcResult(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": toJSON(alerts)},
			},
		})

	case "get_blocks":
		var args struct {
			Window      string `json:"window"`
			LimitTokens int64  `json:"limitTokens"`
			ActiveOnly  bool   `json:"activeOnly"`
		}
		_ = json.Unmarshal(params.Arguments, &args)

		// Mirror `blocks --window`: an explicit duration wins, and a
		// non-positive one falls back to the package default rather than
		// inventing its own validation.
		window := limit.DefaultWindow
		if s := strings.TrimSpace(args.Window); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return rpcErrorResp(req.ID, -32602,
					fmt.Sprintf("invalid window: %q (want a duration like \"5h\")", args.Window))
			}
			if d > 0 {
				window = d
			}
		}

		ss, err := r.Sessions()
		if err != nil {
			return rpcErrorResp(req.ID, -32603, err.Error())
		}
		now := time.Now()
		blocks := limit.Identify(limit.FromSessions(ss), window, now)
		if args.ActiveOnly {
			// The open window is a property of "now", so it is resolved
			// against the full block list, exactly like `blocks --active`.
			if act := limit.Active(blocks); act != nil {
				blocks = []limit.Block{*act}
			} else {
				blocks = nil
			}
		}
		blocks = sortBlocksNewestFirst(blocks)
		return rpcResult(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": toJSON(limit.WireBlocks(blocks, now, args.LimitTokens))},
			},
		})

	case "get_limits":
		var args struct {
			LimitTokens int64 `json:"limitTokens"`
		}
		_ = json.Unmarshal(params.Arguments, &args)

		ss, err := r.Sessions()
		if err != nil {
			return rpcErrorResp(req.ID, -32603, err.Error())
		}
		snap := buildProtocolSnapshot(ss, time.Now(), protocolOpts{LimitTokens: args.LimitTokens}, config.Global())
		return rpcResult(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": toJSON(map[string]interface{}{
					"status": snap.Status,
					"limits": snap.Limits,
				})},
			},
		})

	case "compare_periods":
		var args struct {
			Current  string `json:"current"`
			Previous string `json:"previous"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		current := strings.TrimSpace(args.Current)
		previous := strings.TrimSpace(args.Previous)
		if (current == "") != (previous == "") {
			// Half a pair is ambiguous: with only one side given, the other
			// would silently default, which is never what the caller meant.
			missing := "previous"
			if previous != "" {
				missing = "current"
			}
			return rpcErrorResp(req.ID, -32602,
				fmt.Sprintf("both current and previous are required together (missing %s)", missing))
		}

		ss, err := r.Sessions()
		if err != nil {
			return rpcErrorResp(req.ID, -32603, err.Error())
		}

		var pc *model.PeriodComparison
		if current == "" {
			pc = trends.BuildMonthOverMonth(ss)
		} else {
			curStart, curEnd, err := parseMonthPeriod(current)
			if err != nil {
				return rpcErrorResp(req.ID, -32602,
					fmt.Sprintf("invalid current period: %q (want YYYY-MM)", args.Current))
			}
			prevStart, prevEnd, err := parseMonthPeriod(previous)
			if err != nil {
				return rpcErrorResp(req.ID, -32602,
					fmt.Sprintf("invalid previous period: %q (want YYYY-MM)", args.Previous))
			}
			pc = trends.BuildPeriodComparison(ss, curStart, curEnd, prevStart, prevEnd)
		}
		return rpcResult(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": toJSON(pc)},
			},
		})

	case "list_reports":
		// export.ReportTypes deliberately omits the session-oriented default,
		// so the full list is {sessions} ∪ ReportTypes with sessions first.
		types := make([]string, 0, len(export.ReportTypes)+1)
		seen := map[string]bool{}
		for _, t := range append([]string{"sessions"}, export.ReportTypes...) {
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			types = append(types, t)
		}
		return rpcResult(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": toJSON(map[string]interface{}{
					"reportTypes": types,
					"formats":     []string{"csv", "markdown", "html", "json"},
					"default":     "sessions",
					"notes": "Running `export` with no argument produces the `sessions` document; " +
						"add `--detailed` to include per-request detail for each session.",
				})},
			},
		})

	default:
		return rpcErrorResp(req.ID, -32601, "Unknown tool: "+params.Name)
	}
}

// ---- block JSON (mirrors `devinmonitor blocks --json`) ----

// sortBlocksNewestFirst returns a copy of bs ordered by StartTime descending,
// the order the CLI emits in `blocks --json`.
func sortBlocksNewestFirst(bs []limit.Block) []limit.Block {
	out := make([]limit.Block, len(bs))
	copy(out, bs)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartTime.After(out[j].StartTime)
	})
	return out
}

// parseMonthPeriod parses a "YYYY-MM" argument into its month range: the first
// of the month in local time, and the first of the following month as the
// exclusive end. Both bounds are local, matching how the CLI buckets months.
func parseMonthPeriod(s string) (time.Time, time.Time, error) {
	s = strings.TrimSpace(s)
	// Reject short/odd shapes up front: time.Parse would happily accept
	// "2026-1" for the "2006-01" layout.
	if len(s) != 7 || s[4] != '-' {
		return time.Time{}, time.Time{}, fmt.Errorf("want YYYY-MM")
	}
	t, err := time.ParseInLocation("2006-01", s, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.Local)
	return start, start.AddDate(0, 1, 0), nil
}
