// Package state owns the two external-integration surfaces of devinmonitor:
//
//  1. An atomically written state file that status bars and dashboards poll.
//  2. Capture and read of the "official" upstream statusline payload, with a
//     staleness TTL so readers can fall back to local estimates in time.
//
// Everything is stdlib only (plus in-module packages) and safe under
// CGO_ENABLED=0.
package state

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/status"
)

// OfficialTTLSeconds is how long an upstream capture stays trustworthy.
// After this, readers must treat it as stale and fall back to local estimates.
const OfficialTTLSeconds int64 = 600

// DefaultStatePath returns the default state file path:
//
//	<config dir>/state/latest.json
//
// where <config dir> is filepath.Dir(config.Path()) — so it honours
// DEVINMONITOR_CONFIG_DIR automatically.
func DefaultStatePath() string {
	return filepath.Join(filepath.Dir(config.Path()), "state", "latest.json")
}

// DefaultStatuslinePath returns the default upstream-capture path:
//
//	<config dir>/statusline/latest.json
func DefaultStatuslinePath() string {
	return filepath.Join(filepath.Dir(config.Path()), "statusline", "latest.json")
}

// UpstreamWindow is one rate-limit window from an upstream payload.
type UpstreamWindow struct {
	UsedPercentage *float64 `json:"usedPercentage"`
	ResetsAtEpoch  *int64   `json:"resetsAtEpoch"`
}

// Captured is a persisted upstream snapshot plus its capture time.
type Captured struct {
	CapturedAtEpoch int64           `json:"capturedAtEpoch"`
	FiveHour        *UpstreamWindow `json:"fiveHour,omitempty"`
	SevenDay        *UpstreamWindow `json:"sevenDay,omitempty"`
}

// WriteAtomic writes data to path atomically: create parent dirs, write to a
// pid-unique temp file in the same directory, then rename it over path.
// A concurrent reader must never observe a partially written file.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("state: create %s: %w", dir, err)
	}

	// The pid keeps concurrent *processes* apart; CreateTemp's random suffix
	// keeps concurrent goroutines within one process apart.
	pattern := fmt.Sprintf(".%s.%d.*.tmp", filepath.Base(path), os.Getpid())
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("state: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Best-effort cleanup for every early return; cleared once the rename wins.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: write temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("state: rename temp file over %s: %w", path, err)
	}
	committed = true
	return nil
}

// CaptureStatusline parses an upstream statusline payload, sanitizes every
// percentage, and persists the result to path atomically.
// It returns the capture that was written.
// A payload with no usable rate limits returns a Captured containing only
// CapturedAtEpoch (no error) — the caller decides what to do with that.
func CaptureStatusline(payload []byte, path string, now time.Time) (*Captured, error) {
	// Decoding into raw messages validates the whole document's JSON syntax
	// first, so malformed input is rejected before anything touches the disk.
	var top rawObject
	if err := json.Unmarshal(payload, &top); err != nil {
		return nil, fmt.Errorf("state: invalid statusline payload: %w", err)
	}

	// Prefer the documented "rate_limits" envelope, but stay liberal and also
	// accept the windows sitting at the top level.
	windows := top
	if limits, ok := lookup(top, "ratelimits"); ok {
		var obj rawObject
		if err := json.Unmarshal(limits, &obj); err == nil && obj != nil {
			windows = obj
		}
	}

	captured := &Captured{CapturedAtEpoch: now.Unix()}
	if raw, ok := lookup(windows, "fivehour"); ok {
		captured.FiveHour = decodeWindow(raw)
	}
	if raw, ok := lookup(windows, "sevenday"); ok {
		captured.SevenDay = decodeWindow(raw)
	}

	data, err := json.Marshal(captured)
	if err != nil {
		return nil, fmt.Errorf("state: encode capture: %w", err)
	}
	if err := WriteAtomic(path, data); err != nil {
		return nil, err
	}
	return captured, nil
}

// ReadStatusline loads a capture from path. The second return value reports
// whether it is stale: stale is true when now-CapturedAtEpoch > OfficialTTLSeconds.
// A missing file returns (nil, false, err) with a non-nil error.
func ReadStatusline(path string, now time.Time) (*Captured, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("state: read statusline capture: %w", err)
	}
	var captured Captured
	if err := json.Unmarshal(data, &captured); err != nil {
		return nil, false, fmt.Errorf("state: decode statusline capture: %w", err)
	}
	stale := now.Unix()-captured.CapturedAtEpoch > OfficialTTLSeconds
	return &captured, stale, nil
}

// rawObject is a JSON object whose values stay undecoded, which is what lets
// this package accept every documented key spelling without a struct per shape.
type rawObject map[string]json.RawMessage

// normalizeKey lowercases a JSON key and strips separators so snake_case and
// camelCase spellings of the same field compare equal (used_percentage,
// usedPercentage and usedPercent all normalize the same way).
func normalizeKey(key string) string {
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, "_", "")
	key = strings.ReplaceAll(key, "-", "")
	return key
}

// lookup finds the value stored under any spelling of the normalized key.
func lookup(obj rawObject, normalizedKey string) (json.RawMessage, bool) {
	for key, value := range obj {
		if normalizeKey(key) == normalizedKey {
			return value, true
		}
	}
	return nil, false
}

// decodeWindow converts one rate-limit window. Anything unusable is reported as
// nil (missing window) rather than an error — the schema is upstream's.
func decodeWindow(raw json.RawMessage) *UpstreamWindow {
	var obj rawObject
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil
	}
	window := &UpstreamWindow{}
	for key, value := range obj {
		switch normalizeKey(key) {
		case "usedpercentage", "usedpercent":
			if f, ok := decodeFloat(value); ok {
				// Shared rule: NaN/Inf, negatives and epoch-sized leaks are
				// dropped, a rounding artifact just over 100 is clamped.
				window.UsedPercentage = status.SanitizePercent(f)
			}
		case "resetsat", "resetsatepoch":
			if n, ok := decodeInt(value); ok {
				window.ResetsAtEpoch = &n
			}
		}
	}
	if window.UsedPercentage == nil && window.ResetsAtEpoch == nil {
		return nil
	}
	return window
}

// decodeFloat accepts a JSON number or a numeric JSON string. "NaN" and "Inf"
// strings parse successfully and are then dropped by status.SanitizePercent.
func decodeFloat(raw json.RawMessage) (float64, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return 0, false
		}
		s = strings.TrimSpace(str)
		if s == "" {
			return 0, false
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// decodeInt accepts a JSON number (integer or float) or a numeric JSON string.
func decodeInt(raw json.RawMessage) (int64, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return 0, false
		}
		s = strings.TrimSpace(str)
		if s == "" {
			return 0, false
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return int64(f), true
}
