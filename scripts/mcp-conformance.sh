#!/usr/bin/env bash
#
# mcp-conformance.sh — black-box MCP conformance suite for the devinmonitor CLI.
#
# Drives `devinmonitor mcp` over real stdio against the real binary: MCP stdio
# framing (Content-Length), JSON-RPC 2.0 semantics (notifications, batches,
# errors), the advertised tool/resource surface, and CLI<->MCP parity for
# `get_blocks`.
#
# The server is dual-era, so the suite covers BOTH protocol generations and
# reports a per-generation case count at the end:
#
#   - legacy (handshake, protocol revision 2024-11-05): `initialize`,
#     `notifications/initialized`, `ping`, and results whose shape carries no
#     modern fields. These are the original assertions, unchanged.
#   - modern (stateless, protocol revision 2026-07-28): per-request `_meta`
#     carrying the version, `server/discover`, `resultType: "complete"` on every
#     result, `ttlMs` + `cacheScope` on list/read results, and
#     UnsupportedProtocolVersionError (-32022) with `data.supported` on an
#     unknown version. Methods the modern revision removed (`ping`, the
#     `initialize` handshake) must not be served under modern semantics.
#
# These are end-to-end checks on purpose. The interop bugs this locks down
# (ignoring the spec's Content-Length framing so every reply was preceded by a
# spurious -32700; answering JSON-RPC notifications; a modern client failing
# because the server only spoke the removed handshake) live in the transport
# loop and the era router, so handler-level Go unit tests cannot catch a
# regression in them.
#
# Usage:
#   bash scripts/mcp-conformance.sh [path-to-binary]
#
# The binary defaults to /tmp/dm-mcp-conformance; if it is missing, it is built
# with `go build -o "$BIN" .` from the repo root (resolved relative to this
# script, so the script works from any cwd).
#
# Requires bash and python3 (the JSON parser/assertion engine). Fully offline.
# Exits 0 when every check passed (skips are not failures), 1 otherwise. An era
# with zero passing cases is a failure: the point of this suite is that both
# generations are exercised.
set -uo pipefail

# ---------------------------------------------------------------------------
# Paths and target binary
# ---------------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

BIN="${1:-/tmp/dm-mcp-conformance}"

# ---------------------------------------------------------------------------
# Colours
# ---------------------------------------------------------------------------

if [[ -n "${NO_COLOR:-}" ]]; then
  C_RED=''; C_GREEN=''; C_YELLOW=''; C_RESET=''
else
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RESET=$'\033[0m'
fi

# ---------------------------------------------------------------------------
# Counters and result reporting
# ---------------------------------------------------------------------------
#
# ERA tracks which protocol generation the check currently running belongs to,
# so the summary can prove both were exercised. Cases set it at their top with
# set_era; it defaults to legacy because that was the only era before the
# dual-era upgrade.

ERA="legacy"
set_era() { ERA="$1"; }

PASSED=0
FAILED=0
SKIPPED=0

LEGACY_PASSED=0; LEGACY_FAILED=0; LEGACY_SKIPPED=0
MODERN_PASSED=0; MODERN_FAILED=0; MODERN_SKIPPED=0

pass() {
  printf '%sPASS%s  %s\n' "$C_GREEN" "$C_RESET" "$1"
  PASSED=$((PASSED + 1))
  if [[ "$ERA" == "modern" ]]; then MODERN_PASSED=$((MODERN_PASSED + 1)); else LEGACY_PASSED=$((LEGACY_PASSED + 1)); fi
}
skip() {
  printf '%sSKIP%s  %s\n' "$C_YELLOW" "$C_RESET" "$1"
  SKIPPED=$((SKIPPED + 1))
  if [[ "$ERA" == "modern" ]]; then MODERN_SKIPPED=$((MODERN_SKIPPED + 1)); else LEGACY_SKIPPED=$((LEGACY_SKIPPED + 1)); fi
}

# fail NAME — prints the exact request that was sent and the exact reply that
# came back, so a failure is diagnosable without re-running anything by hand.
fail() {
  printf '%sFAIL%s  %s\n' "$C_RED" "$C_RESET" "$1"
  printf '        payload: %s\n' "${REQ:-(none)}"
  printf '        reply:   %s\n' "${REPLY_OUT:-(no output on stdout)}"
  if [[ -n "${REPLY_ERR:-}" ]]; then
    printf '        stderr:  %s\n' "$REPLY_ERR"
  fi
  FAILED=$((FAILED + 1))
  if [[ "$ERA" == "modern" ]]; then MODERN_FAILED=$((MODERN_FAILED + 1)); else LEGACY_FAILED=$((LEGACY_FAILED + 1)); fi
  return 0
}

