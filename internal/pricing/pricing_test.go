package pricing

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
)

// TestDefaultPathHonoursConfigDir must stay the FIRST test in this package:
// config.Path() memoizes its answer process-wide, so the env var has to be set
// before anything else resolves the config path. No other test here calls it.
func TestDefaultPathHonoursConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVINMONITOR_CONFIG_DIR", dir)

	got := DefaultPath()
	want := filepath.Join(dir, "pricing.json")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
	if filepath.Base(got) != "pricing.json" {
		t.Fatalf("DefaultPath() = %q, want a path ending in pricing.json", got)
	}
	if filepath.Dir(got) != filepath.Dir(config.Path()) {
		t.Fatalf("DefaultPath() dir = %q, want config dir %q", filepath.Dir(got), filepath.Dir(config.Path()))
	}
}

func TestLoadMissingFileIsEmptyNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) error = %v, want nil", path, err)
	}
	if f == nil {
		t.Fatal("Load returned a nil File for a missing path")
	}
	if f.Models == nil {
		t.Fatal("Load returned a nil Models map; callers must never need a nil check")
	}
	if len(f.Models) != 0 {
		t.Fatalf("Models = %v, want empty", f.Models)
	}
	if f.Schema != "" {
		t.Fatalf("Schema = %q, want empty", f.Schema)
	}

	// The returned value must be immediately usable.
	if _, ok := f.Get("anything"); ok {
		t.Fatal("Get on an empty file reported an override")
	}
}

