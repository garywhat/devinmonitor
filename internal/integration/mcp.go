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
					"tools":     map[string]interface{}{},
					"resources": map[string]interface{}{},
				},
				"serverInfo": map[string]interface{}{
					"name":    "devinmonitor",
					"version": "1.0.0",
				},
				"instructions": "DevinMonitor exposes local Devin CLI usage: sessions, cost and token analytics, " +
					"5-hour billing windows and alerts. Tools query specific views; the devinmonitor:// " +
					"resources are read-only summaries. All data is local and nothing is uploaded.",
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

	case "resources/list":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      rawToInterface(req.ID),
			Result: map[string]interface{}{
				"resources": mcpResources(),
			},
		}

	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if params.URI == "" {
			return rpcErrorResp(req.ID, -32602, "missing required parameter: uri")
		}
		body, rerr := readMCPResource(cmd, params.URI)
		if rerr != nil {
			return &rpcResponse{
				JSONRPC: "2.0",
				ID:      rawToInterface(req.ID),
				Error:   rerr,
			}
		}
		return rpcResult(req.ID, map[string]interface{}{
			"contents": []map[string]interface{}{
				{"uri": params.URI, "mimeType": "text/plain", "text": body},
			},
		})

	default:
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      rawToInterface(req.ID),
			Error:   &rpcError{Code: -32601, Message: "Method not found: " + req.Method},
		}
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
