package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/status"
)

func ptrF(f float64) *float64 { return &f }
func ptrI(i int64) *int64     { return &i }

// tmpFiles lists the atomic-write leftovers (*.tmp) in dir, if any.
func tmpFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	return matches
}

func TestWriteAtomicCreatesParentDirsAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "nested", "latest.json")
	want := []byte("{\"hello\":\"world\"}\n")

	if err := WriteAtomic(path, want); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("parent directory was not created: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content round-trip mismatch: got %q want %q", got, want)
	}
}

func TestWriteAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "state")
	path := filepath.Join(nested, "latest.json")

	for i := 0; i < 5; i++ {
		if err := WriteAtomic(path, []byte(fmt.Sprintf("payload-%d", i))); err != nil {
			t.Fatalf("WriteAtomic #%d: %v", i, err)
		}
	}

	if leftovers := tmpFiles(t, nested); len(leftovers) != 0 {
		t.Fatalf("leftover temp files in %s: %v", nested, leftovers)
	}
	if leftovers := tmpFiles(t, dir); len(leftovers) != 0 {
		t.Fatalf("leftover temp files in %s: %v", dir, leftovers)
	}

	entries, err := os.ReadDir(nested)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "latest.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory should hold only latest.json, got %v", names)
	}
}

func TestWriteAtomicReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.json")

	if err := WriteAtomic(path, []byte("first")); err != nil {
		t.Fatalf("first WriteAtomic: %v", err)
	}
	if err := WriteAtomic(path, []byte("second")); err != nil {
		t.Fatalf("second WriteAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("file was not replaced: got %q want %q", got, "second")
	}
	if leftovers := tmpFiles(t, dir); len(leftovers) != 0 {
		t.Fatalf("leftover temp files: %v", leftovers)
	}
}

