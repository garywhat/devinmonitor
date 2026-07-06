// Package i18n provides locale-aware message translation for devinmonitor.
//
// It loads TOML message catalogs embedded at build time and exposes a T()
// function for lookups. Uses a simple flat key-value map approach instead of
// go-i18n's message-file format, to avoid constraints on key naming.
// Technical terms (token, cost, ttft) are intentionally kept in English
// across locales to avoid awkward translations.
package i18n

import (
	"embed"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

//go:embed en.toml zh.toml
var localeFS embed.FS

var (
	catalogs  map[string]map[string]string // locale -> flat key -> value
	locales   = []string{"en", "zh"}
	once      sync.Once
	loadErr   error
	current   string
	currentMu sync.RWMutex
)

// Init initializes the catalogs once. Subsequent calls are no-ops.
func Init() error {
	once.Do(func() {
		catalogs = map[string]map[string]string{}
		for _, loc := range locales {
			data, err := localeFS.ReadFile(loc + ".toml")
			if err != nil {
				loadErr = fmt.Errorf("i18n: read %s.toml: %w", loc, err)
				return
			}
			flat, err := parseFlatTOML(string(data))
			if err != nil {
				loadErr = fmt.Errorf("i18n: parse %s.toml: %w", loc, err)
				return
			}
			catalogs[loc] = flat
		}
		current = detectLocale()
	})
	return loadErr
}

// parseFlatTOML parses TOML and flattens nested tables into "a.b.c" keys.
func parseFlatTOML(s string) (map[string]string, error) {
	var raw map[string]interface{}
	if err := toml.Unmarshal([]byte(s), &raw); err != nil {
		return nil, err
	}
	out := map[string]string{}
	flatten("", raw, out)
	return out, nil
}

func flatten(prefix string, m map[string]interface{}, out map[string]string) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case string:
			out[key] = val
		case map[string]interface{}:
			flatten(key, val, out)
		}
	}
}

// detectLocale picks a locale from env vars / system LANG / OS settings.
func detectLocale() string {
	if v := os.Getenv("DEVINMONITOR_LOCALE"); v != "" {
		return normalize(v)
	}
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(env); v != "" {
			return normalize(v)
		}
	}
	// Fall back to OS-level detection (Windows API, etc.).
	return detectOSLocale()
}

// detectOSLocale tries to detect the locale from the operating system.
// On Windows it calls GetUserDefaultLocaleName; on other platforms it
// returns "en" (Unix systems typically set LANG already).
func detectOSLocale() string {
	return normalize(osLocaleName())
}

// normalize turns "zh_CN.UTF-8" / "zh-CN" / "zh-Hans" / "zh" into "zh".
func normalize(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "C") || strings.EqualFold(v, "POSIX") {
		return "en"
	}
	// Strip encoding suffix (.UTF-8) and region/script suffix (_CN, -CN, -Hans).
	// We only care about the language part (first 2 letters).
	lower := strings.ToLower(v)
	if strings.HasPrefix(lower, "zh") {
		return "zh"
	}
	return "en"
}

// SetLocale switches the active locale at runtime. Returns the actual locale set.
func SetLocale(loc string) string {
	loc = normalize(loc)
	currentMu.Lock()
	current = loc
	currentMu.Unlock()
	return loc
}

// Current returns the active locale code ("en" or "zh").
func Current() string {
	currentMu.RLock()
	defer currentMu.RUnlock()
	return current
}

// Cycle toggles between en and zh, returning the new locale.
func Cycle() string {
	if Current() == "zh" {
		return SetLocale("en")
	}
	return SetLocale("zh")
}

// Locales returns the list of supported locale codes.
func Locales() []string { return locales }

// T translates a message ID to the current locale, with optional template data.
// Missing keys fall back to English, then to the key itself.
// Template data uses {{.Field}} placeholders (text/template syntax).
func T(key string, data ...map[string]interface{}) string {
	if len(catalogs) == 0 {
		return key
	}
	loc := Current()
	msg := lookup(loc, key)
	if msg == "" && loc != "en" {
		msg = lookup("en", key)
	}
	if msg == "" {
		return key
	}
	if len(data) > 0 {
		msg = applyTemplate(msg, data[0])
	}
	return msg
}

func lookup(loc, key string) string {
	if c := catalogs[loc]; c != nil {
		return c[key]
	}
	return ""
}

// applyTemplate replaces {{.Field}} with values from data.
// Minimal text/template subset — avoids importing text/template for speed.
func applyTemplate(s string, data map[string]interface{}) string {
	for k, v := range data {
		s = strings.ReplaceAll(s, "{{."+k+"}}", fmt.Sprintf("%v", v))
	}
	return s
}