# MODERN_META is the per-request metadata every modern request must carry: the
# protocol version, the client capabilities, and (SHOULD) the client identity.
# It is spliced into the handmade `params` objects below.
MODERN_META='"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"mcp-conformance","version":"1.0"},"io.modelcontextprotocol/clientCapabilities":{}}'

# ---------------------------------------------------------------------------
# Prerequisites
# ---------------------------------------------------------------------------

if ! command -v python3 >/dev/null 2>&1; then
  printf '%sSKIP%s  python3 not found on PATH.\n' "$C_YELLOW" "$C_RESET"
  printf '      This suite uses python3 as its JSON parser/assertion engine and is fully offline.\n'
  printf '      Install python3 (>= 3.6) and re-run: bash scripts/mcp-conformance.sh\n'
  exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Hard timeout for every server invocation, so a regression in the read loop
# cannot hang the suite. `timeout` is GNU coreutils (often `gtimeout` on macOS).
TIMEOUT_BIN="$(command -v timeout 2>/dev/null || command -v gtimeout 2>/dev/null || true)"

run_bin() { # arbitrary invocation of the CLI, hard-timeout guarded
  if [[ -n "$TIMEOUT_BIN" ]]; then
    "$TIMEOUT_BIN" 10 "$BIN" "$@"
  else
    "$BIN" "$@"
  fi
}

run_server() { run_bin mcp; }

if [[ ! -x "$BIN" ]]; then
  printf 'building %s (go build -o %s . in %s)\n' "$BIN" "$BIN" "$REPO_ROOT"
  if ! (cd "$REPO_ROOT" && go build -o "$BIN" .); then
    printf '%sFAIL%s  could not build the binary at %s\n' "$C_RED" "$C_RESET" "$BIN"
    exit 1
  fi
fi

printf 'MCP conformance against: %s\n' "$BIN"
printf 'repo root:               %s\n\n' "$REPO_ROOT"

# ---------------------------------------------------------------------------
# Assertion engine (python3)
# ---------------------------------------------------------------------------

cat > "$TMP/check.py" <<'PYEOF'
"""JSON assertion engine for scripts/mcp-conformance.sh.

Reads the server's reply (raw stdout text) on stdin; the check to run comes
from argv. Prints nothing and exits 0 when the check holds, prints a one-line
reason and exits 1 when it does not, and exits 3 to request a SKIP.
"""

import json
import re
import sys

# The modern revision this server is expected to speak statelessly.
MODERN_VERSION = "2026-07-28"


def die(msg):
    sys.stdout.write(str(msg) + "\n")
    sys.exit(1)


def need(cond, msg):
    if not cond:
        die(msg)


def preview(value, limit=300):
    text = value if isinstance(value, str) else json.dumps(value)
    return text if len(text) <= limit else text[:limit] + "..."


def tool_text(doc):
    """content[0].text of a tools/call result, or die explaining why not."""
    need(isinstance(doc, dict), "reply is not a JSON object")
    err = doc.get("error")
    if isinstance(err, dict):
        die("expected a result but got error %s: %s" % (err.get("code"), err.get("message")))
    res = doc.get("result")
    need(isinstance(res, dict), "reply carries no result object")
    content = res.get("content")
    need(isinstance(content, list) and content, "result.content is missing or empty")
    first = content[0]
    need(isinstance(first, dict), "result.content[0] is not an object")
    text = first.get("text")
    need(isinstance(text, str) and text.strip() != "",
         "result.content[0].text is missing or empty")
    return text


def text_as_json(text, what):
    try:
        return json.loads(text)
    except Exception as exc:
        die("%s is not valid JSON (%s): %s" % (what, exc, preview(text)))


raw = sys.stdin.read()
mode = sys.argv[1]
extra = sys.argv[2:]

try:
    doc = json.loads(raw)
except Exception as exc:
    die("reply is not valid JSON: %s — %s" % (exc, preview(raw)))


# ---- 1. initialize --------------------------------------------------------

if mode == "initialize":
    need(isinstance(doc, dict), "reply is not a JSON object")
    res = doc.get("result")
    need(isinstance(res, dict), "initialize returned no result object: %s" % preview(doc))

    pv = res.get("protocolVersion")
    need(isinstance(pv, str) and pv.strip() != "", "result.protocolVersion is missing or empty")

    caps = res.get("capabilities")
    need(isinstance(caps, dict), "result.capabilities is missing or not an object")
    need("tools" in caps, "result.capabilities has no 'tools'")
    need("resources" in caps, "result.capabilities has no 'resources'")

    info = res.get("serverInfo")
    need(isinstance(info, dict), "result.serverInfo is missing or not an object")
    need(info.get("name") == "devinmonitor",
         "result.serverInfo.name is %r, expected 'devinmonitor'" % info.get("name"))

    instr = res.get("instructions")
    need(isinstance(instr, str) and instr.strip() != "",
         "result.instructions is missing, empty or not a string")
    sys.exit(0)