func TestCaptureStatuslineSpellings(t *testing.T) {
	now := time.Unix(1790000123, 0)

	tests := []struct {
		name      string
		payload   string
		fiveHour  *UpstreamWindow
		sevenDay  *UpstreamWindow
		wantError bool
	}{
		{
			name:     "snake_case five_hour number resets_at",
			payload:  `{"rate_limits": {"five_hour": {"used_percentage": 42.5, "resets_at": 1790000000}}}`,
			fiveHour: &UpstreamWindow{UsedPercentage: ptrF(42.5), ResetsAtEpoch: ptrI(1790000000)},
		},
		{
			name:     "camelCase fiveHour",
			payload:  `{"rate_limits": {"fiveHour": {"usedPercentage": 42.5, "resetsAtEpoch": 1790000000}}}`,
			fiveHour: &UpstreamWindow{UsedPercentage: ptrF(42.5), ResetsAtEpoch: ptrI(1790000000)},
		},
		{
			name:     "seven_day with resets_at_epoch",
			payload:  `{"rate_limits": {"seven_day": {"used_percentage": 12, "resets_at_epoch": 1790000000}}}`,
			sevenDay: &UpstreamWindow{UsedPercentage: ptrF(12), ResetsAtEpoch: ptrI(1790000000)},
		},
		{
			name:     "camelCase sevenDay",
			payload:  `{"rate_limits": {"sevenDay": {"usedPercentage": 33, "resetsAtEpoch": 1790000001}}}`,
			sevenDay: &UpstreamWindow{UsedPercentage: ptrF(33), ResetsAtEpoch: ptrI(1790000001)},
		},
		{
			name:     "resets_at as a numeric string",
			payload:  `{"rate_limits": {"five_hour": {"used_percentage": 10, "resets_at": "1790000000"}}}`,
			fiveHour: &UpstreamWindow{UsedPercentage: ptrF(10), ResetsAtEpoch: ptrI(1790000000)},
		},
		{
			name:     "epoch-sized percentage leak is dropped",
			payload:  `{"rate_limits": {"five_hour": {"used_percentage": 1.79e9, "resets_at": 1790000000}}}`,
			fiveHour: &UpstreamWindow{UsedPercentage: nil, ResetsAtEpoch: ptrI(1790000000)},
		},
		{
			name:     "negative percentage is dropped",
			payload:  `{"rate_limits": {"five_hour": {"used_percentage": -3.5, "resets_at": 1790000000}}}`,
			fiveHour: &UpstreamWindow{UsedPercentage: nil, ResetsAtEpoch: ptrI(1790000000)},
		},
		{
			name:     "NaN percentage string is dropped",
			payload:  `{"rate_limits": {"five_hour": {"used_percentage": "NaN"}}}`,
			fiveHour: nil,
		},
		{
			name:     "100 percent is kept",
			payload:  `{"rate_limits": {"five_hour": {"used_percentage": 100}}}`,
			fiveHour: &UpstreamWindow{UsedPercentage: ptrF(100)},
		},
		{
			name:     "rounding artifact just above 100 follows the shared sanitizer",
			payload:  `{"rate_limits": {"five_hour": {"used_percentage": 100.5}}}`,
			fiveHour: windowFor(status.SanitizePercent(100.5)),
		},
		{
			name:     "well above 100 follows the shared sanitizer",
			payload:  `{"rate_limits": {"seven_day": {"used_percentage": 101.5}}}`,
			sevenDay: windowFor(status.SanitizePercent(101.5)),
		},
		{
			name:     "window with no usable field is nil",
			payload:  `{"rate_limits": {"five_hour": {"something_else": 7}}}`,
			fiveHour: nil,
		},
		{
			name:     "unknown extra keys are ignored",
			payload:  `{"generated_at": 1, "rate_limits": {"unknown_window": {"used_percentage": 9}, "five_hour": {"used_percentage": 20, "extra": {"deep": [1, 2, 3]}}, "seven_day": {"usedPercentage": 5, "resetsAtEpoch": "1790000002", "note": null}}}`,
			fiveHour: &UpstreamWindow{UsedPercentage: ptrF(20)},
			sevenDay: &UpstreamWindow{UsedPercentage: ptrF(5), ResetsAtEpoch: ptrI(1790000002)},
		},
		{
			name:     "both windows at once",
			payload:  `{"rate_limits": {"five_hour": {"used_percentage": 1, "resets_at": 1790000000}, "seven_day": {"used_percentage": 2, "resets_at": 1790000001}}}`,
			fiveHour: &UpstreamWindow{UsedPercentage: ptrF(1), ResetsAtEpoch: ptrI(1790000000)},
			sevenDay: &UpstreamWindow{UsedPercentage: ptrF(2), ResetsAtEpoch: ptrI(1790000001)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "statusline", "latest.json")

			captured, err := CaptureStatusline([]byte(tc.payload), path, now)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected an error, got capture %+v", captured)
				}
				return
			}
			if err != nil {
				t.Fatalf("CaptureStatusline: %v", err)
			}
			if captured.CapturedAtEpoch != now.Unix() {
				t.Fatalf("CapturedAtEpoch = %d, want %d", captured.CapturedAtEpoch, now.Unix())
			}
			assertWindow(t, "fiveHour", captured.FiveHour, tc.fiveHour)
			assertWindow(t, "sevenDay", captured.SevenDay, tc.sevenDay)

			// The returned capture must be exactly what landed on disk.
			loaded, stale, err := ReadStatusline(path, now)
			if err != nil {
				t.Fatalf("ReadStatusline: %v", err)
			}
			if stale {
				t.Fatalf("freshly captured file reported stale")
			}
			assertWindow(t, "persisted fiveHour", loaded.FiveHour, tc.fiveHour)
			assertWindow(t, "persisted sevenDay", loaded.SevenDay, tc.sevenDay)
			if loaded.CapturedAtEpoch != captured.CapturedAtEpoch {
				t.Fatalf("persisted CapturedAtEpoch = %d, want %d", loaded.CapturedAtEpoch, captured.CapturedAtEpoch)
			}
		})
	}
}

func assertWindow(t *testing.T, label string, got, want *UpstreamWindow) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("%s = %+v, want nil", label, *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("%s = nil, want %+v", label, *want)
	}
	switch {
	case want.UsedPercentage == nil && got.UsedPercentage != nil:
		t.Fatalf("%s usedPercentage = %v, want nil", label, *got.UsedPercentage)
	case want.UsedPercentage != nil && got.UsedPercentage == nil:
		t.Fatalf("%s usedPercentage = nil, want %v", label, *want.UsedPercentage)
	case want.UsedPercentage != nil && *got.UsedPercentage != *want.UsedPercentage:
		t.Fatalf("%s usedPercentage = %v, want %v", label, *got.UsedPercentage, *want.UsedPercentage)
	}
	switch {
	case want.ResetsAtEpoch == nil && got.ResetsAtEpoch != nil:
		t.Fatalf("%s resetsAtEpoch = %v, want nil", label, *got.ResetsAtEpoch)
	case want.ResetsAtEpoch != nil && got.ResetsAtEpoch == nil:
		t.Fatalf("%s resetsAtEpoch = nil, want %v", label, *want.ResetsAtEpoch)
	case want.ResetsAtEpoch != nil && *got.ResetsAtEpoch != *want.ResetsAtEpoch:
		t.Fatalf("%s resetsAtEpoch = %v, want %v", label, *got.ResetsAtEpoch, *want.ResetsAtEpoch)
	}
}

