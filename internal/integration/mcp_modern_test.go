package integration

// Dual-era conformance tests for the MCP handler.
//
// They pin the two generations separately: a modern request (protocol revision
// 2026-07-28, version carried per-request in `_meta`) must be served
// statelessly with `resultType` and cache hints, while a legacy request (the
// `initialize` handshake, or simply no modern `_meta`) must keep the exact
// result shape it had before the upgrade. The legacy assertions are the
// pre-existing contract, not a weakened version of it.
//
// The end-to-end counterpart is scripts/mcp-conformance.sh, which drives the
// same paths over real stdio; these tests lock the handler in isolation.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// ---- helpers ----

// modernParams builds request params carrying the required per-request `_meta`
// for the given protocol version, plus any method-specific fields.
func modernParams(t *testing.T, version string, extra map[string]interface{}) json.RawMessage {
	t.Helper()
	params := map[string]interface{}{
		"_meta": map[string]interface{}{
			metaProtocolVersion:    version,
			metaClientCapabilities: map[string]interface{}{},
			metaClientInfo:         map[string]interface{}{"name": "mcp-modern-test", "version": "1.0.0"},
		},
	}
	for k, v := range extra {
		params[k] = v
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return raw
}

// callMCP routes one request through the handler and returns the decoded
// response document ({"result": ...} or {"error": ...}).
func callMCP(t *testing.T, cmd *cobra.Command, method string, params json.RawMessage) map[string]interface{} {
	t.Helper()
	resp := handleMCPRequest(cmd, &rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  params,
	})
	if resp == nil {
		t.Fatalf("%s: handler returned no response for a request with an id", method)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("%s: marshal response: %v", method, err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: response is not a JSON object: %v (%s)", method, err, raw)
	}
	return doc
}

// resultOf returns the result object, failing if the reply carried an error.
func resultOf(t *testing.T, what string, doc map[string]interface{}) map[string]interface{} {
	t.Helper()
	if errDoc, ok := doc["error"]; ok {
		t.Fatalf("%s: expected a result, got error %v", what, errDoc)
	}
	res, ok := doc["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s: reply carries no result object: %v", what, doc)
	}
	return res
}

// errorOf returns (code, data), failing if the reply carried a result.
func errorOf(t *testing.T, what string, doc map[string]interface{}) (float64, map[string]interface{}) {
	t.Helper()
	if _, ok := doc["result"]; ok {
		t.Fatalf("%s: expected an error, got a result: %v", what, doc)
	}
	errDoc, ok := doc["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s: reply carries no error object: %v", what, doc)
	}
	code, ok := errDoc["code"].(float64)
	if !ok {
		t.Fatalf("%s: error code is not a number: %v", what, errDoc)
	}
	data, _ := errDoc["data"].(map[string]interface{})
	return code, data
}

// requireResultType asserts the modern `resultType: "complete"` marker.
func requireResultType(t *testing.T, what string, res map[string]interface{}) {
	t.Helper()
	if got := res["resultType"]; got != "complete" {
		t.Errorf("%s: result.resultType = %v, want \"complete\"", what, got)
	}
}

// requireServerInfo asserts the self-identifying modern `_meta.serverInfo`.
func requireServerInfo(t *testing.T, what string, res map[string]interface{}) {
	t.Helper()
	meta, ok := res["_meta"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s: result._meta is missing or not an object: %v", what, res["_meta"])
	}
	info, ok := meta[metaServerInfo].(map[string]interface{})
	if !ok {
		t.Fatalf("%s: result._meta[%s] is missing or not an object", what, metaServerInfo)
	}
	if info["name"] != "devinmonitor" {
		t.Errorf("%s: serverInfo.name = %v, want \"devinmonitor\"", what, info["name"])
	}
}

// requireCacheHints asserts the required caching pair and its values.
func requireCacheHints(t *testing.T, what string, res map[string]interface{}, wantScope string) {
	t.Helper()
	ttl, ok := res["ttlMs"].(float64)
	if !ok {
		t.Errorf("%s: result.ttlMs is missing or not a number: %v", what, res["ttlMs"])
	} else if ttl < 0 {
		t.Errorf("%s: result.ttlMs = %v, must be >= 0", what, ttl)
	}
	if got := res["cacheScope"]; got != wantScope {
		t.Errorf("%s: result.cacheScope = %v, want %q", what, got, wantScope)
	}
}