# ---- 2. framing -----------------------------------------------------------

if mode == "framing_ok":
    # Bash has already checked "exactly one line" and "no -32700"; this confirms
    # that one line is a well-formed, non-error JSON-RPC response.
    need(isinstance(doc, dict), "framed reply is not a single JSON object")
    need("error" not in doc, "framed reply carried an error: %s" % preview(doc))
    need(isinstance(doc.get("result"), dict), "framed reply has no result object")
    sys.exit(0)


# ---- 3. tools/list --------------------------------------------------------

if mode == "tools_list":
    res = doc.get("result")
    need(isinstance(res, dict), "tools/list returned no result object: %s" % preview(doc))
    tools = res.get("tools")
    need(isinstance(tools, list), "result.tools is missing or not an array")

    required = [
        "get_sessions", "get_session", "get_cost_summary", "get_alerts",
        "get_blocks", "get_limits", "compare_periods", "list_reports",
    ]
    present = [t.get("name") for t in tools if isinstance(t, dict)]
    missing = [n for n in required if n not in present]
    need(not missing,
         "missing tools: %s (advertised: %s)" % (", ".join(missing), ", ".join(str(p) for p in present)))

    bad_schema = [t.get("name") for t in tools
                  if not isinstance(t.get("inputSchema"), dict)
                  or t["inputSchema"].get("type") != "object"]
    need(not bad_schema,
         "inputSchema.type != 'object' for: %s" % ", ".join(str(n) for n in bad_schema))
    sys.exit(0)


# ---- 4. resources/list ----------------------------------------------------

if mode == "resources_list":
    res = doc.get("result")
    need(isinstance(res, dict), "resources/list returned no result object: %s" % preview(doc))
    resources = res.get("resources")
    need(isinstance(resources, list), "result.resources is missing or not an array")

    want = ["devinmonitor://summary", "devinmonitor://models",
            "devinmonitor://blocks", "devinmonitor://alerts"]
    got = [r.get("uri") for r in resources if isinstance(r, dict)]
    need(len(got) == len(want),
         "expected exactly %d resources, got %d: %s" % (len(want), len(got), ", ".join(str(g) for g in got)))
    need(sorted(got) == sorted(want),
         "resource URIs %s do not match the expected four %s"
         % (", ".join(str(g) for g in got), ", ".join(want)))

    unnamed = [r.get("uri") for r in resources
               if not isinstance(r.get("name"), str) or r["name"].strip() == ""]
    need(not unnamed, "resources with an empty name: %s" % ", ".join(str(u) for u in unnamed))
    sys.exit(0)


# ---- 5. resources/read ----------------------------------------------------

if mode == "resource_read":
    uri = extra[0]
    res = doc.get("result")
    need(isinstance(res, dict), "resources/read %s returned no result: %s" % (uri, preview(doc)))
    contents = res.get("contents")
    need(isinstance(contents, list) and contents,
         "result.contents is missing or empty for %s" % uri)
    first = contents[0]
    need(isinstance(first, dict), "contents[0] is not an object for %s" % uri)
    need(first.get("uri") == uri,
         "contents[0].uri is %r, expected %r" % (first.get("uri"), uri))

    text = first.get("text")
    need(isinstance(text, str) and text.strip() != "",
         "contents[0].text is missing or empty for %s" % uri)
    need("\x1b" not in text, "contents[0].text for %s contains an ANSI escape (\\x1b)" % uri)
    need("\t" not in text, "contents[0].text for %s contains a tab character" % uri)
    sys.exit(0)


# ---- 6/7. JSON-RPC errors -------------------------------------------------

if mode == "error":
    need(isinstance(doc, dict), "reply is not a JSON object")
    need("result" not in doc,
         "expected a JSON-RPC error but the reply carried a result: %s" % preview(doc))
    err = doc.get("error")
    need(isinstance(err, dict), "no error object in reply: %s" % preview(doc))
    need("code" in err, "error object has no code: %s" % preview(err))
    if extra:
        need(str(err["code"]) == extra[0],
             "error code %s, expected %s" % (err["code"], extra[0]))
    sys.exit(0)

if mode == "error_naming":
    need(isinstance(doc, dict), "reply is not a JSON object")
    need("result" not in doc,
         "expected a JSON-RPC error but the reply carried a result: %s" % preview(doc))
    err = doc.get("error")
    need(isinstance(err, dict), "no error object in reply: %s" % preview(doc))
    need("code" in err, "error object has no code: %s" % preview(err))
    token = extra[0]
    message = str(err.get("message", ""))
    need(re.search(r"\b%s\b" % re.escape(token), message, re.IGNORECASE),
         "error message does not name the missing parameter %r: %r" % (token, message))
    sys.exit(0)


# ---- 8. tools/call --------------------------------------------------------

