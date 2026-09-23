package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/pricing"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// ---- Config Command (#78) ----

var cmdConfig = func() *cobra.Command {
	// configSchemaURL mirrors the $id of docs/config.schema.json, so the
	// printed URL and the one written into a config file's "$schema" agree.
	const configSchemaURL = "https://raw.githubusercontent.com/garywhat/devinmonitor/main/docs/config.schema.json"

	c := &cobra.Command{
		Use:   "config [show|set|reset|timezone|reset-hour|model-alias|schema]",
		Short: i18n.T("cmd.config"),
		Long: i18n.T("cmd.config") + "\n\n" +
			"  show       " + i18n.T("cmd.configShow") + "\n" +
			"  set <key> <value>\n" +
			"  reset\n" +
			"  timezone [show|set <tz>|auto]\n" +
			"  reset-hour <hour>\n" +
			"  model-alias [list|add <alias> <canonical>|remove <alias>]\n" +
			"  schema     " + i18n.T("help.configSchema"),
		// Default action when no subcommand: show config.
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) > 0 {
				fmt.Fprintf(os.Stderr, "unknown subcommand: %s (valid: show, set, reset, timezone, reset-hour, model-alias, schema)\n", args[0])
				os.Exit(1)
			}
			showConfig(config.Global())
		},
	}

	// Subcommand: config show
	c.AddCommand(&cobra.Command{
		Use:   "show",
		Short: i18n.T("cmd.configShow"),
		Run: func(cmd *cobra.Command, args []string) {
			showConfig(config.Global())
		},
	})

	// Subcommand: config schema
	c.AddCommand(&cobra.Command{
		Use:   "schema",
		Short: i18n.T("help.configSchema"),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Schema URL: %s\n", configSchemaURL)
			fmt.Printf("Config file: %s\n", config.Path())
			// The schema may also be checked out next to the working
			// directory; its absence is not an error.
			if _, err := os.Stat("docs/config.schema.json"); err == nil {
				fmt.Println("Local schema: docs/config.schema.json")
			}
		},
	})

	// Subcommand: config set <key> <value>
	c.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: i18n.T("cmd.configSet"),
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			if err := setConfigKey(cfg, args[0], args[1]); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			if err := config.SaveGlobal(); err != nil {
				fmt.Fprintf(os.Stderr, "save: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Set %s = %s\n", args[0], args[1])
		},
	})

	// Subcommand: config reset
	c.AddCommand(&cobra.Command{
		Use:   "reset",
		Short: i18n.T("cmd.configReset"),
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			*cfg = config.Config{}
			cfg.Theme = "auto"
			cfg.ColorScheme = "auto"
			cfg.Locale = "en"
			cfg.TimeFormat = "auto"
			cfg.Timezone = "auto"
			cfg.RefreshInterval = 500
			cfg.RefreshHz = 1.0
			cfg.Currency = "USD"
			cfg.Plan = "none"
			if err := config.SaveGlobal(); err != nil {
				fmt.Fprintf(os.Stderr, "save: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Configuration reset to defaults.")
		},
	})

	// Subcommand: config timezone [show|set <tz>|auto]
	c.AddCommand(cmdConfigTimezoneSub())

	// Subcommand: config reset-hour <hour>
	c.AddCommand(cmdConfigResetHourSub())

	// Subcommand: config model-alias [list|add <alias> <canonical>|remove <alias>]
	c.AddCommand(cmdModelAliasSub())

	return c
}

func showConfig(cfg *config.Config) {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	// Strip the mutex field (it's not serialized anyway due to lowercase).
	fmt.Println(string(data))
}

