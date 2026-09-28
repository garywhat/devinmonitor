package config

import (
	"encoding/json"
	"os"
	"testing"
)

// TestLoadDoesNotSeedLocale is the regression guard for a bug that a locale
// feature introduced and that no other test could have caught.
//
// Load() used to seed Locale with "en". Because SaveGlobal marshals the whole
// struct, any `config set <anything>` therefore wrote a "locale" key the user
// had never chosen:
//
//	$ devinmonitor sessions                      # zh_CN system -> 标题
//	$ devinmonitor config set theme dark
//	$ devinmonitor sessions                      # -> Title   (silently English)
//
// The reader of that key (applyConfigLocale in main.go) cannot tell "the user
// chose English" from "a default was written for them", so the seeded default
// outranked system detection. Leaving the field empty means "unset", which is
// what the reader already checks for.
func TestLoadDoesNotSeedLocale(t *testing.T) {
	t.Setenv("DEVINMONITOR_CONFIG_DIR", t.TempDir())
	cfg := Load()
	if cfg.Locale != "" {
		t.Errorf("Load() seeded Locale = %q; it must stay empty so an absent key does not outrank system detection", cfg.Locale)
	}
}

// TestSavingAnUnrelatedSettingDoesNotClaimALocale walks the exact sequence that
// produced the bug: load the defaults, change one unrelated field, save, and
// check that the file has not gained a locale CHOICE.
//
// An explicit empty value is fine -- the reader treats "" as unset -- what
// matters is that it is not "en".
func TestSavingAnUnrelatedSettingDoesNotClaimALocale(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVINMONITOR_CONFIG_DIR", dir)

	// Global() is what the CLI mutates and saves (SaveGlobal is a no-op when
	// nothing has been loaded into that slot), so the test uses the same path.
	cfg := Global()
	cfg.Theme = "dark"
	if err := SaveGlobal(); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}

	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse written config: %v", err)
	}
	loc, present := raw["locale"]
	if !present {
		return // absent is the ideal: nothing to misinterpret
	}
	var s string
	if err := json.Unmarshal(loc, &s); err != nil {
		t.Fatalf("locale is not a string: %s", loc)
	}
	if s != "" {
		t.Errorf("changing an unrelated setting wrote locale=%q; a user who never chose a locale must not appear to have chosen one", s)
	}
}

// TestLoadDefaultsAreIntact makes sure removing the locale seed did not remove
// the other defaults by accident.
func TestLoadDefaultsAreIntact(t *testing.T) {
	t.Setenv("DEVINMONITOR_CONFIG_DIR", t.TempDir())
	cfg := Load()
	for name, got := range map[string]string{
		"theme":       cfg.Theme,
		"colorScheme": cfg.ColorScheme,
		"timeFormat":  cfg.TimeFormat,
		"timezone":    cfg.Timezone,
		"currency":    cfg.Currency,
		"plan":        cfg.Plan,
	} {
		if got == "" {
			t.Errorf("%s default is empty; removing the locale seed must not disturb the others", name)
		}
	}
	if cfg.RefreshInterval == 0 {
		t.Error("refreshInterval default is zero")
	}
}
