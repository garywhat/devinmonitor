// Package config manages devinmonitor's persistent configuration file.
// Config lives at ~/.devinmonitor/config.json (overridable via DEVINMONITOR_CONFIG_DIR).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Config is the devinmonitor configuration.
type Config struct {
	mu sync.RWMutex

	// Display
	Theme         string `json:"theme"`          // auto, dark, light, dracula, nord, etc.
	ColorScheme   string `json:"colorScheme"`    // auto, light, dark
	Locale        string `json:"locale"`         // en, zh, etc.
	TimeFormat    string `json:"timeFormat"`     // auto, 12h, 24h
	Timezone      string `json:"timezone"`       // auto or IANA tz
	DateFormat    string `json:"dateFormat"`     // optional date format
	AbbrevTokens  bool   `json:"abbreviateTokens"`
	NoEmoji       bool   `json:"noEmoji"`
	NoHeader      bool   `json:"noHeader"`

	// Refresh
	RefreshInterval int `json:"refreshInterval"` // seconds
	RefreshHz       float64 `json:"refreshHz"`   // display refresh rate

	// Budget
	BudgetDaily   float64 `json:"budgetDaily"`
	BudgetWeekly  float64 `json:"budgetWeekly"`
	BudgetMonthly float64 `json:"budgetMonthly"`

	// Currency
	Currency string `json:"currency"` // USD, EUR, CNY, etc.
	ACURate  float64 `json:"acuRate"` // ACU to USD conversion rate

	// Plan (Devin subscription)
	Plan          string  `json:"plan"`          // none, custom
	PlanMonthly   float64 `json:"planMonthly"`   // monthly plan cost in USD
	PlanACULimit  float64 `json:"planACULimit"`  // monthly ACU limit

	// Reset
	ResetHour int `json:"resetHour"` // 0-23, daily reset hour

	// Notifications
	NotifyDesktop bool   `json:"notifyDesktop"`
	NotifyWebhook string `json:"notifyWebhook"` // Discord/Slack/Telegram URL

	// Model aliases: maps provider model names to canonical names for pricing.
	ModelAliases map[string]string `json:"modelAliases"`

	// Custom pricing overrides (per million tokens).
	CustomPricing map[string]CustomPricing `json:"customPricing"`

	// Saved flags (preferences persisted between runs).
	SavedFlags map[string]string `json:"savedFlags"`
}

// CustomPricing is a user-defined pricing entry.
type CustomPricing struct {
	InputPerM     float64 `json:"inputPerM"`
	OutputPerM    float64 `json:"outputPerM"`
	CacheReadPerM float64 `json:"cacheReadPerM"`
	CacheWritePerM float64 `json:"cacheWritePerM"`
}

var (
	globalConfig *Config
	globalPath   string
	once         sync.Once
)

// Path returns the config file path.
func Path() string {
	once.Do(func() {
		if env := os.Getenv("DEVINMONITOR_CONFIG_DIR"); env != "" {
			globalPath = filepath.Join(env, "config.json")
			return
		}
		home, _ := os.UserHomeDir()
		var dir string
		switch runtime.GOOS {
		case "windows":
			appdata := os.Getenv("APPDATA")
			if appdata != "" {
				dir = filepath.Join(appdata, "devinmonitor")
			}
		default:
			dir = filepath.Join(home, ".devinmonitor")
		}
		globalPath = filepath.Join(dir, "config.json")
	})
	return globalPath
}

// Load reads the config file. Returns a zero-value Config if file is missing.
func Load() *Config {
	cfg := &Config{
		Theme:          "auto",
		ColorScheme:    "auto",
		Locale:         "en",
		TimeFormat:     "auto",
		Timezone:       "auto",
		RefreshInterval: 3,
		RefreshHz:      1.0,
		Currency:       "USD",
		Plan:           "none",
	}
	p := Path()
	data, err := os.ReadFile(p)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, cfg)
	return cfg
}

// Save writes the config to disk atomically.
func Save(cfg *Config) error {
	cfg.mu.Lock()
	defer cfg.mu.Unlock()
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Global returns the singleton config, loading on first access.
func Global() *Config {
	if globalConfig == nil {
		globalConfig = Load()
	}
	return globalConfig
}

// SaveGlobal persists the current global config.
func SaveGlobal() error {
	if globalConfig == nil {
		return nil
	}
	return Save(globalConfig)
}