if mode == "tool_content":
    tool = extra[0]
    text = tool_text(doc)
    text_as_json(text, "tools/call %s content[0].text" % tool)
    sys.exit(0)


# ---- 10. ping -------------------------------------------------------------

if mode == "ping":
    need(isinstance(doc, dict), "reply is not a JSON object")
    need("result" in doc, "ping returned no result: %s" % preview(doc))
    need(isinstance(doc["result"], dict), "ping result is not an object: %s" % preview(doc["result"]))
    sys.exit(0)


# ---- 11. batch ------------------------------------------------------------

if mode == "batch":
    need(isinstance(doc, list),
         "batch reply is not a JSON array: %s" % preview(doc))
    need(len(doc) == 2,
         "expected exactly 2 responses (the notification is omitted), got %d: %s"
         % (len(doc), preview(doc)))
    ids = sorted(str(d.get("id")) for d in doc if isinstance(d, dict))
    need(ids == sorted(extra), "batch response ids %s, expected %s" % (ids, sorted(extra)))
    bad = [d for d in doc if not isinstance(d, dict) or d.get("jsonrpc") != "2.0"]
    need(not bad, "batch responses are not JSON-RPC 2.0: %s" % preview(doc))
    sys.exit(0)


# ---- 12. malformed JSON ---------------------------------------------------

if mode == "parse_error":
    need(isinstance(doc, dict), "reply is not a JSON object")
    err = doc.get("error")
    need(isinstance(err, dict), "no error object for a malformed body: %s" % preview(doc))
    need(err.get("code") == -32700,
         "expected -32700 Parse error, got %s: %s" % (err.get("code"), preview(err)))
    sys.exit(0)


# ---- 13. modern era (protocol revision 2026-07-28) ------------------------

def result_object(doc, what):
    """The result object of a reply, or die explaining why there is none."""
    need(isinstance(doc, dict), "reply is not a JSON object")
    err = doc.get("error")
    if isinstance(err, dict):
        die("%s: expected a result but got error %s: %s"
            % (what, err.get("code"), err.get("message")))
    res = doc.get("result")
    need(isinstance(res, dict), "%s: reply carries no result object" % what)
    return res


def server_info(res, what):
    """result._meta['io.modelcontextprotocol/serverInfo'] or die."""
    meta = res.get("_meta")
    need(isinstance(meta, dict), "%s: result._meta is missing or not an object: %s"
         % (what, preview(res.get("_meta"))))
    info = meta.get("io.modelcontextprotocol/serverInfo")
    need(isinstance(info, dict), "%s: result._meta has no serverInfo: %s"
         % (what, preview(meta)))
    need(info.get("name") == "devinmonitor",
         "%s: serverInfo.name is %r, expected 'devinmonitor'" % (what, info.get("name")))
    return info


def caching_hints(res, what, want_scope):
    """ttlMs/cacheScope required on cacheable modern results."""
    ttl = res.get("ttlMs")
    need(isinstance(ttl, int) and not isinstance(ttl, bool) and ttl >= 0,
         "%s: result.ttlMs is %s, want an integer >= 0" % (what, preview(ttl)))
    need(res.get("cacheScope") == want_scope,
         "%s: result.cacheScope is %r, want %r" % (what, res.get("cacheScope"), want_scope))


if mode == "modern_discover":
    res = result_object(doc, "server/discover")
    need(res.get("resultType") == "complete",
         "server/discover: resultType is %r, want 'complete'" % res.get("resultType"))
    versions = res.get("supportedVersions")
    need(isinstance(versions, list) and versions,
         "server/discover: supportedVersions is missing or empty: %s" % preview(versions))
    need(all(isinstance(v, str) for v in versions),
         "server/discover: supportedVersions is not a list of strings: %s" % preview(versions))
    need(MODERN_VERSION in versions,
         "server/discover: supportedVersions %s does not include %s" % (versions, MODERN_VERSION))
    caps = res.get("capabilities")
    need(isinstance(caps, dict), "server/discover: capabilities is missing or not an object")
    for want in ("tools", "resources"):
        need(want in caps, "server/discover: capabilities has no %r" % want)
    server_info(res, "server/discover")
    caching_hints(res, "server/discover", "public")
    instr = res.get("instructions")
    need(isinstance(instr, str) and instr.strip() != "",
         "server/discover: instructions is missing or empty")
    sys.exit(0)


if mode == "modern_result":
    # extra[0] is the expected cacheScope; the caller checked the method's own
    # fields separately (tools_list, resources_list, resource_read).
    want_scope = extra[0]
    res = result_object(doc, "modern result")
    need(res.get("resultType") == "complete",
         "modern result: resultType is %r, want 'complete'" % res.get("resultType"))
    server_info(res, "modern result")
    caching_hints(res, "modern result", want_scope)
    sys.exit(0)


