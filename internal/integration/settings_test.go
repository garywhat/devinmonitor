package integration

import (
	"testing"

	"github.com/garywhat/devinmonitor/internal/config"
)

// TestNormalizeLocale pins the validation that makes `config set locale` honest.
//
// The validation is not decoration: i18n.SetLocale silently normalizes anything
// it does not recognise to "en", so without it `config set locale fr` would
// report success, persist "fr", and then run in English — a setting that looks
// applied and is not.
func TestNormalizeLocale(t *testing.T) {
	accept := map[string]string{
		"en":    "en",
		"zh":    "zh",
		"EN":    "en",
		"  zh ": "zh",
	}
	for in, want := range accept {
		got, err := normalizeLocale(in)
		if err != nil {
			t.Errorf("normalizeLocale(%q) rejected a supported locale: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}

	for _, in := range []string{"", "  ", "fr", "de-DE", "zh_CN", "en_US.UTF-8", "klingon"} {
		if got, err := normalizeLocale(in); err == nil {
			t.Errorf("normalizeLocale(%q) = %q, want an error (only the catalogued locales are real)", in, got)
		}
	}
}

// TestSetConfigKeyLocale covers the write path: the value is persisted, and a
// value that would not take effect is refused BEFORE anything is saved.
func TestSetConfigKeyLocale(t *testing.T) {
	cfg := &config.Config{}
	if err := setConfigKey(cfg, "locale", "zh"); err != nil {
		t.Fatalf("setConfigKey(locale, zh): %v", err)
	}
	if cfg.Locale != "zh" {
		t.Errorf("Locale = %q, want zh", cfg.Locale)
	}

	if err := setConfigKey(cfg, "locale", "fr"); err == nil {
		t.Error("setConfigKey(locale, fr) was accepted; it would silently run in English")
	}
	if cfg.Locale != "zh" {
		t.Errorf("a rejected locale changed the value to %q; the config must be left alone", cfg.Locale)
	}
}
