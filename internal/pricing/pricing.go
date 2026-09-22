// Package pricing owns devinmonitor's hand-editable model price-override file:
//
//	<config dir>/pricing.json
//
// Overrides used to live inside config.json under "customPricing"; this file
// is now the canonical place for new ones, because a standalone document can
// carry a "$schema" pointer and so gets editor autocomplete and validation
// for free. A user can fix or add a model's pricing without rebuilding the
// binary.
//
// The package is intentionally forgiving on read (unknown keys are ignored, a
// missing file is not an error) and strict only in Validate, which is what
// reports problems to a human.
package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/state"
)

// SchemaURL is the published JSON Schema for the override file.
const SchemaURL = "https://raw.githubusercontent.com/garywhat/devinmonitor/main/docs/pricing.schema.json"

// Override is one model's user-supplied pricing, in USD per 1M tokens.
type Override struct {
	InputPerM      float64 `json:"inputPerM"`
	OutputPerM     float64 `json:"outputPerM"`
	CacheReadPerM  float64 `json:"cacheReadPerM"`
	CacheWritePerM float64 `json:"cacheWritePerM"`
	Free           bool    `json:"free,omitempty"`
}

// File is the on-disk shape of the override file.
type File struct {
	Schema string              `json:"$schema,omitempty"`
	Models map[string]Override `json:"models"`
}

// Source reports which table produced a pricing entry.
type Source string

const (
	SourceOverride Source = "override"
	SourceBuiltin  Source = "builtin"
)

// DefaultPath returns <config dir>/pricing.json, where <config dir> is
// filepath.Dir(config.Path()) so it honours DEVINMONITOR_CONFIG_DIR.
func DefaultPath() string {
	return filepath.Join(filepath.Dir(config.Path()), "pricing.json")
}

// emptyFile is the usable-but-empty document returned for a missing file.
func emptyFile() *File {
	return &File{Models: map[string]Override{}}
}

// Load reads the override file at path.
//
// A MISSING file returns an empty (non-nil) File and a nil error — absence is
// not a failure. Invalid JSON returns an error. Unknown top-level keys and
// unknown per-model keys are tolerated on read (forward compatibility: a file
// written by a newer devinmonitor must not break an older one); Validate is
// what reports them.
//
// On success f.Models is always non-nil, so callers never need a nil check.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyFile(), nil
		}
		return nil, fmt.Errorf("pricing: read %s: %w", path, err)
	}

	// Decoding into the struct (rather than a strict decoder) is the whole
	// forward-compatibility story: encoding/json drops keys with no field.
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("pricing: parse %s: %w", path, err)
	}
	if f.Models == nil {
		// Covers `{}`, `{"models":null}` and a bare `null`.
		f.Models = map[string]Override{}
	}
	return &f, nil
}

// Save writes the file atomically with the $schema field set to SchemaURL and
// Models non-nil (so an empty file serializes as `"models": {}`). Parent
// directories are created. The output is indented JSON with a trailing
// newline.
//
// f is updated in place to match what was written, so a caller can keep using
// the same value without reloading.
func Save(path string, f *File) error {
	if f == nil {
		return errors.New("pricing: cannot save a nil override file")
	}

	out := File{Schema: SchemaURL, Models: f.Models}
	if out.Models == nil {
		out.Models = map[string]Override{}
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("pricing: encode override file: %w", err)
	}
	data = append(data, '\n')

	// Reuse the shared atomic writer instead of hand-rolling temp+rename: it
	// creates the parent directories, syncs, and cleans up on every failure.
	if err := state.WriteAtomic(path, data); err != nil {
		return err
	}

	f.Schema = SchemaURL
	f.Models = out.Models
	return nil
}

// Validate returns human-readable problems with the file, each naming the
// offending model and field. It returns nil when the file is fine.
//
// Rules: every numeric field must be >= 0 and finite; a model must not have
// all four rates equal to 0 unless Free is true (that is far more likely to be
// a mistake than an intentional zero); model names must not be empty or
// whitespace-only.
//
// Problems are reported in sorted model-name order so output is stable.
func Validate(f *File) []string {
	if f == nil {
		return []string{"pricing: file is nil"}
	}

	names := make([]string, 0, len(f.Models))
	for name := range f.Models {
		names = append(names, name)
	}
	sort.Strings(names)

	// Declared nil, not empty: no problems means a nil slice.
	var problems []string

	for _, name := range names {
		o := f.Models[name]
		label := fmt.Sprintf("model %q", name)

		switch {
		case name == "":
			problems = append(problems, `model "": name is empty`)
		case strings.TrimSpace(name) == "":
			problems = append(problems, fmt.Sprintf("model %q: name is whitespace-only", name))
		}

		for _, field := range []struct {
			name  string
			value float64
		}{
			{"inputPerM", o.InputPerM},
			{"outputPerM", o.OutputPerM},
			{"cacheReadPerM", o.CacheReadPerM},
			{"cacheWritePerM", o.CacheWritePerM},
		} {
			switch {
			case math.IsNaN(field.value):
				problems = append(problems, fmt.Sprintf("%s: %s is NaN", label, field.name))
			case math.IsInf(field.value, 0):
				problems = append(problems, fmt.Sprintf("%s: %s is not finite (%v)", label, field.name, field.value))
			case field.value < 0:
				problems = append(problems, fmt.Sprintf("%s: %s is negative (%g)", label, field.name, field.value))
			}
		}

		if !o.Free &&
			o.InputPerM == 0 && o.OutputPerM == 0 &&
			o.CacheReadPerM == 0 && o.CacheWritePerM == 0 {
			problems = append(problems, fmt.Sprintf(
				`%s: all four rates are zero; set "free": true if this model is genuinely free`, label))
		}
	}

	return problems
}

// Resolve returns the effective pricing for a model name: the user override
// when present, otherwise model.LookupPricing(name). It never returns a
// half-applied merge — an override replaces the built-in entry wholesale.
//
// The returned Pricing.Model is always the requested name, never the built-in
// table's canonical name, so callers can display exactly what they asked for
// (which is what makes prefix matches like "claude-sonnet-4-5-20250929"
// legible in a report).
func Resolve(f *File, name string) (model.Pricing, Source) {
	if o, ok := f.Get(name); ok {
		return model.Pricing{
			Model:          name,
			InputPerM:      o.InputPerM,
			OutputPerM:     o.OutputPerM,
			CacheReadPerM:  o.CacheReadPerM,
			CacheWritePerM: o.CacheWritePerM,
			Free:           o.Free,
		}, SourceOverride
	}

	p := model.LookupPricing(name)
	p.Model = name
	return p, SourceBuiltin
}

// Get returns the override for name and whether it exists.
// A nil *File has no overrides.
func (f *File) Get(name string) (Override, bool) {
	if f == nil || f.Models == nil {
		return Override{}, false
	}
	o, ok := f.Models[name]
	return o, ok
}

// Set stores an override for name, creating the Models map when nil.
func (f *File) Set(name string, o Override) {
	if f == nil {
		return
	}
	if f.Models == nil {
		f.Models = map[string]Override{}
	}
	f.Models[name] = o
}

// Remove deletes an override for name, reporting whether anything was removed.
func (f *File) Remove(name string) bool {
	if f == nil || f.Models == nil {
		return false
	}
	if _, ok := f.Models[name]; !ok {
		return false
	}
	delete(f.Models, name)
	return true
}