if mode == "modern_result_uncached":
    # tools/call results embed live usage data: resultType yes, cache hints no.
    res = result_object(doc, "modern tools/call")
    need(res.get("resultType") == "complete",
         "modern tools/call: resultType is %r, want 'complete'" % res.get("resultType"))
    server_info(res, "modern tools/call")
    for key in ("ttlMs", "cacheScope"):
        need(key not in res, "modern tools/call: tool results must not carry %r" % key)
    sys.exit(0)


if mode == "legacy_shape":
    # A reply served under the negotiated legacy revision must keep the exact
    # shape legacy clients saw before the dual-era upgrade.
    res = result_object(doc, "legacy result")
    for key in ("resultType", "ttlMs", "cacheScope", "_meta"):
        need(key not in res, "legacy result carries modern field %r: %s" % (key, preview(res)))
    sys.exit(0)


if mode == "unsupported_version":
    need(isinstance(doc, dict), "reply is not a JSON object")
    need("result" not in doc,
         "expected UnsupportedProtocolVersionError but got a result: %s" % preview(doc))
    err = doc.get("error")
    need(isinstance(err, dict), "no error object in reply: %s" % preview(doc))
    need(err.get("code") == -32022,
         "error code is %s, want -32022 (UnsupportedProtocolVersion)" % err.get("code"))
    data = err.get("data")
    need(isinstance(data, dict), "error.data is missing or not an object: %s" % preview(err))
    supported = data.get("supported")
    need(isinstance(supported, list) and supported,
         "error.data.supported is missing or empty: %s" % preview(supported))
    need(MODERN_VERSION in supported,
         "error.data.supported %s does not include %s" % (supported, MODERN_VERSION))
    if extra:
        need(data.get("requested") == extra[0],
             "error.data.requested is %r, expected %r" % (data.get("requested"), extra[0]))
    sys.exit(0)


if mode == "method_not_found":
    need(isinstance(doc, dict), "reply is not a JSON object")
    need("result" not in doc,
         "expected -32601 but the reply carried a result: %s" % preview(doc))
    err = doc.get("error")
    need(isinstance(err, dict), "no error object in reply: %s" % preview(doc))
    need(err.get("code") == -32601,
         "error code is %s, want -32601 (Method not found)" % err.get("code"))
    if extra:
        message = str(err.get("message", ""))
        need(re.search(r"\b%s\b" % re.escape(extra[0]), message, re.IGNORECASE),
             "error message does not name the removed method %r: %r" % (extra[0], message))
    sys.exit(0)


# ---- 14. CLI <-> MCP parity for get_blocks --------------------------------

if mode == "blocks_keys":
    cli_path = extra[0]
    try:
        cli = json.load(open(cli_path))
    except Exception as exc:
        die("blocks --json is not valid JSON: %s" % exc)
    if not isinstance(cli, list):
        die("blocks --json did not return a JSON array")
    if not cli:
        sys.stdout.write("blocks --json produced an empty array on this machine\n")
        sys.exit(3)  # requested SKIP
    cli_first = cli[0]
    need(isinstance(cli_first, dict), "the first block from blocks --json is not an object")

    data = text_as_json(tool_text(doc), "tools/call get_blocks content[0].text")

    blocks = None
    if isinstance(data, list):
        blocks = data
    elif isinstance(data, dict):
        lists = [v for v in data.values() if isinstance(v, list)]
        if len(lists) == 1:
            blocks = lists[0]
    need(blocks is not None,
         "could not locate a block array in get_blocks content: %s" % preview(data))
    need(len(blocks) > 0, "tools/call get_blocks returned an empty array")
    mcp_first = blocks[0]
    need(isinstance(mcp_first, dict), "the first block from get_blocks is not an object")

    cli_keys = sorted(cli_first.keys())
    mcp_keys = sorted(mcp_first.keys())
    if cli_keys != mcp_keys:
        only_cli = [k for k in cli_keys if k not in mcp_keys]
        only_mcp = [k for k in mcp_keys if k not in cli_keys]
        die("per-block key set drifted — only in `blocks --json`: [%s]; only in get_blocks: [%s]\n"
            "        (CLI keys: %s)\n        (MCP keys: %s)"
            % (", ".join(only_cli), ", ".join(only_mcp),
               ", ".join(cli_keys), ", ".join(mcp_keys)))
    sys.exit(0)


die("unknown check mode: %s" % mode)
PYEOF

CHECK_PY="$TMP/check.py"

# ---------------------------------------------------------------------------
# Transport helpers
# ---------------------------------------------------------------------------

REQ=""
REPLY_OUT=""
REPLY_ERR=""
REPLY_LINES=0

# send REQUEST — MCP stdio framing: a Content-Length header followed by exactly
# N bytes of JSON. No trailing newline inside the body (it is not counted in N).
send() {
  local req="$1"
  printf 'Content-Length: %d\r\n\r\n%s' "${#req}" "$req"
}

