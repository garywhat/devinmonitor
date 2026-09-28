#!/usr/bin/env bash
# Cross-check our numbers against Devin's own accounting.
#
# Why this exists: for a long time nothing in this repository compared a figure
# we computed with the figure Devin itself reports. That is how our token totals
# stayed 2.0-3.4x too high -- every request is written to 2-3 message_nodes, each
# carrying an identical copy of its metrics, and we summed them all -- without
# anyone noticing. Devin stores its own per-session accounting in the
# sessions.metadata.response_dimensions column, which we already read, so the
# check is nearly free.
#
# Usage:  scripts/verify-against-vendor.sh [path-to-binary] [path-to-sessions.db]
#
# Exit codes: 0 = every comparable session agrees; 1 = a mismatch; 2 = cannot run
# (no database, no sqlite3, or the schema predates response_dimensions).

set -u

BIN="${1:-}"
DB_PATH="${2:-${DEVINMONITOR_DB:-$HOME/.local/share/devin/cli/sessions.db}}"

if [ -z "$BIN" ]; then
  for candidate in ./devinmonitor /tmp/dm-verify; do
    if [ -x "$candidate" ]; then BIN="$candidate"; break; fi
  done
fi
if [ -z "$BIN" ] || [ ! -x "$BIN" ]; then
  echo "verify-against-vendor: no binary; pass one as \$1 or build one first" >&2
  exit 2
fi
if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "verify-against-vendor: sqlite3 is required" >&2
  exit 2
fi
if [ ! -f "$DB_PATH" ]; then
  echo "verify-against-vendor: skipping, no database at $DB_PATH"
  exit 2
fi

DB="file:${DB_PATH}?mode=ro"

# Devin writes response_dimensions asynchronously, so a session that is still
# running may not have them yet. Those are skipped, not failed.
if [ "$(sqlite3 "$DB" "select count(*) from sessions where metadata like '%response_dimensions%';" 2>/dev/null || echo 0)" = "0" ]; then
  echo "verify-against-vendor: skipping, no session carries response_dimensions"
  exit 2
fi

TOLERANCE="${TOLERANCE:-0.05}"   # 5%: Devin's figure is written asynchronously
mkdir -p /tmp/vendor-check
OUT=/tmp/vendor-check/ours.json

if ! "$BIN" export sessions --format json > "$OUT" 2>/tmp/vendor-check/err.txt; then
  echo "verify-against-vendor: our own export failed:" >&2
  sed 's/^/  /' /tmp/vendor-check/err.txt >&2
  exit 2
fi

python3 - "$DB" "$OUT" "$TOLERANCE" <<'PY'
import json, sqlite3, sys

db_url, ours_path, tol = sys.argv[1], sys.argv[2], float(sys.argv[3])

raw = json.load(open(ours_path, encoding="utf-8"))
rows = raw if isinstance(raw, list) else raw.get("sessions", [])
ours = {r.get("id"): r for r in rows if isinstance(r, dict)}

con = sqlite3.connect(db_url, uri=True)
checked = skipped = 0
bad = []
missing_split = []

for (sid,) in con.execute("select id from sessions order by created_at"):
    md = con.execute("select metadata from sessions where id=?", (sid,)).fetchone()
    vendor = {}
    if md and md[0]:
        try:
            for d in (json.loads(md[0]).get("response_dimensions") or []):
                kind = list(d.get("kind", {}).values())
                if not kind:
                    continue
                vendor[d.get("uid")] = kind[0].get("value")
        except (ValueError, AttributeError):
            pass
    if "input_tokens" not in vendor:
        skipped += 1
        continue

    mine = ours.get(sid)
    if mine is None:
        skipped += 1
        continue

    # Our raw totals include context compaction; Devin's figure excludes it. So
    # the comparable quantity is (total - compaction).
    def num(*keys):
        for k in keys:
            v = mine.get(k)
            if isinstance(v, (int, float)):
                return v
        return 0

    our_in = num("input_tokens", "inputTokens")

    # Our totals include context compaction and Devin's exclude it, so the
    # comparable quantity is (total - compaction). If the export does not carry
    # the compaction split we must NOT silently compare the raw total and then
    # report a failure that is really a missing field -- say so and stop.
    if "compaction" not in mine:
        print(f"  {sid}: export has no 'compaction' field; cannot compare like with like", file=sys.stderr)
        missing_split.append(sid)
        continue
    comp = mine.get("compaction") or {}
    compaction = comp.get("input_tokens", 0) if isinstance(comp, dict) else 0

    want = float(vendor["input_tokens"])
    got = our_in - compaction
    if want <= 0:
        skipped += 1
        continue
    checked += 1
    delta = abs(got - want) / want
    mark = "ok  " if delta <= tol else "FAIL"
    print(f"  {mark} {sid:<22} ours={got:>14,.0f}  devin={want:>14,.0f}  delta={delta*100:5.2f}%")
    if delta > tol:
        bad.append((sid, got, want, delta))

print()
print(f"  checked {checked}, skipped {skipped} (no vendor figure or still running), tolerance {tol*100:.0f}%")
if missing_split:
    print(f"  {len(missing_split)} session(s) could not be compared: the session export")
    print("  does not expose the compaction split, so our cache-inclusive total cannot be")
    print("  lined up against Devin's compaction-excluded figure. This is a GAP, not a pass.")
    sys.exit(2)
if bad:
    print(f"  {len(bad)} session(s) disagree with Devin's own accounting:")
    for sid, got, want, delta in bad:
        print(f"    {sid}: ours {got:,.0f} vs devin {want:,.0f} ({delta*100:.1f}%)")
    sys.exit(1)
if checked == 0:
    print("  nothing comparable was found; treat this as unverified, not as passing")
    sys.exit(2)
print("  all comparable sessions agree with Devin's own accounting")
PY