// requireNoModernFields asserts the legacy result shape: no `resultType`, no
// cache hints, no per-result `_meta`. Legacy clients must keep seeing exactly
// what they saw before the dual-era upgrade.
func requireNoModernFields(t *testing.T, what string, res map[string]interface{}) {
	t.Helper()
	for _, key := range []string{"resultType", "ttlMs", "cacheScope", "_meta"} {
		if v, ok := res[key]; ok {
			t.Errorf("%s: legacy result carries modern field %q = %v", what, key, v)
		}
	}
}

// ---- server/discover ----

func TestModernDiscover(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}
	doc := callMCP(t, cmd, "server/discover", modernParams(t, modernProtocolVersion, nil))
	res := resultOf(t, "server/discover", doc)

	requireResultType(t, "server/discover", res)
	requireServerInfo(t, "server/discover", res)
	requireCacheHints(t, "server/discover", res, "public")

	versions, ok := res["supportedVersions"].([]interface{})
	if !ok || len(versions) == 0 {
		t.Fatalf("server/discover: supportedVersions is missing or empty: %v", res["supportedVersions"])
	}
	found := false
	for _, v := range versions {
		if v == modernProtocolVersion {
			found = true
		}
	}
	if !found {
		t.Errorf("server/discover: supportedVersions %v does not include %q", versions, modernProtocolVersion)
	}

	caps, ok := res["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatalf("server/discover: capabilities is missing or not an object: %v", res["capabilities"])
	}
	for _, want := range []string{"tools", "resources"} {
		if _, ok := caps[want]; !ok {
			t.Errorf("server/discover: capabilities has no %q", want)
		}
	}
	if instr, ok := res["instructions"].(string); !ok || strings.TrimSpace(instr) == "" {
		t.Errorf("server/discover: instructions is missing or empty: %v", res["instructions"])
	}
}

// TestModernDiscoverProbeWithoutMeta pins the stdio backward-compatibility
// probe: a dual-era client's first message must get a DiscoverResult, not a
// "missing _meta" rejection, or it cannot tell this server is modern at all.
func TestModernDiscoverProbeWithoutMeta(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}
	doc := callMCP(t, cmd, "server/discover", nil)
	res := resultOf(t, "server/discover (no _meta probe)", doc)
	requireResultType(t, "server/discover (no _meta probe)", res)
	if _, ok := res["supportedVersions"].([]interface{}); !ok {
		t.Errorf("probe: supportedVersions missing: %v", res["supportedVersions"])
	}
}

// ---- modern requests need no handshake ----

func TestModernToolsListWithoutHandshake(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}
	// No initialize, no notifications/initialized, no session: the request is
	// the first and only message the server sees.
	doc := callMCP(t, cmd, "tools/list", modernParams(t, modernProtocolVersion, nil))
	res := resultOf(t, "modern tools/list", doc)

	requireResultType(t, "modern tools/list", res)
	requireServerInfo(t, "modern tools/list", res)
	requireCacheHints(t, "modern tools/list", res, "public")

	tools, ok := res["tools"].([]interface{})
	if !ok || len(tools) != 8 {
		t.Fatalf("modern tools/list: expected 8 tools, got %v", res["tools"])
	}
}

func TestModernResourcesList(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}
	doc := callMCP(t, cmd, "resources/list", modernParams(t, modernProtocolVersion, nil))
	res := resultOf(t, "modern resources/list", doc)

	requireResultType(t, "modern resources/list", res)
	requireServerInfo(t, "modern resources/list", res)
	requireCacheHints(t, "modern resources/list", res, "public")

	resources, ok := res["resources"].([]interface{})
	if !ok || len(resources) != 4 {
		t.Fatalf("modern resources/list: expected 4 resources, got %v", res["resources"])
	}
}