func setConfigKey(cfg *config.Config, key, val string) error {
	switch strings.ToLower(key) {
	case "theme":
		cfg.Theme = val
	case "colorscheme":
		cfg.ColorScheme = val
	case "locale":
		cfg.Locale = val
	case "timeformat":
		cfg.TimeFormat = val
	case "timezone":
		cfg.Timezone = val
	case "dateformat":
		cfg.DateFormat = val
	case "abbreviatetokens":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("invalid bool: %s", val)
		}
		cfg.AbbrevTokens = b
	case "noemoji":
		b, _ := strconv.ParseBool(val)
		cfg.NoEmoji = b
	case "noheader":
		b, _ := strconv.ParseBool(val)
		cfg.NoHeader = b
	case "refreshinterval":
		n, _ := strconv.Atoi(val)
		if n < 100 {
			n = 100 // keep in sync with live.MinIntervalMs
		}
		cfg.RefreshInterval = n
	case "refreshhz":
		f, _ := strconv.ParseFloat(val, 64)
		cfg.RefreshHz = f
	case "budgetdaily":
		f, _ := strconv.ParseFloat(val, 64)
		cfg.BudgetDaily = f
	case "budgetweekly":
		f, _ := strconv.ParseFloat(val, 64)
		cfg.BudgetWeekly = f
	case "budgetmonthly":
		f, _ := strconv.ParseFloat(val, 64)
		cfg.BudgetMonthly = f
	case "currency":
		cfg.Currency = val
	case "acurate":
		f, _ := strconv.ParseFloat(val, 64)
		cfg.ACURate = f
	case "plan":
		cfg.Plan = val
	case "planmonthly":
		f, _ := strconv.ParseFloat(val, 64)
		cfg.PlanMonthly = f
	case "planaculimit":
		f, _ := strconv.ParseFloat(val, 64)
		cfg.PlanACULimit = f
	case "resethour":
		n, _ := strconv.Atoi(val)
		if n < 0 || n > 23 {
			return fmt.Errorf("reset hour must be 0-23")
		}
		cfg.ResetHour = n
	case "notifydesktop":
		b, _ := strconv.ParseBool(val)
		cfg.NotifyDesktop = b
	case "notifywebhook":
		cfg.NotifyWebhook = val
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

// ---- Timezone subcommand (#92) ----

var cmdConfigTimezoneSub = func() *cobra.Command {
	c := &cobra.Command{
		Use:   "timezone [show|set <tz>|auto]",
		Short: i18n.T("cmd.configTimezone"),
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			if len(args) == 0 || args[0] == "show" {
				fmt.Printf("Current timezone: %s\n", cfg.Timezone)
				if cfg.Timezone == "auto" {
					_, offset := time.Now().Zone()
					fmt.Printf("Detected: %s (UTC%+.1f)\n", detectTimezone(), float64(offset)/3600)
				}
				return
			}
			switch args[0] {
			case "set":
				if len(args) < 2 {
					fmt.Fprintln(os.Stderr, "usage: config timezone set <tz>")
					os.Exit(1)
				}
				cfg.Timezone = args[1]
				saveConfig()
				fmt.Printf("Timezone set to: %s\n", args[1])
			case "auto":
				tz := detectTimezone()
				cfg.Timezone = tz
				saveConfig()
				fmt.Printf("Auto-detected timezone: %s\n", tz)
			default:
				fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
				os.Exit(1)
			}
		},
	}
	return c
}

// detectTimezone auto-detects the system timezone.
func detectTimezone() string {
	// Try TZ env var.
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	// Use time.Now().Zone() as a best-effort detection.
	zone, _ := time.Now().Zone()
	if zone != "" && zone != "Local" {
		// Try to find a matching IANA zone. This is a simplification.
		return zone
	}
	return "UTC"
}

// ---- Reset Hour subcommand (#93) ----

var cmdConfigResetHourSub = func() *cobra.Command {
	c := &cobra.Command{
		Use:   "reset-hour <hour>",
		Short: i18n.T("cmd.configResetHour"),
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			h, err := strconv.Atoi(args[0])
			if err != nil || h < 0 || h > 23 {
				fmt.Fprintln(os.Stderr, "reset hour must be an integer 0-23")
				os.Exit(1)
			}
			cfg := config.Global()
			cfg.ResetHour = h
			if err := config.SaveGlobal(); err != nil {
				fmt.Fprintf(os.Stderr, "save: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Reset hour set to: %d:00\n", h)
		},
	}
	return c
}

// ---- Model Aliases subcommand (#90) ----