// windowFor builds the window expected for a payload that carries only a used
// percentage: the sanitizer decides whether the field survives at all, so this
// keeps the pipeline test honest without re-pinning the sanitizer's policy.
func windowFor(percent *float64) *UpstreamWindow {
	if percent == nil {
		return nil
	}
	return &UpstreamWindow{UsedPercentage: percent}
}

// TestCaptureStatuslineAppliesSharedSanitizer proves every persisted percentage
// went through status.SanitizePercent rather than being written raw. The oracle
// is the shared sanitizer itself, so a policy change on its side does not make
// this test brittle — only a missing pass-through does.
func TestCaptureStatuslineAppliesSharedSanitizer(t *testing.T) {
	now := time.Unix(1790000123, 0)

	values := []float64{0, 0.5, 42.5, 99.9, 100, 100.5, 101, 101.5, 3600, 1.79e9, -1e-9, -42.5, math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, v := range values {
		payload := fmt.Sprintf(`{"rate_limits": {"five_hour": {"used_percentage": %q}}}`, strconv.FormatFloat(v, 'g', -1, 64))
		want := status.SanitizePercent(v)

		t.Run(strconv.FormatFloat(v, 'g', -1, 64), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "latest.json")
			captured, err := CaptureStatusline([]byte(payload), path, now)
			if err != nil {
				t.Fatalf("CaptureStatusline(%s): %v", payload, err)
			}
			assertWindow(t, "fiveHour", captured.FiveHour, windowFor(want))

			// The value on disk must match the sanitizer's verdict too.
			loaded, _, err := ReadStatusline(path, now)
			if err != nil {
				t.Fatalf("ReadStatusline: %v", err)
			}
			assertWindow(t, "persisted fiveHour", loaded.FiveHour, windowFor(want))
		})
	}
}

func TestCaptureStatuslineEmptyPayload(t *testing.T) {
	now := time.Unix(1790000123, 0)
	path := filepath.Join(t.TempDir(), "latest.json")

	for _, payload := range []string{`{}`, `{"rate_limits": {}}`, `{"rate_limits": null}`, `null`} {
		captured, err := CaptureStatusline([]byte(payload), path, now)
		if err != nil {
			t.Fatalf("CaptureStatusline(%s): %v", payload, err)
		}
		if captured == nil {
			t.Fatalf("CaptureStatusline(%s) returned nil capture", payload)
		}
		if captured.FiveHour != nil || captured.SevenDay != nil {
			t.Fatalf("CaptureStatusline(%s) = %+v, want no windows", payload, *captured)
		}
		if captured.CapturedAtEpoch != now.Unix() {
			t.Fatalf("CaptureStatusline(%s) CapturedAtEpoch = %d, want %d", payload, captured.CapturedAtEpoch, now.Unix())
		}
		// It was still persisted (the caller decides what to do with it).
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("capture for %s was not persisted: %v", payload, err)
		}
		var roundTripped Captured
		if err := json.Unmarshal(data, &roundTripped); err != nil {
			t.Fatalf("persisted capture for %s is not valid JSON: %v", payload, err)
		}
	}
}

func TestCaptureStatuslineInvalidJSON(t *testing.T) {
	now := time.Unix(1790000123, 0)

	for _, payload := range []string{`{`, `{"rate_limits": `, `{"rate_limits": {"five_hour": }}}`, `not json at all`} {
		path := filepath.Join(t.TempDir(), "statusline", "latest.json")

		captured, err := CaptureStatusline([]byte(payload), path, now)
		if err == nil {
			t.Fatalf("CaptureStatusline(%s) = %+v, want error", payload, captured)
		}
		if captured != nil {
			t.Fatalf("CaptureStatusline(%s) returned a capture alongside the error", payload)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("CaptureStatusline(%s) wrote %s despite invalid JSON (stat err: %v)", payload, path, statErr)
		}
	}
}