func TestLoadTolerant(t *testing.T) {
	realistic := `{
  "$schema": "https://raw.githubusercontent.com/garywhat/devinmonitor/main/docs/pricing.schema.json",
  "models": {
    "claude-sonnet-4-5": {
      "inputPerM": 3,
      "outputPerM": 15,
      "cacheReadPerM": 0.3,
      "cacheWritePerM": 3.75
    },
    "glm-5-2": {"free": true}
  }
}`

	// Unknown keys at both levels plus a known model: a file written by a
	// newer binary must still load.
	forwardCompat := `{
  "someFutureTopLevelKey": {"nested": [1, 2, 3]},
  "models": {
    "new-model": {"inputPerM": 1, "outputPerM": 2, "someFutureField": "ignored"}
  }
}`

	cases := []struct {
		name    string
		content string
		check   func(t *testing.T, f *File)
	}{
		{
			name:    "empty object",
			content: `{}`,
			check: func(t *testing.T, f *File) {
				if len(f.Models) != 0 {
					t.Fatalf("Models = %v, want empty", f.Models)
				}
			},
		},
		{
			name:    "null models",
			content: `{"models":null}`,
			check: func(t *testing.T, f *File) {
				if len(f.Models) != 0 {
					t.Fatalf("Models = %v, want empty", f.Models)
				}
			},
		},
		{
			name:    "bare json null",
			content: `null`,
			check: func(t *testing.T, f *File) {
				if f.Models == nil {
					t.Fatal("Models is nil after loading null")
				}
			},
		},
		{
			name:    "realistic",
			content: realistic,
			check: func(t *testing.T, f *File) {
				if f.Schema != SchemaURL {
					t.Fatalf("Schema = %q, want %q", f.Schema, SchemaURL)
				}
				if len(f.Models) != 2 {
					t.Fatalf("len(Models) = %d, want 2", len(f.Models))
				}
				sn, ok := f.Get("claude-sonnet-4-5")
				if !ok {
					t.Fatal("claude-sonnet-4-5 missing")
				}
				if sn.InputPerM != 3 || sn.OutputPerM != 15 || sn.CacheReadPerM != 0.3 || sn.CacheWritePerM != 3.75 {
					t.Fatalf("claude-sonnet-4-5 = %+v, want the values from the file", sn)
				}
				if sn.Free {
					t.Fatal("claude-sonnet-4-5 reported Free = true")
				}
				free, ok := f.Get("glm-5-2")
				if !ok || !free.Free {
					t.Fatalf("glm-5-2 = %+v, %v; want a free override", free, ok)
				}
				if free.InputPerM != 0 {
					t.Fatalf("glm-5-2 InputPerM = %v, want 0", free.InputPerM)
				}
			},
		},
		{
			name:    "forward compatible unknown keys",
			content: forwardCompat,
			check: func(t *testing.T, f *File) {
				o, ok := f.Get("new-model")
				if !ok {
					t.Fatal("new-model missing; unknown keys must not break the load")
				}
				if o.InputPerM != 1 || o.OutputPerM != 2 {
					t.Fatalf("new-model = %+v, want input 1 output 2", o)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pricing.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			f, err := Load(path)
			if err != nil {
				t.Fatalf("Load error = %v, want nil", err)
			}
			if f == nil {
				t.Fatal("Load returned a nil File")
			}
			if f.Models == nil {
				t.Fatal("Models is nil after a successful Load")
			}
			tc.check(t, f)
		})
	}
}

func TestLoadInvalidJSONReturnsError(t *testing.T) {
	for _, content := range []string{
		`{"models": {`,
		`not json at all`,
		`{"models": {"m": {"inputPerM": "three"}}}`,
	} {
		t.Run(content, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pricing.json")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			f, err := Load(path)
			if err == nil {
				t.Fatalf("Load(%q) error = nil, want a parse error (file = %+v)", content, f)
			}
			if f != nil {
				t.Fatalf("Load(%q) returned a File alongside the error: %+v", content, f)
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pricing.json")

	want := map[string]Override{
		"claude-sonnet-4-5": {InputPerM: 3, OutputPerM: 15, CacheReadPerM: 0.3, CacheWritePerM: 3.75},
		"free-model":        {Free: true},
	}
	f := &File{Models: want}
	if err := Save(path, f); err != nil {
		t.Fatalf("Save error = %v", err)
	}

	// The in-memory value is kept in step with what was written.
	if f.Schema != SchemaURL {
		t.Fatalf("f.Schema = %q after Save, want %q", f.Schema, SchemaURL)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	got := string(raw)

	if !strings.Contains(got, `"$schema"`) {
		t.Fatalf("saved file has no %q key:\n%s", "$schema", got)
	}
	if !strings.Contains(got, SchemaURL) {
		t.Fatalf("saved file does not point at SchemaURL:\n%s", got)
	}
	if !strings.Contains(got, "\n  \"$schema\":") {
		t.Fatalf("saved file is not indented JSON:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("saved file does not end with a newline: %q", got[len(got)-20:])
	}
	// Fully decoded: proves the document is valid JSON with the schema field.
	var decoded File
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("saved file is not valid JSON: %v\n%s", err, got)
	}
	if decoded.Models == nil {
		t.Fatal("saved file decoded to nil Models")
	}
	if !reflect.DeepEqual(decoded.Models, want) {
		t.Fatalf("decoded Models = %+v, want %+v", decoded.Models, want)
	}

	// No temp file survives the atomic write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Fatalf("glob found leftover temp files: %v", left)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*.tmp")); len(left) != 0 {
		t.Fatalf("glob found leftover hidden temp files: %v", left)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load after Save error = %v", err)
	}
	if !reflect.DeepEqual(loaded.Models, want) {
		t.Fatalf("round-tripped Models = %+v, want %+v", loaded.Models, want)
	}
	if loaded.Schema != SchemaURL {
		t.Fatalf("round-tripped Schema = %q, want %q", loaded.Schema, SchemaURL)
	}
}

func TestSaveEmptyFileSerializesEmptyModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")

	if err := Save(path, &File{}); err != nil {
		t.Fatalf("Save error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), `"models": {}`) {
		t.Fatalf("empty file did not serialize Models as {}:\n%s", raw)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("saved file does not end with a newline:\n%q", raw)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if loaded.Models == nil || len(loaded.Models) != 0 {
		t.Fatalf("Models = %v, want non-nil and empty", loaded.Models)
	}
}

func TestSaveCreatesMissingParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "pricing.json")

	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("precondition failed: %s already exists (err = %v)", filepath.Dir(path), err)
	}
	if err := Save(path, &File{Models: map[string]Override{"m": {InputPerM: 1}}}); err != nil {
		t.Fatalf("Save error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat saved file: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if o, ok := loaded.Get("m"); !ok || o.InputPerM != 1 {
		t.Fatalf("loaded override = %+v, %v; want inputPerM 1", o, ok)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name     string
		file     *File
		wantNil  bool
		mustHave []string
	}{
		{
			name:    "fully valid",
			file:    &File{Models: map[string]Override{"gpt-4o": {InputPerM: 2.5, OutputPerM: 10, CacheReadPerM: 1.25}}},
			wantNil: true,
		},
		{
			name:    "all zero with free true is valid",
			file:    &File{Models: map[string]Override{"glm-5-2": {Free: true}}},
			wantNil: true,
		},
		{
			name:    "empty file is valid",
			file:    &File{Models: map[string]Override{}},
			wantNil: true,
		},
		{
			name:     "negative rate",
			file:     &File{Models: map[string]Override{"bad-model": {InputPerM: -1, OutputPerM: 15}}},
			mustHave: []string{"bad-model", "inputPerM"},
		},
		{
			name:     "nan rate",
			file:     &File{Models: map[string]Override{"nan-model": {InputPerM: 1, OutputPerM: math.NaN()}}},
			mustHave: []string{"nan-model", "outputPerM", "NaN"},
		},
		{
			name:     "infinite rate",
			file:     &File{Models: map[string]Override{"inf-model": {InputPerM: 1, CacheReadPerM: math.Inf(1)}}},
			mustHave: []string{"inf-model", "cacheReadPerM"},
		},
		{
			name:     "all zero without free",
			file:     &File{Models: map[string]Override{"zero-model": {}}},
			mustHave: []string{"zero-model", "free"},
		},
		{
			name:     "empty model name",
			file:     &File{Models: map[string]Override{"": {InputPerM: 1}}},
			mustHave: []string{"name is empty"},
		},
		{
			name:     "whitespace-only model name",
			file:     &File{Models: map[string]Override{"   ": {InputPerM: 1}}},
			mustHave: []string{"whitespace-only"},
		},
		{
			name:     "nil file",
			file:     nil,
			mustHave: []string{"nil"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := Validate(tc.file)
			if tc.wantNil {
				if len(problems) != 0 {
					t.Fatalf("Validate = %v, want nil", problems)
				}
				return
			}
			if len(problems) == 0 {
				t.Fatal("Validate = nil, want at least one problem")
			}
			for _, want := range tc.mustHave {
				joined := strings.Join(problems, "\n")
				if !strings.Contains(joined, want) {
					t.Fatalf("problems do not mention %q:\n%s", want, joined)
				}
			}
		})
	}
}

func TestValidateReportsEveryOffendingModel(t *testing.T) {
	f := &File{Models: map[string]Override{
		"z-last":  {InputPerM: -1},
		"a-first": {},
	}}

	problems := Validate(f)
	if len(problems) != 2 {
		t.Fatalf("Validate = %v, want exactly 2 problems", problems)
	}
	// Sorted by model name, so output is stable for a human.
	if !strings.Contains(problems[0], "a-first") || !strings.Contains(problems[1], "z-last") {
		t.Fatalf("problems are not in sorted model order: %v", problems)
	}
}

func TestResolve(t *testing.T) {
	f := &File{Models: map[string]Override{
		"claude-sonnet-4-5": {InputPerM: 7, OutputPerM: 70, CacheReadPerM: 0.7, CacheWritePerM: 8},
		"brand-new-model":   {InputPerM: 1, OutputPerM: 2, CacheReadPerM: 0.1, CacheWritePerM: 0.2},
		"free-override":     {Free: true},
	}}

	t.Run("override wins wholesale", func(t *testing.T) {
		p, src := Resolve(f, "claude-sonnet-4-5")
		if src != SourceOverride {
			t.Fatalf("Source = %q, want %q", src, SourceOverride)
		}
		if p.Model != "claude-sonnet-4-5" {
			t.Fatalf("Model = %q, want the requested name", p.Model)
		}
		want := model.Pricing{Model: "claude-sonnet-4-5", InputPerM: 7, OutputPerM: 70, CacheReadPerM: 0.7, CacheWritePerM: 8}
		if p != want {
			t.Fatalf("Resolve = %+v, want %+v (no half-applied merge with the built-in entry)", p, want)
		}
	})

	t.Run("override for an unknown model", func(t *testing.T) {
		p, src := Resolve(f, "brand-new-model")
		if src != SourceOverride {
			t.Fatalf("Source = %q, want %q", src, SourceOverride)
		}
		if p.InputPerM != 1 || p.OutputPerM != 2 {
			t.Fatalf("Resolve = %+v, want the override values", p)
		}
	})

	t.Run("free override", func(t *testing.T) {
		p, src := Resolve(f, "free-override")
		if src != SourceOverride || !p.Free {
			t.Fatalf("Resolve = %+v, %q; want a free override", p, src)
		}
	})

	t.Run("falls back to builtin", func(t *testing.T) {
		p, src := Resolve(f, "gpt-4o")
		if src != SourceBuiltin {
			t.Fatalf("Source = %q, want %q", src, SourceBuiltin)
		}
		if p.Model != "gpt-4o" {
			t.Fatalf("Model = %q, want the requested name", p.Model)
		}
		builtin := model.LookupPricing("gpt-4o")
		builtin.Model = "gpt-4o"
		if p != builtin {
			t.Fatalf("Resolve = %+v, want builtin %+v", p, builtin)
		}
		if p.InputPerM == 0 {
			t.Fatalf("Resolve = %+v, want a non-zero builtin price", p)
		}
	})

	t.Run("builtin fuzzy match keeps the requested name", func(t *testing.T) {
		const name = "claude-sonnet-4-5-20250929"
		p, src := Resolve(&File{}, name)
		if src != SourceBuiltin {
			t.Fatalf("Source = %q, want %q", src, SourceBuiltin)
		}
		if p.Model != name {
			t.Fatalf("Model = %q, want the requested name %q", p.Model, name)
		}
	})

	t.Run("nil file falls back to builtin", func(t *testing.T) {
		var nilFile *File
		p, src := Resolve(nilFile, "claude-sonnet-4-5")
		if src != SourceBuiltin {
			t.Fatalf("Source = %q, want %q", src, SourceBuiltin)
		}
		if p.Model != "claude-sonnet-4-5" {
			t.Fatalf("Model = %q, want the requested name", p.Model)
		}
	})

	t.Run("unknown model is zero pricing", func(t *testing.T) {
		p, src := Resolve(&File{}, "no-such-model")
		if src != SourceBuiltin {
			t.Fatalf("Source = %q, want %q", src, SourceBuiltin)
		}
		if p.Model != "no-such-model" || p.InputPerM != 0 || p.OutputPerM != 0 || p.Free {
			t.Fatalf("Resolve = %+v, want zero-value pricing with the requested name", p)
		}
	})
}

func TestSetGetRemove(t *testing.T) {
	t.Run("set creates the map", func(t *testing.T) {
		f := &File{}
		if _, ok := f.Get("m"); ok {
			t.Fatal("Get reported an override on an empty file")
		}
		f.Set("m", Override{InputPerM: 1, OutputPerM: 2})
		if f.Models == nil {
			t.Fatal("Set did not create the Models map")
		}
		o, ok := f.Get("m")
		if !ok {
			t.Fatal("Get did not find the override Set just stored")
		}
		if o.InputPerM != 1 || o.OutputPerM != 2 {
			t.Fatalf("Get = %+v, want the stored override", o)
		}
	})

	t.Run("set overwrites", func(t *testing.T) {
		f := &File{Models: map[string]Override{"m": {InputPerM: 1}}}
		f.Set("m", Override{InputPerM: 9})
		if o, _ := f.Get("m"); o.InputPerM != 9 {
			t.Fatalf("Get = %+v, want the replacement", o)
		}
	})

	t.Run("remove", func(t *testing.T) {
		f := &File{Models: map[string]Override{"m": {InputPerM: 1}}}
		if !f.Remove("m") {
			t.Fatal("Remove = false for a present key")
		}
		if _, ok := f.Get("m"); ok {
			t.Fatal("the key is still present after Remove")
		}
		if f.Remove("m") {
			t.Fatal("Remove = true for an absent key")
		}
		if len(f.Models) != 0 {
			t.Fatalf("Models = %v, want empty", f.Models)
		}
	})

	t.Run("nil receiver is safe", func(t *testing.T) {
		var f *File
		if _, ok := f.Get("m"); ok {
			t.Fatal("Get on a nil receiver reported an override")
		}
		if f.Remove("m") {
			t.Fatal("Remove on a nil receiver reported a removal")
		}
		f.Set("m", Override{InputPerM: 1}) // must not panic
		if f != nil {
			t.Fatal("Set on a nil receiver mutated something")
		}
	})
}