func TestModernResourcesReadIsPrivate(t *testing.T) {
	cmd := newResourceCommand(t)
	doc := callMCP(t, cmd, "resources/read",
		modernParams(t, modernProtocolVersion, map[string]interface{}{"uri": resourceSummary}))
	res := resultOf(t, "modern resources/read", doc)

	requireResultType(t, "modern resources/read", res)
	requireServerInfo(t, "modern resources/read", res)
	// Local usage data: private and immediately stale.
	requireCacheHints(t, "modern resources/read", res, "private")
	if ttl, ok := res["ttlMs"].(float64); ok && ttl != 0 {
		t.Errorf("modern resources/read: ttlMs = %v, want 0 (local data is immediately stale)", ttl)
	}

	contents, ok := res["contents"].([]interface{})
	if !ok || len(contents) == 0 {
		t.Fatalf("modern resources/read: contents is missing or empty: %v", res["contents"])
	}
}

func TestModernToolCallCarriesResultType(t *testing.T) {
	cmd := newResourceCommand(t)
	doc := callMCP(t, cmd, "tools/call", modernParams(t, modernProtocolVersion,
		map[string]interface{}{"name": "get_sessions", "arguments": map[string]interface{}{}}))
	res := resultOf(t, "modern tools/call", doc)

	requireResultType(t, "modern tools/call", res)
	requireServerInfo(t, "modern tools/call", res)
	// Tool results embed live data and are not cacheable.
	if _, ok := res["ttlMs"]; ok {
		t.Errorf("modern tools/call: tool results must not carry cache hints: %v", res)
	}

	content, ok := res["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("modern tools/call: content is missing or empty: %v", res["content"])
	}
	first, _ := content[0].(map[string]interface{})
	text, _ := first["text"].(string)
	var parsed interface{}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("modern tools/call: content[0].text is not valid JSON: %v", err)
	}
}

// ---- version negotiation errors ----

func TestModernUnsupportedProtocolVersion(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}
	doc := callMCP(t, cmd, "tools/list", modernParams(t, "1900-01-01", nil))
	code, data := errorOf(t, "unsupported version", doc)

	if code != -32022 {
		t.Errorf("unsupported version: error code = %v, want -32022", code)
	}
	if data == nil {
		t.Fatalf("unsupported version: error carries no data object")
	}
	if got := data["requested"]; got != "1900-01-01" {
		t.Errorf("unsupported version: data.requested = %v, want \"1900-01-01\"", got)
	}
	supported, ok := data["supported"].([]interface{})
	if !ok || len(supported) == 0 {
		t.Fatalf("unsupported version: data.supported is missing or empty: %v", data["supported"])
	}
	found := false
	for _, v := range supported {
		if v == modernProtocolVersion {
			found = true
		}
	}
	if !found {
		t.Errorf("unsupported version: data.supported %v does not include %q", supported, modernProtocolVersion)
	}
}

// TestModernUnsupportedVersionOnDiscover covers the probe path: a dual-era
// client that probes with a version we do not speak must get the recognized
// modern error (so it retries) rather than a legacy fallback.
func TestModernUnsupportedVersionOnDiscover(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}
	doc := callMCP(t, cmd, "server/discover", modernParams(t, "2025-11-25", nil))
	code, _ := errorOf(t, "unsupported version on discover", doc)
	if code != -32022 {
		t.Errorf("unsupported version on discover: error code = %v, want -32022", code)
	}
}

func TestModernMalformedProtocolVersion(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}
	raw, err := json.Marshal(map[string]interface{}{
		"_meta": map[string]interface{}{metaProtocolVersion: 20260728},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	doc := callMCP(t, cmd, "tools/list", raw)
	code, _ := errorOf(t, "malformed version", doc)
	if code != -32602 {
		t.Errorf("malformed version: error code = %v, want -32602 (invalid params)", code)
	}
}

// ---- revision removals are honoured per era ----

func TestModernRemovedMethodsAreUnknown(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}
	for _, method := range []string{"ping", "notifications/initialized"} {
		doc := callMCP(t, cmd, method, modernParams(t, modernProtocolVersion, nil))
		code, _ := errorOf(t, "modern "+method, doc)
		if code != -32601 {
			t.Errorf("modern %s: error code = %v, want -32601 (removed in %s)",
				method, code, modernProtocolVersion)
		}
	}
}

