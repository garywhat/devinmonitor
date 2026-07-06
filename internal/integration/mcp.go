package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// ---- MCP Server (#83) ----

var cmdMCP = func() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: i18n.T("cmd.mcp"),
		Run: func(cmd *cobra.Command, args []string) {
			runMCPServer(cmd)
		},
	}
}

// JSON-RPC 2.0 types.

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpTool describes a tool exposed via MCP.
type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Type       string                 `json:"type"`
		Properties map[string]interface{} `json:"properties"`
		Required   []string               `json:"required,omitempty"`
	} `json:"inputSchema"`
}

// runMCPServer implements a minimal MCP server over stdio using JSON-RPC 2.0.
func runMCPServer(cmd *cobra.Command) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // 10MB max
	encoder := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			encoder.Encode(rpcResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &rpcError{Code: -32700, Message: "Parse error"},
			})
			continue
		}
		resp := handleMCPRequest(cmd, &req)
		if resp != nil {
			_ = encoder.Encode(resp)
		}
	}
}

func handleMCPRequest(cmd *cobra.Command, req *rpcRequest) *rpcResponse {
	switch req.Method {
	case "initialize":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      rawToInterface(req.ID),
			Result: map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{},
				},
				"serverInfo": map[string]interface{}{
					"name":    "devinmonitor",
					"version": "1.0.0",
				},
			},
		}

	case "notifications/initialized":
		// Notification — no response.
		return nil

	case "tools/list":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      rawToInterface(req.ID),
			Result: map[string]interface{}{
				"tools:": mcpTools(),
			},
		}

	case "tools/call":
		return handleToolCall(cmd, req)

	default:
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      rawToInterface(req.ID),
			Error:   &rpcError{Code: -32601, Message: "Method not found: " + req.Method},
		}
	}
}

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

	default:
		return rpcErrorResp(req.ID, -32601, "Unknown tool: "+params.Name)
	}
}

func rpcResult(id json.RawMessage, result interface{}) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      rawToInterface(id),
		Result:  result,
	}
}

func rpcErrorResp(id json.RawMessage, code int, msg string) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      rawToInterface(id),
		Error:   &rpcError{Code: code, Message: msg},
	}
}

// rawToInterface converts a json.RawMessage to a generic interface{},
// returning nil for empty/invalid input.
func rawToInterface(raw json.RawMessage) interface{} {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

// toJSON marshals v to indented JSON string, falling back to fmt.Sprint.
func toJSON(v interface{}) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// Ensure io is used (for potential future streaming).
var _ = io.EOF