func TestReadStatuslineStaleness(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "statusline.json")

	captured := &Captured{
		CapturedAtEpoch: 1000,
		FiveHour:        &UpstreamWindow{UsedPercentage: ptrF(7), ResetsAtEpoch: ptrI(2000)},
	}
	data, err := json.Marshal(captured)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := WriteAtomic(path, data); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}

	tests := []struct {
		name      string
		now       time.Time
		wantStale bool
	}{
		{name: "captured now", now: time.Unix(1000, 0), wantStale: false},
		{name: "one second before TTL", now: time.Unix(1000+OfficialTTLSeconds-1, 0), wantStale: false},
		{name: "exactly TTL is not stale", now: time.Unix(1000+OfficialTTLSeconds, 0), wantStale: false},
		{name: "one second past TTL", now: time.Unix(1000+OfficialTTLSeconds+1, 0), wantStale: true},
		{name: "long past TTL", now: time.Unix(1000+OfficialTTLSeconds*10, 0), wantStale: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, stale, err := ReadStatusline(path, tc.now)
			if err != nil {
				t.Fatalf("ReadStatusline: %v", err)
			}
			if stale != tc.wantStale {
				t.Fatalf("stale = %v, want %v", stale, tc.wantStale)
			}
			if got.CapturedAtEpoch != 1000 {
				t.Fatalf("CapturedAtEpoch = %d, want 1000", got.CapturedAtEpoch)
			}
			assertWindow(t, "fiveHour", got.FiveHour, captured.FiveHour)
		})
	}

	t.Run("missing file", func(t *testing.T) {
		got, stale, err := ReadStatusline(filepath.Join(dir, "does-not-exist.json"), time.Unix(1000, 0))
		if err == nil {
			t.Fatalf("ReadStatusline on a missing file returned %+v, want error", got)
		}
		if got != nil {
			t.Fatalf("ReadStatusline on a missing file returned a capture: %+v", got)
		}
		if stale {
			t.Fatalf("ReadStatusline on a missing file reported stale")
		}
	})

	t.Run("corrupt file", func(t *testing.T) {
		corrupt := filepath.Join(dir, "corrupt.json")
		if err := os.WriteFile(corrupt, []byte("{"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if _, _, err := ReadStatusline(corrupt, time.Unix(1000, 0)); err == nil {
			t.Fatalf("ReadStatusline on corrupt JSON returned no error")
		}
	})
}

// TestWriteAtomicConcurrentReadersNeverSeePartialFile hammers one path from
// many goroutines while a reader re-parses the file. The run is bounded by a
// fixed iteration count rather than by wall-clock timing.
func TestWriteAtomicConcurrentReadersNeverSeePartialFile(t *testing.T) {
	const (
		writers         = 20
		writesPerWriter = 40
	)

	dir := t.TempDir()
	nested := filepath.Join(dir, "state")
	path := filepath.Join(nested, "latest.json")

	seed, err := json.Marshal(map[string]int{"writer": -1, "iteration": -1})
	if err != nil {
		t.Fatalf("Marshal seed: %v", err)
	}
	if err := WriteAtomic(path, seed); err != nil {
		t.Fatalf("seed WriteAtomic: %v", err)
	}

	type record struct {
		Writer    int `json:"writer"`
		Iteration int `json:"iteration"`
	}

	var (
		mu      sync.Mutex
		bad     []byte
		badErr  error
		badSeen bool
	)
	recordBad := func(raw []byte, err error) {
		mu.Lock()
		defer mu.Unlock()
		if !badSeen {
			badSeen = true
			bad = append([]byte(nil), raw...)
			badErr = err
		}
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	var reads atomic.Int64

	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					// Only possible before the first rename lands.
					continue
				}
				recordBad(nil, err)
				return
			}
			var rec record
			if err := json.Unmarshal(raw, &rec); err != nil {
				recordBad(raw, err)
				return
			}
			if rec.Writer < -1 || rec.Writer >= writers || rec.Iteration < -1 || rec.Iteration >= writesPerWriter {
				recordBad(raw, fmt.Errorf("read out-of-range record %+v", rec))
				return
			}
			reads.Add(1)
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < writesPerWriter; i++ {
				payload, err := json.Marshal(record{Writer: w, Iteration: i})
				if err != nil {
					recordBad(nil, err)
					return
				}
				if err := WriteAtomic(path, payload); err != nil {
					recordBad(nil, err)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(stop)
	readers.Wait()

	mu.Lock()
	defer mu.Unlock()
	if badSeen {
		t.Fatalf("reader observed a partial/invalid file: %v\ncontent: %q", badErr, bad)
	}
	if reads.Load() == 0 {
		t.Fatalf("reader never completed a read; the test proved nothing")
	}
	if leftovers := tmpFiles(t, nested); len(leftovers) != 0 {
		t.Fatalf("leftover temp files after concurrent writes: %v", leftovers)
	}
	if leftovers := tmpFiles(t, dir); len(leftovers) != 0 {
		t.Fatalf("leftover temp files after concurrent writes: %v", leftovers)
	}
}

func TestDefaultPathsHonourConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVINMONITOR_CONFIG_DIR", dir)

	statePath := DefaultStatePath()
	statuslinePath := DefaultStatuslinePath()

	if !strings.HasSuffix(filepath.ToSlash(statePath), "state/latest.json") {
		t.Fatalf("DefaultStatePath() = %q, want a path ending in state/latest.json", statePath)
	}
	if !strings.HasSuffix(filepath.ToSlash(statuslinePath), "statusline/latest.json") {
		t.Fatalf("DefaultStatuslinePath() = %q, want a path ending in statusline/latest.json", statuslinePath)
	}

	// Both paths must be rooted at the config directory.
	configDir := filepath.Dir(config.Path())
	if statePath != filepath.Join(configDir, "state", "latest.json") {
		t.Fatalf("DefaultStatePath() = %q, want it derived from config dir %q", statePath, configDir)
	}
	if statuslinePath != filepath.Join(configDir, "statusline", "latest.json") {
		t.Fatalf("DefaultStatuslinePath() = %q, want it derived from config dir %q", statuslinePath, configDir)
	}

	if configDir == dir {
		// Cold cache: config.Path() honoured the environment.
		if statePath != filepath.Join(dir, "state", "latest.json") {
			t.Fatalf("DefaultStatePath() = %q, want %q", statePath, filepath.Join(dir, "state", "latest.json"))
		}
		if statuslinePath != filepath.Join(dir, "statusline", "latest.json") {
			t.Fatalf("DefaultStatuslinePath() = %q, want %q", statuslinePath, filepath.Join(dir, "statusline", "latest.json"))
		}
	} else {
		// config.Path() memoizes its result with sync.Once, so a re-run of this
		// test suite in the same process (go test -count=N) keeps the first
		// directory. Env freshness is still asserted, in a fresh process, below.
		t.Logf("config.Path() already memoized as %q; checking env freshness in a child process", configDir)
	}

	assertChildHonoursConfigDir(t, dir)
}

// assertChildHonoursConfigDir re-executes this test binary with the environment
// variable set, which is the only way to observe config.Path()'s env handling
// with a cold sync.Once cache.
func assertChildHonoursConfigDir(t *testing.T, configDir string) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestStatePathChildProcess$", "-test.v=false")
	cmd.Env = childEnv(map[string]string{
		"DEVINMONITOR_STATE_PATH_CHILD": "1",
		"DEVINMONITOR_CONFIG_DIR":       configDir,
	})
	out, err := cmd.CombinedOutput()
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			t.Skipf("cannot re-exec the test binary (%v); env freshness unverified in-process", err)
		}
		t.Fatalf("child process did not honour DEVINMONITOR_CONFIG_DIR: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("child process produced unexpected output: %q", out)
	}
}

