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
	// Data carries the structured detail some modern errors require, notably
	// `data.supported` on UnsupportedProtocolVersionError. Legacy errors leave
	// it nil, so their wire shape is byte-for-byte unchanged.
	Data interface{} `json:"data,omitempty"`
}

// ---- protocol generations ----
//
// This server is dual-era. It serves both
//
//   - the Modern generation (protocol revision 2026-07-28), which is stateless:
//     every request declares its own protocol version in `_meta`, every result
//     carries `resultType`, and list/read results carry cache hints; and
//   - the Legacy generation, which opens with an `initialize` handshake and is
//     served exactly as it was before this upgrade.
//
// The generation is selected per request:
//
//   - `initialize` always selects legacy semantics (the versioning spec assigns
//     that meaning to the method, and legacy clients have no fall-forward);
//   - a request declaring `_meta["io.modelcontextprotocol/protocolVersion"]` is
//     served statelessly under that modern revision;
//   - `server/discover` is always modern: it is the stdio probe a dual-era
//     client sends before it knows which generation it is talking to;
//   - anything else is legacy, because that is the only generation in which a
//     request without per-request metadata has meaning.
//
// See https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning
const (
	// modernProtocolVersion is the revision served with per-request metadata.
	modernProtocolVersion = "2026-07-28"
	// legacyProtocolVersion is the revision negotiated by the `initialize`
	// handshake. Unchanged so existing clients keep the contract they had.
	legacyProtocolVersion = "2024-11-05"

	// `_meta` keys reserved by the modern specification.
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// modernSupportedVersions lists the revisions this server serves statelessly,
// in preference order. It is what `server/discover` advertises and what an
// UnsupportedProtocolVersionError reports as `data.supported`.
//
// Legacy revisions are deliberately absent. `supported` tells a modern client
// which version to retry with through per-request `_meta`; asking for
// "2024-11-05" that way would demand handshake semantics the modern framing
// does not have. Handshake interoperability is advertised by answering
// `initialize`, not by this list.
var modernSupportedVersions = []string{modernProtocolVersion}

// Cache hints (milliseconds). The tool and resource *lists* are fixed for a
// release and carry no user data, so they are publicly cacheable for an hour.
// A resource *read* is this machine's usage data, so it is private and
// immediately stale.
const (
	listTTLMs     = int64(3600 * 1000)
	discoverTTLMs = int64(3600 * 1000)
	readTTLMs     = int64(0)
)

// mcpServerInfo identifies this server implementation. In the modern era it is
// carried in each result's `_meta` and in the `server/discover` response.
func mcpServerInfo() map[string]interface{} {
	return map[string]interface{}{
		"name":    "devinmonitor",
		"version": "1.0.0",
	}
}

// mcpCapabilities is the capability set advertised by both generations. Only
// the features this server actually implements are listed.
func mcpCapabilities() map[string]interface{} {
	return map[string]interface{}{
		"tools":     map[string]interface{}{},
		"resources": map[string]interface{}{},
	}
}

// mcpInstructions is the natural-language guidance attached to `initialize`
// (legacy) and `server/discover` (modern), so both eras describe the server the
// same way.
const mcpInstructions = "DevinMonitor exposes local Devin CLI usage: sessions, cost and token analytics, " +
	"5-hour billing windows and alerts. Tools query specific views; the devinmonitor:// " +
	"resources are read-only summaries. All data is local and nothing is uploaded."

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

	// `initialize` opens the legacy era whatever else the message carries.
	if req.Method == "initialize" {
		return handleLegacyRequest(cmd, req)
	}

	meta := requestMeta(req.Params)
	version, declared, wellFormed := declaredProtocolVersion(meta)

	// Modern (stateless) era: the request declares its own protocol version, or
	// it is the `server/discover` probe a dual-era client sends before it knows
	// which generation this server speaks.
	//
	// A declared version that is not a string is malformed (-32602). The other
	// reserved `_meta` fields are accepted but not required: no RPC this server
	// implements depends on a client capability, so
	// MissingRequiredClientCapabilityError (-32021) cannot arise, and requiring
	// them would reject probes that legitimately send only the version.
	if declared || req.Method == "server/discover" {
		if declared && !wellFormed {
			return rpcErrorResp(req.ID, -32602,
				"Invalid params: _meta."+metaProtocolVersion+" must be a string")
		}
		if declared && !modernVersionSupported(version) {
			return unsupportedProtocolVersionResp(req.ID, version)
		}
		return handleModernRequest(cmd, req)
	}

	return handleLegacyRequest(cmd, req)
}

// requestMeta decodes the reserved `_meta` keys from a request's params. It
// returns nil when the params carry no `_meta` object, which is how a legacy
// request (and any pre-handshake message) looks.
func requestMeta(params json.RawMessage) map[string]json.RawMessage {
	if len(params) == 0 {
		return nil
	}
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}
	return p.Meta
}

// declaredProtocolVersion reports the protocol version a request declares in
// `_meta`. present is false when the key is absent — the era discriminator.
// wellFormed is false when the key exists but is not a string, which the
// specification requires the server to reject as invalid params.
func declaredProtocolVersion(meta map[string]json.RawMessage) (version string, present, wellFormed bool) {
	raw, ok := meta[metaProtocolVersion]
	if !ok {
		return "", false, true
	}
	if err := json.Unmarshal(raw, &version); err != nil {
		return "", true, false
	}
	return version, true, true
}