var cmdModelAliasSub = func() *cobra.Command {
	c := &cobra.Command{
		Use:   "model-alias [list|add <alias> <canonical>|remove <alias>]",
		Short: i18n.T("cmd.configModelAlias"),
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			if len(args) == 0 || args[0] == "list" {
				if len(cfg.ModelAliases) == 0 {
					fmt.Println("No model aliases configured.")
					return
				}
				type kv struct{ k, v string }
				var pairs []kv
				for k, v := range cfg.ModelAliases {
					pairs = append(pairs, kv{k, v})
				}
				sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
				t := ui.NewTable("Alias", "Canonical Model")
				for _, p := range pairs {
					t.Row(p.k, p.v)
				}
				fmt.Println(t.String())
				return
			}
			switch args[0] {
			case "add":
				if len(args) < 3 {
					fmt.Fprintln(os.Stderr, "usage: config model-alias add <alias> <canonical>")
					os.Exit(1)
				}
				if cfg.ModelAliases == nil {
					cfg.ModelAliases = map[string]string{}
				}
				cfg.ModelAliases[args[1]] = args[2]
				saveConfig()
				fmt.Printf("Model alias added: %s -> %s\n", args[1], args[2])
			case "remove":
				if len(args) < 2 {
					fmt.Fprintln(os.Stderr, "usage: config model-alias remove <alias>")
					os.Exit(1)
				}
				delete(cfg.ModelAliases, args[1])
				saveConfig()
				fmt.Printf("Model alias removed: %s\n", args[1])
			default:
				fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
				os.Exit(1)
			}
		},
	}
	return c
}

// ---- Custom Pricing (#91) ----