// TestStatePathChildProcess is a helper: it only does work when re-executed by
// assertChildHonoursConfigDir with DEVINMONITOR_STATE_PATH_CHILD=1.
func TestStatePathChildProcess(t *testing.T) {
	if os.Getenv("DEVINMONITOR_STATE_PATH_CHILD") != "1" {
		t.Skip("helper process; re-executed by TestDefaultPathsHonourConfigDir")
	}

	dir := os.Getenv("DEVINMONITOR_CONFIG_DIR")
	if got, want := DefaultStatePath(), filepath.Join(dir, "state", "latest.json"); got != want {
		fmt.Fprintf(os.Stderr, "DefaultStatePath() = %q, want %q\n", got, want)
		os.Exit(3)
	}
	if got, want := DefaultStatuslinePath(), filepath.Join(dir, "statusline", "latest.json"); got != want {
		fmt.Fprintf(os.Stderr, "DefaultStatuslinePath() = %q, want %q\n", got, want)
		os.Exit(4)
	}
	fmt.Fprint(os.Stdout, "ok")
	os.Exit(0)
}

// childEnv returns the current environment with the given keys replaced, so a
// re-executed child never sees a duplicate DEVINMONITOR_CONFIG_DIR.
func childEnv(override map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(override))
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			if _, ok := override[kv[:i]]; ok {
				continue
			}
		}
		env = append(env, kv)
	}
	for k, v := range override {
		env = append(env, k+"="+v)
	}
	return env
}