# capture — runs the server with stdin from $TMP/in.txt and records its stdout,
# stderr and output line count. Deliberately reads a file rather than a pipe: as
# a pipeline stage it runs in a subshell, and these assignments (plus the
# empty-vs-nonempty silence checks) would be silently lost.
capture() {
  run_server < "$TMP/in.txt" > "$TMP/out.txt" 2> "$TMP/err.txt"
  REPLY_OUT="$(cat "$TMP/out.txt")"
  REPLY_ERR="$(cat "$TMP/err.txt")"
  if [[ -z "$REPLY_OUT" ]]; then
    REPLY_LINES=0
  else
    REPLY_LINES="$(printf '%s\n' "$REPLY_OUT" | wc -l | tr -d '[:space:]')"
  fi
}

# invoke REQUEST — one framed request, capturing the reply.
invoke() {
  REQ="$1"
  send "$REQ" > "$TMP/in.txt"
  capture
}

# invoke_ndjson REQUEST — bare newline-delimited JSON, no framing header.
invoke_ndjson() {
  REQ="$1"
  printf '%s\n' "$REQ" > "$TMP/in.txt"
  capture
}

# check NAME MODE [ARGS...] — asserts the current reply with python3.
check() {
  local name="$1"; shift
  local reason rc
  reason="$(printf '%s' "$REPLY_OUT" | python3 "$CHECK_PY" "$@" 2>&1)"
  rc=$?
  if [[ $rc -eq 0 ]]; then
    pass "$name"
  elif [[ $rc -eq 3 ]]; then
    skip "$name — $reason"
  else
    fail "$name"
    if [[ -n "$reason" ]]; then
      printf '        reason:  %s\n' "$reason"
    fi
  fi
}

# check_nth NAME N MODE [ARGS...] — like check, but asserts the Nth reply line
# of a multi-message stream (used by the both-eras-on-one-stream case).
check_nth() {
  local name="$1" n="$2"; shift 2
  local saved="$REPLY_OUT"
  REPLY_OUT="$(printf '%s\n' "$saved" | sed -n "${n}p")"
  check "$name" "$@"
  REPLY_OUT="$saved"
}

# ---------------------------------------------------------------------------
# Cases
# ---------------------------------------------------------------------------

case_initialize() {
  invoke '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"mcp-conformance","version":"1.0"}}}'
  check "initialize: protocolVersion, capabilities{tools,resources}, serverInfo.name, instructions" initialize
}

# The critical regression guard: a single spec-framed request must yield exactly
# one output line and no spurious -32700 Parse error.
case_framing() {
  invoke '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
  if [[ "$REPLY_LINES" != "1" ]]; then
    fail "framing: one Content-Length request produces exactly one output line (got $REPLY_LINES)"
  elif printf '%s' "$REPLY_OUT" | grep -q -- '-32700'; then
    fail "framing: framed request produces no -32700 Parse error"
  else
    pass "framing: one Content-Length request produces exactly one output line, no -32700"
  fi
  check "framing: the single line is a well-formed non-error JSON-RPC response" framing_ok
}

case_tools_list() {
  invoke '{"jsonrpc":"2.0","id":3,"method":"tools/list"}'
  check "tools/list: all eight tools present, every inputSchema.type == object" tools_list
}

case_resources_list() {
  invoke '{"jsonrpc":"2.0","id":4,"method":"resources/list"}'
  check "resources/list: exactly the four URIs, each with a non-empty name" resources_list
}

case_resources_read() {
  local uri id=5
  for uri in 'devinmonitor://summary' 'devinmonitor://models' 'devinmonitor://blocks' 'devinmonitor://alerts'; do
    invoke "{\"jsonrpc\":\"2.0\",\"id\":$id,\"method\":\"resources/read\",\"params\":{\"uri\":\"$uri\"}}"
    check "resources/read $uri: uri echoed, non-empty text, no ANSI escape, no tab" resource_read "$uri"
    id=$((id + 1))
  done
}

case_resources_read_unknown() {
  invoke '{"jsonrpc":"2.0","id":9,"method":"resources/read","params":{"uri":"devinmonitor://does-not-exist"}}'
  check "resources/read unknown URI: JSON-RPC error, not a success result" error
}

case_resources_read_no_uri() {
  invoke '{"jsonrpc":"2.0","id":10,"method":"resources/read","params":{}}'
  check "resources/read without uri: JSON-RPC error" error
}

case_tool_calls() {
  local tool id=20
  for tool in get_sessions get_session get_cost_summary get_alerts get_blocks get_limits compare_periods list_reports; do
    invoke "{\"jsonrpc\":\"2.0\",\"id\":$id,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":{}}}"
    if [[ "$tool" == "get_session" ]]; then
      # get_session requires an id; a JSON-RPC error naming it is correct here.
      check "tools/call get_session {}: JSON-RPC error naming the missing parameter" error_naming id
    else
      check "tools/call $tool {}: result.content[0].text parses as JSON" tool_content "$tool"
    fi
    id=$((id + 1))
  done
}