var cmdPricing = func() *cobra.Command {
	var input, output, cacheRead, cacheWrite float64
	var sourceURL string
	var forceFetch bool
	var freeFlag bool
	var filePath string

	// pricingPath resolves the override file: --file when given, otherwise the
	// canonical <config dir>/pricing.json.
	pricingPath := func() string {
		if filePath != "" {
			return filePath
		}
		return pricing.DefaultPath()
	}

	// overridesTable renders the merged overrides table: pricing.json entries
	// first (source "file"), then legacy cfg.CustomPricing entries (source
	// "config"). A model present in the override file shadows its legacy
	// config entry, mirroring the precedence remove uses. ok is false when the
	// override file exists but could not be read; an empty string with ok true
	// means there are no overrides at all.
	overridesTable := func(cfg *config.Config) (string, bool) {
		type entry struct {
			model string
			o     pricing.Override
			src   string
		}
		f, err := pricing.Load(pricingPath())
		if err != nil {
			fmt.Fprintf(os.Stderr, "pricing: %v\n", err)
			return "", false
		}

		entries := make([]entry, 0, len(f.Models))
		for m, o := range f.Models {
			entries = append(entries, entry{model: m, o: o, src: "file"})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].model < entries[j].model })

		legacy := make([]string, 0, len(cfg.CustomPricing))
		for m := range cfg.CustomPricing {
			legacy = append(legacy, m)
		}
		sort.Strings(legacy)
		for _, m := range legacy {
			if _, inFile := f.Get(m); inFile {
				continue
			}
			p := cfg.CustomPricing[m]
			entries = append(entries, entry{
				model: m,
				o: pricing.Override{
					InputPerM:      p.InputPerM,
					OutputPerM:     p.OutputPerM,
					CacheReadPerM:  p.CacheReadPerM,
					CacheWritePerM: p.CacheWritePerM,
				},
				src: "config",
			})
		}

		if len(entries) == 0 {
			return "", true
		}
		t := ui.NewTable("Model", "Input/M", "Output/M", "Cache R/M", "Cache W/M", "Free", "Source")
		for _, e := range entries {
			free := "no"
			if e.o.Free {
				free = "yes"
			}
			t.Row(e.model,
				fmt.Sprintf("$%.2f", e.o.InputPerM),
				fmt.Sprintf("$%.2f", e.o.OutputPerM),
				fmt.Sprintf("$%.2f", e.o.CacheReadPerM),
				fmt.Sprintf("$%.2f", e.o.CacheWritePerM),
				free,
				e.src)
		}
		return t.String(), true
	}

	c := &cobra.Command{
		Use:   "pricing [list|overrides|set <model>|remove <model>|validate|schema|fetch|cache]",
		Short: i18n.T("cmd.pricing"),
		Long: i18n.T("cmd.pricing") + "\n\n" +
			"  list       built-in table plus every user override\n" +
			"  overrides  " + i18n.T("help.pricingOverrides") + "\n" +
			"  set <model> --input <usd> --output <usd> [--cache-read <usd>] [--cache-write <usd>] [--free]\n" +
			"  remove <model>\n" +
			"  validate   " + i18n.T("help.pricingValidate") + "\n" +
			"  schema     " + i18n.T("help.pricingSchema"),
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			sub := ""
			if len(args) > 0 {
				sub = args[0]
			}
			switch sub {
			case "", "list":
				// Show builtin pricing.
				fmt.Println("Built-in pricing:")
				t := ui.NewTable("Model", "Input/M", "Output/M", "Cache R/M", "Free")
				for _, p := range model.AllPricing() {
					free := "no"
					if p.Free {
						free = "yes"
					}
					t.Row(p.Model,
						fmt.Sprintf("$%.2f", p.InputPerM),
						fmt.Sprintf("$%.2f", p.OutputPerM),
						fmt.Sprintf("$%.2f", p.CacheReadPerM),
						free)
				}
				fmt.Println(t.String())

				// Show the merged overrides, file first then legacy config.
				fmt.Printf("\nOverride file: %s\n", pricingPath())
				table, ok := overridesTable(cfg)
				if !ok {
					os.Exit(1)
				}
				if table == "" {
					fmt.Println("No pricing overrides.")
				} else {
					fmt.Println(table)
				}
			case "overrides":
				fmt.Printf("Override file: %s\n", pricingPath())
				table, ok := overridesTable(cfg)
				if !ok {
					os.Exit(1)
				}
				if table == "" {
					fmt.Println("No pricing overrides.")
				} else {
					fmt.Println(table)
				}
				// The fetched catalogue can hold hundreds of models, so it is
				// summarised rather than listed: what the user needs to know is
				// that it is in effect, above the built-in table but below their
				// own overrides.
				if c, err := pricing.LoadCache(pricing.CachePath()); err == nil && c != nil && len(c.Models) > 0 {
					state := "fresh"
					if pricing.NeedsRefresh(c, pricing.DefaultCacheTTL, time.Now()) {
						state = "stale"
					}
					fmt.Printf("\nFetched catalogue (lower precedence than the entries above):\n")
					fmt.Printf("  %d models from %s, fetched %s (%s)\n", len(c.Models), c.Source, c.FetchedAt, state)
					fmt.Printf("  %s\n", pricing.CachePath())
				}
			case "set":
				if len(args) < 2 {
					fmt.Fprintln(os.Stderr, "usage: pricing set <model> --input <usd> --output <usd> [--cache-read <usd>] [--cache-write <usd>] [--free]")
					os.Exit(1)
				}
				m := args[1]
				path := pricingPath()
				f, err := pricing.Load(path)
				if err != nil {
					fmt.Fprintf(os.Stderr, "pricing: %v\n", err)
					os.Exit(1)
				}
				f.Set(m, pricing.Override{
					InputPerM:      input,
					OutputPerM:     output,
					CacheReadPerM:  cacheRead,
					CacheWritePerM: cacheWrite,
					Free:           freeFlag,
				})
				if err := pricing.Save(path, f); err != nil {
					fmt.Fprintf(os.Stderr, "pricing: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Custom pricing set for %s in %s\n", m, path)
			case "remove":
				if len(args) < 2 {
					fmt.Fprintln(os.Stderr, "usage: pricing remove <model>")
					os.Exit(1)
				}
				m := args[1]
				path := pricingPath()
				f, err := pricing.Load(path)
				if err != nil {
					fmt.Fprintf(os.Stderr, "pricing: %v\n", err)
					os.Exit(1)
				}
				if f.Remove(m) {
					if err := pricing.Save(path, f); err != nil {
						fmt.Fprintf(os.Stderr, "pricing: %v\n", err)
						os.Exit(1)
					}
					fmt.Printf("Custom pricing removed for %s (source: file, %s)\n", m, path)
					break
				}
				if cfg != nil && cfg.CustomPricing != nil {
					if _, ok := cfg.CustomPricing[m]; ok {
						delete(cfg.CustomPricing, m)
						saveConfig()
						fmt.Printf("Custom pricing removed for %s (source: config)\n", m)
						break
					}
				}
				fmt.Printf("No override for %s\n", m)
			case "validate":
				path := pricingPath()
				if _, err := os.Stat(path); os.IsNotExist(err) {
					fmt.Printf("No override file at %s; nothing to validate.\n", path)
					break
				}
				f, err := pricing.Load(path)
				if err != nil {
					fmt.Fprintf(os.Stderr, "pricing: %v\n", err)
					os.Exit(1)
				}
				if problems := pricing.Validate(f); len(problems) > 0 {
					for _, p := range problems {
						fmt.Fprintln(os.Stderr, p)
					}
					os.Exit(1)
				}
				fmt.Printf("OK: %s is valid\n", path)
			case "fetch":
				// Explicit, user-initiated download. The catalogue is public and
				// the request carries no local data; it is cached for offline use
				// and never consulted at read time.
				if pricing.Offline() {
					fmt.Fprintf(os.Stderr, "offline (%s is set); refusing to fetch\n", pricing.EnvOffline)
					os.Exit(1)
				}
				cachePath := pricing.CachePath()
				if !forceFetch {
					if c, err := pricing.LoadCache(cachePath); err == nil && c != nil &&
						!pricing.NeedsRefresh(c, pricing.DefaultCacheTTL, time.Now()) {
						fmt.Printf("Cache is fresh (%s, %d models); use --force to refetch.\n",
							c.FetchedAt, len(c.Models))
						return
					}
				}
				fmt.Printf("Fetching %s ...\n", firstNonEmpty(sourceURL, pricing.DefaultFetchURL))
				c, err := pricing.Fetch(sourceURL, pricing.DefaultFetchTimeout, time.Now())
				if err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					os.Exit(1)
				}
				if err := pricing.SaveCache(cachePath, c); err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Cached %d models from %s\n  %s\n", len(c.Models), c.Source, cachePath)
				fmt.Println("User overrides in the override file still win over the cache.")
			case "cache":
				c, err := pricing.LoadCache(pricing.CachePath())
				switch {
				case err != nil:
					fmt.Fprintf(os.Stderr, "%v\n", err)
					os.Exit(1)
				case c == nil || len(c.Models) == 0:
					fmt.Println("No cached price catalogue. Run: pricing fetch")
				default:
					state := "fresh"
					if pricing.NeedsRefresh(c, pricing.DefaultCacheTTL, time.Now()) {
						state = "stale"
					}
					fmt.Printf("Source:    %s\n", c.Source)
					fmt.Printf("Fetched:   %s (%s)\n", c.FetchedAt, state)
					fmt.Printf("Models:    %d\n", len(c.Models))
					fmt.Printf("Cache file: %s\n", pricing.CachePath())
					fmt.Println("Auto-refresh: " + onOff(config.Global().PricingAutoFetch))
				}
			case "schema":
				fmt.Printf("Schema URL: %s\n", pricing.SchemaURL)
				fmt.Printf("Override file: %s\n", pricingPath())
				fmt.Printf("Catalogue cache: %s\n", pricing.CachePath())
				// The schema may also be checked out next to the working
				// directory; its absence is not an error.
				if _, err := os.Stat("docs/pricing.schema.json"); err == nil {
					fmt.Println("Local schema: docs/pricing.schema.json")
				}
			default:
				fmt.Fprintf(os.Stderr, "unknown subcommand: %s (valid: list, overrides, set, remove, validate, schema, fetch, cache)\n", args[0])
				os.Exit(1)
			}
		},
	}
	c.Flags().Float64Var(&input, "input", 0, "USD per 1M input tokens")
	c.Flags().Float64Var(&output, "output", 0, "USD per 1M output tokens")
	c.Flags().Float64Var(&cacheRead, "cache-read", 0, "USD per 1M cache-read tokens")
	c.Flags().Float64Var(&cacheWrite, "cache-write", 0, "USD per 1M cache-write tokens")
	c.Flags().BoolVar(&freeFlag, "free", false, i18n.T("help.pricingFree"))
	c.Flags().StringVar(&filePath, "file", "", i18n.T("help.pricingFile"))
	c.Flags().StringVar(&sourceURL, "source", "", i18n.T("help.pricingSource"))
	c.Flags().BoolVar(&forceFetch, "force", false, i18n.T("help.pricingForce"))
	return c
}

// firstNonEmpty returns a when it is non-empty, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// onOff renders a boolean for human-facing status output.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
