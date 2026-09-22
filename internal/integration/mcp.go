package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
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
//
// It accepts both stdio framings:
//   - the MCP spec's `Content-Length: N\r\n\r\n<body>` framing, which real MCP
//     clients (Claude Desktop, Cursor, VS Code, ...) send, and
//   - bare newline-delimited JSON (one message per line) for convenience.
//
// Previously only the latter was handled, so a spec-framed request first had
// its `Content-Length: N` header parsed as a JSON message, emitting a spurious
// `-32700 Parse error` response before the real one.
func runMCPServer(cmd *cobra.Command) {
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

	dispatch := func(body []byte) {
		body = bytes.TrimSpace(body)
		if len(body) == 0 {
			return
		}
		parseError := func() {
			_ = encoder.Encode(rpcResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &rpcError{Code: -32700, Message: "Parse error"},
			})
		}

		// Batch request: a JSON array of messages. Responses are collected
		// into a single array, omitting notifications (which have none).
		if body[0] == '[' {
			var batch []json.RawMessage
			if err := json.Unmarshal(body, &batch); err != nil || len(batch) == 0 {
				parseError()
				return
			}
			out := make([]*rpcResponse, 0, len(batch))
			for _, raw := range batch {
				var req rpcRequest
				if err := json.Unmarshal(raw, &req); err != nil {
					out = append(out, &rpcResponse{
						JSONRPC: "2.0",
						ID:      nil,
						Error:   &rpcError{Code: -32700, Message: "Parse error"},
					})
					continue
				}
				if resp := handleMCPRequest(cmd, &req); resp != nil {
					out = append(out, resp)
				}
			}
			if len(out) > 0 {
				_ = encoder.Encode(out)
			}
			return
		}

		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			parseError()
			return
		}
		if resp := handleMCPRequest(cmd, &req); resp != nil {
			_ = encoder.Encode(resp)
		}
	}

	for {
		line, err := reader.ReadString('\n')
		if line == "" {
			if err != nil {
				return // EOF with nothing buffered.
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if err != nil {
				return
			}
			continue
		}

		if n, ok := contentLengthHeader(trimmed); ok {
			// Skip any remaining headers up to the blank separator line.
			for {
				h, herr := reader.ReadString('\n')
				if strings.TrimSpace(h) == "" || herr != nil {
					break
				}
			}
			if n > 0 {
				body := make([]byte, n)
				if _, rerr := io.ReadFull(reader, body); rerr != nil {
					return // truncated frame
				}
				dispatch(body)
			}
			if err != nil {
				return
			}
			continue
		}

		// Bare newline-delimited JSON message.
		dispatch([]byte(trimmed))
		if err != nil {
			return
		}
	}
}

// contentLengthHeader parses a `Content-Length: N` header line (header name
// matched case-insensitively). ok is false when the line is not that header.
func contentLengthHeader(line string) (int, bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return 0, false
	}
	if !strings.EqualFold(strings.TrimSpace(line[:i]), "Content-Length") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[i+1:]))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func handleMCPRequest(cmd *cobra.Command, req *rpcRequest) *rpcResponse {
	// JSON-RPC 2.0: a message without an `id` is a notification and MUST NOT
	// receive a response — not even for an unknown method. Replying with an
	// error (as the default branch used to) makes real MCP clients receive
	// spurious responses for e.g. `notifications/cancelled`.
	if len(req.ID) == 0 {
		return nil
	}

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

	case "ping":
		// Liveness check; the spec returns an empty result object.
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      rawToInterface(req.ID),
			Result:  map[string]interface{}{},
		}

	case "tools/list":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      rawToInterface(req.ID),
			Result: map[string]interface{}{
				"tools": mcpTools(),
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