case_notifications() {
  invoke '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  if [[ -z "$(printf '%s' "$REPLY_OUT" | tr -d '[:space:]')" ]]; then
    pass "notification notifications/initialized: no reply emitted"
  else
    fail "notification notifications/initialized: expected zero output"
  fi

  invoke '{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"test"}}'
  if [[ -z "$(printf '%s' "$REPLY_OUT" | tr -d '[:space:]')" ]]; then
    pass "notification notifications/cancelled (unknown method): still silent"
  else
    fail "notification notifications/cancelled (unknown method): expected zero output"
  fi

  # Same unknown method, but carrying an id: now it is a request, so -32601.
  invoke '{"jsonrpc":"2.0","id":"unknown-req","method":"notifications/cancelled","params":{}}'
  check "unknown request (with id): returns -32601" error -32601
}

case_ping() {
  invoke '{"jsonrpc":"2.0","id":30,"method":"ping"}'
  check "ping: result object" ping
}

case_batch() {
  invoke '[{"jsonrpc":"2.0","id":10,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":11,"method":"ping"}]'
  check "batch: 2 requests + 1 notification yields exactly 2 responses" batch 10 11
}

case_malformed_json() {
  invoke '{"jsonrpc":"2.0","id":12,'
  check "malformed JSON body: -32700 Parse error" parse_error
}

case_ndjson_fallback() {
  invoke_ndjson '{"jsonrpc":"2.0","id":13,"method":"ping"}'
  check "NDJSON fallback: bare JSON line without Content-Length still works" ping
}

# Guards the MCP view and the CLI view against drifting apart.
case_get_blocks_parity() {
  local name="get_blocks cross-check: first-block key set matches 'blocks --json'"
  local cli_rc=0
  run_bin blocks --json > "$TMP/cli-blocks.json" 2> "$TMP/cli-blocks.err" || cli_rc=$?

  if [[ $cli_rc -ne 0 ]]; then
    skip "$name — 'blocks --json' exited $cli_rc: $(head -c 200 "$TMP/cli-blocks.err" | tr '\n' ' ')"
    return
  fi

  invoke '{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"get_blocks","arguments":{}}}'
  check "$name" blocks_keys "$TMP/cli-blocks.json"
}

# ---------------------------------------------------------------------------
# Modern-era cases (protocol revision 2026-07-28)
# ---------------------------------------------------------------------------
#
# Every request below carries its protocol version in `_meta`; none of them
# performs the removed `initialize` handshake. `server/discover` must answer
# even as the first message on the stream, because it is the probe a dual-era
# client uses to detect that this server is modern rather than legacy.

case_modern_discover() {
  invoke "{\"jsonrpc\":\"2.0\",\"id\":50,\"method\":\"server/discover\",\"params\":{$MODERN_META}}"
  check "modern server/discover (first message, no handshake): supportedVersions, capabilities, serverInfo, ttlMs/cacheScope" modern_discover
}

case_modern_no_handshake() {
  invoke "{\"jsonrpc\":\"2.0\",\"id\":51,\"method\":\"tools/list\",\"params\":{$MODERN_META}}"
  check "modern tools/list as the first message: resultType + serverInfo + public cache hints" modern_result public
  check "modern tools/list: all eight tools present, every inputSchema.type == object" tools_list
}

case_modern_tool_call() {
  invoke "{\"jsonrpc\":\"2.0\",\"id\":52,\"method\":\"tools/call\",\"params\":{\"name\":\"get_cost_summary\",\"arguments\":{},$MODERN_META}}"
  check "modern tools/call without handshake: resultType + serverInfo, no cache hints" modern_result_uncached
  check "modern tools/call get_cost_summary: content[0].text parses as JSON" tool_content get_cost_summary
}

case_modern_resources() {
  invoke "{\"jsonrpc\":\"2.0\",\"id\":53,\"method\":\"resources/list\",\"params\":{$MODERN_META}}"
  check "modern resources/list: resultType + serverInfo + public cache hints" modern_result public
  check "modern resources/list: exactly the four URIs, each with a non-empty name" resources_list

  invoke "{\"jsonrpc\":\"2.0\",\"id\":54,\"method\":\"resources/read\",\"params\":{\"uri\":\"devinmonitor://summary\",$MODERN_META}}"
  check "modern resources/read: resultType + serverInfo + private cache hints" modern_result private
  check "modern resources/read devinmonitor://summary: uri echoed, non-empty text, no ANSI escape, no tab" resource_read devinmonitor://summary
}

