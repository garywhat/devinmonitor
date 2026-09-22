package pricing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// catalogueJSON is the published shape: USD per SINGLE token, as strings.
const catalogueJSON = `{"data":[
 {"id":"openai/gpt-4o","pricing":{"prompt":"0.0000025","completion":"0.00001","input_cache_read":"0.00000125"}},
 {"id":"anthropic/claude-sonnet-4-5","pricing":{"prompt":"0.000003","completion":"0.000015"}},
 {"id":"free/model","pricing":{"prompt":"0","completion":"0"}},
 {"id":"no/pricing","pricing":{}},
 {"id":"","pricing":{"prompt":"0.001","completion":"0.001"}}
]}`

func TestParseCatalogueScalesToPerMillion(t *testing.T) {
	got, err := parseCatalogue([]byte(catalogueJSON))
	if err != nil {
		t.Fatalf("parseCatalogue: %v", err)
	}
	// 2.5e-6 per token == 2.50 per 1M tokens.
	if o := got["openai/gpt-4o"]; o.InputPerM != 2.5 || o.OutputPerM != 10.0 || o.CacheReadPerM != 1.25 {
		t.Errorf("gpt-4o = %+v, want 2.5 / 10 / 1.25 per 1M", o)
	}
	// The bare model name is indexed too, so a local "gpt-4o" resolves.
	if o, ok := got["gpt-4o"]; !ok || o.InputPerM != 2.5 {
		t.Errorf("bare name gpt-4o = %+v (present=%v), want the same entry", o, ok)
	}
	if o := got["free/model"]; !o.Free {
		t.Errorf("zero-price model = %+v, want Free", o)
	}
	// An entry with no usable price is skipped rather than guessed.
	if _, ok := got["no/pricing"]; ok {
		t.Error("an entry without pricing was included")
	}
	if _, ok := got[""]; ok {
		t.Error("an empty model id was included")
	}
}

func TestNeedsRefresh(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	ttl := 24 * time.Hour
	fresh := &Cache{FetchedAt: now.Add(-time.Hour).Format(time.RFC3339), Models: map[string]Override{"a": {}}}
	stale := &Cache{FetchedAt: now.Add(-25 * time.Hour).Format(time.RFC3339), Models: map[string]Override{"a": {}}}
	edge := &Cache{FetchedAt: now.Add(-ttl).Format(time.RFC3339), Models: map[string]Override{"a": {}}}

	if NeedsRefresh(nil, ttl, now) != true {
		t.Error("nil cache should need a refresh")
	}
	if NeedsRefresh(fresh, ttl, now) {
		t.Error("an hour-old cache should be fresh")
	}
	if !NeedsRefresh(stale, ttl, now) {
		t.Error("a 25-hour-old cache should be stale")
	}
	if !NeedsRefresh(edge, ttl, now) {
		t.Error("exactly at the TTL should be stale (>= comparison)")
	}
	if !NeedsRefresh(&Cache{FetchedAt: "not-a-time"}, ttl, now) {
		t.Error("an unparseable timestamp should force a refresh")
	}
	if !NeedsRefresh(&Cache{FetchedAt: fresh.FetchedAt}, ttl, now) {
		t.Error("an empty catalogue should force a refresh")
	}
}

func TestFetchAgainstLocalServer(t *testing.T) {
	// httptest keeps the suite hermetic: no real network is touched.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.ContentLength > 0 {
			t.Errorf("request carried a body of %d bytes; it must send nothing", r.ContentLength)
		}
		_, _ = w.Write([]byte(catalogueJSON))
	}))
	defer srv.Close()
	t.Setenv(EnvOffline, "")

	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	c, err := Fetch(srv.URL, 2*time.Second, now)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if c.Source != srv.URL {
		t.Errorf("Source = %q, want %q", c.Source, srv.URL)
	}
	if c.FetchedAt != now.Format(time.RFC3339) {
		t.Errorf("FetchedAt = %q, want %q", c.FetchedAt, now.Format(time.RFC3339))
	}
	if len(c.Models) == 0 {
		t.Error("no models parsed")
	}
}

func TestFetchFailures(t *testing.T) {
	t.Setenv(EnvOffline, "")
	now := time.Now()

	t.Run("http error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		if _, err := Fetch(srv.URL, time.Second, now); err == nil {
			t.Error("a 500 response should be an error")
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>not json</html>"))
		}))
		defer srv.Close()
		if _, err := Fetch(srv.URL, time.Second, now); err == nil {
			t.Error("a non-JSON body should be an error")
		}
	})

	t.Run("empty catalogue", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":[]}`))
		}))
		defer srv.Close()
		if _, err := Fetch(srv.URL, time.Second, now); err == nil {
			t.Error("an empty catalogue should be an error rather than wiping the cache")
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		if _, err := Fetch("http://127.0.0.1:1/models", 300*time.Millisecond, now); err == nil {
			t.Error("an unreachable host should be an error")
		}
	})
}

func TestOfflineForbidsFetching(t *testing.T) {
	t.Setenv(EnvOffline, "1")
	if !Offline() {
		t.Fatal("Offline() = false with the env var set")
	}
	// Even a reachable URL must be refused, and no server should be contacted.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("offline mode made a network request")
	}))
	defer srv.Close()
	if _, err := Fetch(srv.URL, time.Second, time.Now()); err == nil {
		t.Error("Fetch succeeded while offline")
	}
	// Clearing it restores the capability.
	t.Setenv(EnvOffline, "false")
	if Offline() {
		t.Error("Offline() = true for a falsy value")
	}
}

// TestCacheRoundTrip deliberately does NOT call config.Path() (directly or via
// DefaultPath/CachePath): it memoizes behind a process-wide sync.Once, so
// whichever test touches it first fixes the value for the whole run. The
// path-derivation assertions live in TestDefaultPathHonoursConfigDir, which is
// the designated first caller.
func TestCacheRoundTrip(t *testing.T) {
	// A missing cache is empty, not an error.
	missing, err := LoadCache(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || len(missing.Models) != 0 {
		t.Fatalf("LoadCache(missing) = %+v, %v; want an empty cache and no error", missing, err)
	}

	path := filepath.Join(t.TempDir(), "cache.json")
	want := &Cache{Source: "http://x", FetchedAt: "2026-09-22T00:00:00Z", Models: map[string]Override{"m": {InputPerM: 1}}}
	if err := SaveCache(path, want); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	got, err := LoadCache(path)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if got.Source != want.Source || got.Models["m"].InputPerM != 1 {
		t.Errorf("round trip = %+v", got)
	}
	if got.Schema == "" {
		t.Error("SaveCache should stamp $schema")
	}
	// Concise, diffable file.
	raw, _ := os.ReadFile(path)
	if !json.Valid(raw) || !strings.HasSuffix(string(raw), "\n") {
		t.Error("cache file should be valid JSON ending in a newline")
	}
	if leftovers, _ := filepath.Glob(path + "*tmp*"); len(leftovers) > 0 {
		t.Errorf("leftover temp files: %v", leftovers)
	}
}