// ---- legacy era is intact ----

func TestLegacyInitializeStillWorks(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}
	doc := callMCP(t, cmd, "initialize", json.RawMessage(
		`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"legacy-client","version":"1.0"}}`))
	res := resultOf(t, "legacy initialize", doc)

	if got := res["protocolVersion"]; got != legacyProtocolVersion {
		t.Errorf("legacy initialize: protocolVersion = %v, want %q", got, legacyProtocolVersion)
	}
	caps, ok := res["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatalf("legacy initialize: capabilities missing: %v", res["capabilities"])
	}
	for _, want := range []string{"tools", "resources"} {
		if _, ok := caps[want]; !ok {
			t.Errorf("legacy initialize: capabilities has no %q", want)
		}
	}
	info, ok := res["serverInfo"].(map[string]interface{})
	if !ok || info["name"] != "devinmonitor" {
		t.Errorf("legacy initialize: serverInfo = %v, want name devinmonitor", res["serverInfo"])
	}
	if instr, ok := res["instructions"].(string); !ok || strings.TrimSpace(instr) == "" {
		t.Errorf("legacy initialize: instructions missing or empty: %v", res["instructions"])
	}
	requireNoModernFields(t, "legacy initialize", res)
}

// TestLegacyRequestsKeepLegacyShape is the negative control for the modern
// upgrade: without modern `_meta` every result stays exactly as it was, and
// `ping` — removed in the modern revision — still answers.
func TestLegacyRequestsKeepLegacyShape(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}

	listDoc := callMCP(t, cmd, "tools/list", nil)
	listRes := resultOf(t, "legacy tools/list", listDoc)
	requireNoModernFields(t, "legacy tools/list", listRes)
	if tools, ok := listRes["tools"].([]interface{}); !ok || len(tools) != 8 {
		t.Errorf("legacy tools/list: expected 8 tools, got %v", listRes["tools"])
	}

	pingDoc := callMCP(t, cmd, "ping", nil)
	pingRes := resultOf(t, "legacy ping", pingDoc)
	if len(pingRes) != 0 {
		t.Errorf("legacy ping: result = %v, want an empty object", pingRes)
	}
}

// TestLegacyProgressTokenIsNotModernMeta guards the era discriminator: the
// legacy revision also has a `_meta` (for `progressToken`), so only the modern
// protocol-version key may switch a request to modern semantics.
func TestLegacyProgressTokenIsNotModernMeta(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}
	raw, err := json.Marshal(map[string]interface{}{
		"_meta": map[string]interface{}{"progressToken": "legacy-token"},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	doc := callMCP(t, cmd, "tools/list", raw)
	res := resultOf(t, "legacy tools/list with progressToken", doc)
	requireNoModernFields(t, "legacy tools/list with progressToken", res)
}

// TestBothErasOnOneConnection proves the eras are independent: the handshake
// does not put the process into a mode that a later modern request inherits.
func TestBothErasOnOneConnection(t *testing.T) {
	cmd := &cobra.Command{Use: "mcp"}

	initDoc := callMCP(t, cmd, "initialize", json.RawMessage(`{"protocolVersion":"2024-11-05"}`))
	initRes := resultOf(t, "initialize", initDoc)
	requireNoModernFields(t, "initialize", initRes)

	modernDoc := callMCP(t, cmd, "tools/list", modernParams(t, modernProtocolVersion, nil))
	modernRes := resultOf(t, "modern tools/list after initialize", modernDoc)
	requireResultType(t, "modern tools/list after initialize", modernRes)
	requireCacheHints(t, "modern tools/list after initialize", modernRes, "public")

	legacyDoc := callMCP(t, cmd, "tools/list", nil)
	legacyRes := resultOf(t, "legacy tools/list after modern request", legacyDoc)
	requireNoModernFields(t, "legacy tools/list after modern request", legacyRes)
}