case_modern_unsupported_version() {
  invoke "{\"jsonrpc\":\"2.0\",\"id\":55,\"method\":\"tools/list\",\"params\":{\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\"1900-01-01\",\"io.modelcontextprotocol/clientCapabilities\":{}}}}"
  check "modern unknown protocol version: -32022 UnsupportedProtocolVersion with data.supported + data.requested" unsupported_version 1900-01-01

  # A recognised modern error is exactly what tells a dual-era client NOT to
  # fall back to the legacy handshake.
  invoke "{\"jsonrpc\":\"2.0\",\"id\":56,\"method\":\"server/discover\",\"params\":{\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\"2025-11-25\",\"io.modelcontextprotocol/clientCapabilities\":{}}}}"
  check "modern server/discover probe with an unsupported version: -32022 (recognised modern error, no legacy fallback)" unsupported_version 2025-11-25
}

case_modern_malformed_meta() {
  invoke '{"jsonrpc":"2.0","id":57,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":20260728}}}'
  check "modern _meta.protocolVersion that is not a string: -32602 Invalid params" error -32602

  # The legacy revision also has a `_meta` (progressToken), so only the modern
  # protocol-version key may switch a request to modern semantics.
  invoke '{"jsonrpc":"2.0","id":58,"method":"tools/list","params":{"_meta":{"progressToken":"legacy-token"}}}'
  check "legacy _meta (progressToken only) is not modern: legacy-shaped tools/list" legacy_shape
}

case_modern_removed_methods() {
  invoke "{\"jsonrpc\":\"2.0\",\"id\":59,\"method\":\"ping\",\"params\":{$MODERN_META}}"
  check "modern ping: removed in 2026-07-28, so -32601 (not served as era-ambiguous legacy)" method_not_found ping

  # `initialize` selects legacy semantics whatever else the message carries, so
  # a legacy-style client still gets the handshake it can use.
  invoke '{"jsonrpc":"2.0","id":60,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"mcp-conformance","version":"1.0"}}}'
  check "initialize still selects legacy semantics (dual-era rule): handshake result" initialize
  check "initialize still selects legacy semantics: no modern resultType on the handshake result" legacy_shape
}

# The two generations must be independent on one connection: answering the
# handshake must not lock the process into legacy semantics, and a modern
# request must not disturb a later legacy one.
case_both_eras_one_stream() {
  local init='{"jsonrpc":"2.0","id":61,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"mcp-conformance","version":"1.0"}}}'
  local modern="{\"jsonrpc\":\"2.0\",\"id\":62,\"method\":\"tools/list\",\"params\":{$MODERN_META}}"
  REQ="$init || $modern"
  { send "$init"; send "$modern"; } > "$TMP/in.txt"
  capture

  if [[ "$REPLY_LINES" != "2" ]]; then
    fail "both eras on one stream: initialize + modern tools/list yield exactly 2 replies (got $REPLY_LINES)"
    return
  fi

  set_era legacy
  check_nth "both eras on one stream: reply 1 (initialize) keeps the legacy shape" 1 legacy_shape
  set_era modern
  check_nth "both eras on one stream: reply 2 (modern _meta) carries resultType + public cache hints" 2 modern_result public
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------
#
# Era is assigned here, per case, so the summary can prove both generations
# were exercised. The legacy block is the original suite, unchanged.

set_era legacy
case_initialize
case_framing
case_tools_list
case_resources_list
case_resources_read
case_resources_read_unknown
case_resources_read_no_uri
case_tool_calls
case_notifications
case_ping
case_batch
case_malformed_json
case_ndjson_fallback
case_get_blocks_parity

set_era modern
case_modern_discover
case_modern_no_handshake
case_modern_tool_call
case_modern_resources
case_modern_unsupported_version
case_modern_malformed_meta
case_modern_removed_methods
case_both_eras_one_stream

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

printf '\n%d passed / %d failed\n' "$PASSED" "$FAILED"
if [[ $SKIPPED -gt 0 ]]; then
  printf '%d skipped\n' "$SKIPPED"
fi

printf 'era coverage — legacy (handshake, %s): %d passed / %d failed / %d skipped\n' \
  "2024-11-05" "$LEGACY_PASSED" "$LEGACY_FAILED" "$LEGACY_SKIPPED"
printf 'era coverage — modern (per-request _meta, %s): %d passed / %d failed / %d skipped\n' \
  "2026-07-28" "$MODERN_PASSED" "$MODERN_FAILED" "$MODERN_SKIPPED"

# A suite that only exercised one generation cannot demonstrate dual-era
# support, so zero passing cases in either era is a failure of the suite itself.
if [[ $LEGACY_PASSED -eq 0 || $MODERN_PASSED -eq 0 ]]; then
  printf '%sFAIL%s  era coverage: both generations must have at least one passing case\n' "$C_RED" "$C_RESET"
  FAILED=$((FAILED + 1))
fi

if [[ $FAILED -gt 0 ]]; then
  exit 1
fi
exit 0