// modernVersionSupported reports whether v is served statelessly.
func modernVersionSupported(v string) bool {
	for _, s := range modernSupportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// unsupportedProtocolVersionResp is the modern era's version-mismatch error
// (UnsupportedProtocolVersionError, -32022). `data.supported` lists the
// revisions the client may retry with.
func unsupportedProtocolVersionResp(id json.RawMessage, requested string) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      rawToInterface(id),
		Error: &rpcError{
			Code:    -32022,
			Message: "Unsupported protocol version",
			Data: map[string]interface{}{
				"supported": modernSupportedVersions,
				"requested": requested,
			},
		},
	}
}

// handleLegacyRequest serves one request under the negotiated handshake
// revision: the pre-existing behaviour, preserved verbatim so legacy clients
// see exactly the results they saw before the dual-era upgrade (no
// `resultType`, no cache hints, no `_meta`).
func handleLegacyRequest(cmd *cobra.Command, req *rpcRequest) *rpcResponse {
	switch req.Method {
	case "initialize":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      rawToInterface(req.ID),
			Result: map[string]interface{}{
				"protocolVersion": legacyProtocolVersion,
				"capabilities":    mcpCapabilities(),
				"serverInfo":      mcpServerInfo(),
				"instructions":    mcpInstructions,
			},
		}

	case "notifications/initialized":
		// Notification — no response.
		return nil

	case "ping":
		// Liveness check; the legacy spec returns an empty result object.
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
		return handleResourceRead(cmd, req)

	default:
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      rawToInterface(req.ID),
			Error:   &rpcError{Code: -32601, Message: "Method not found: " + req.Method},
		}
	}
}

// handleModernRequest serves one request statelessly under protocol revision
// 2026-07-28. Methods this revision removed — the `initialize` handshake and
// `ping` — have no modern meaning, so they are unknown here; that is what
// distinguishes a modern reply from an era-ambiguous legacy one.
func handleModernRequest(cmd *cobra.Command, req *rpcRequest) *rpcResponse {
	switch req.Method {
	case "server/discover":
		// MUST be implemented. Beyond version selection this is the stdio
		// backward-compatibility probe, so it must answer even when it is the
		// very first message of the process.
		return modernResult(req.ID, map[string]interface{}{
			"supportedVersions": modernSupportedVersions,
			"capabilities":      mcpCapabilities(),
			"instructions":      mcpInstructions,
			"ttlMs":             discoverTTLMs,
			"cacheScope":        "public",
		})

	case "tools/list":
		return modernResult(req.ID, map[string]interface{}{
			"tools":      mcpTools(),
			"ttlMs":      listTTLMs,
			"cacheScope": "public",
		})

	case "tools/call":
		// Tool results are not cacheable: they embed live usage data.
		return modernizeResult(handleToolCall(cmd, req), nil)

	case "resources/list":
		return modernResult(req.ID, map[string]interface{}{
			"resources":  mcpResources(),
			"ttlMs":      listTTLMs,
			"cacheScope": "public",
		})

	case "resources/read":
		return modernizeResult(handleResourceRead(cmd, req),
			&cacheHint{ttlMs: readTTLMs, cacheScope: "private"})

	default:
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      rawToInterface(req.ID),
			Error: &rpcError{
				Code: -32601,
				Message: "Method not found: " + req.Method +
					" (not part of protocol version " + modernProtocolVersion + ")",
			},
		}
	}
}

// handleResourceRead implements resources/read for both generations; only the
// surrounding result shape (resultType, cache hints) differs.
func handleResourceRead(cmd *cobra.Command, req *rpcRequest) *rpcResponse {
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
}

// cacheHint is the caching pair every cacheable modern result must carry.
type cacheHint struct {
	ttlMs      int64
	cacheScope string
}

// modernResult builds a modern result: the method's own fields plus the
// required `resultType` and the self-identifying `_meta.serverInfo`.
func modernResult(id json.RawMessage, fields map[string]interface{}) *rpcResponse {
	fields["resultType"] = "complete"
	fields["_meta"] = map[string]interface{}{metaServerInfo: mcpServerInfo()}
	return rpcResult(id, fields)
}

// modernizeResult upgrades a legacy-shaped result to the modern shape, adding
// the cache hints when hint is non-nil. Errors pass through untouched:
// `resultType` describes results, not errors, and an error must never be made
// to look like a successful result.
func modernizeResult(resp *rpcResponse, hint *cacheHint) *rpcResponse {
	if resp == nil || resp.Error != nil {
		return resp
	}
	fields, ok := resp.Result.(map[string]interface{})
	if !ok {
		return resp
	}
	if hint != nil {
		fields["ttlMs"] = hint.ttlMs
		fields["cacheScope"] = hint.cacheScope
	}
	fields["resultType"] = "complete"
	fields["_meta"] = map[string]interface{}{metaServerInfo: mcpServerInfo()}
	return resp
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
