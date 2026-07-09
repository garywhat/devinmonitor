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
	"github.com/garywhat/devinmonitor/internal/ui"
)

// ---- Config Command (#78) ----

var cmdConfig = func() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: i18n.T("cmd.config"),
		// Default action when no subcommand: show config.
		Run: func(cmd *cobra.Command, args []string) {
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
			cfg.RefreshInterval = 3
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
				_ = config.SaveGlobal()
				fmt.Printf("Timezone set to: %s\n", args[1])
			case "auto":
				tz := detectTimezone()
				cfg.Timezone = tz
				_ = config.SaveGlobal()
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
				_ = config.SaveGlobal()
				fmt.Printf("Model alias added: %s -> %s\n", args[1], args[2])
			case "remove":
				if len(args) < 2 {
					fmt.Fprintln(os.Stderr, "usage: config model-alias remove <alias>")
					os.Exit(1)
				}
				delete(cfg.ModelAliases, args[1])
				_ = config.SaveGlobal()
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
	c := &cobra.Command{
		Use:   "pricing [list|set <model>|remove <model>]",
		Short: i18n.T("cmd.pricing"),
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			if len(args) == 0 || args[0] == "list" {
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

				// Show custom overrides.
				if len(cfg.CustomPricing) > 0 {
					fmt.Println("\nCustom overrides:")
					t2 := ui.NewTable("Model", "Input/M", "Output/M", "Cache R/M", "Cache W/M")
					for m, p := range cfg.CustomPricing {
						t2.Row(m,
							fmt.Sprintf("$%.2f", p.InputPerM),
							fmt.Sprintf("$%.2f", p.OutputPerM),
							fmt.Sprintf("$%.2f", p.CacheReadPerM),
							fmt.Sprintf("$%.2f", p.CacheWritePerM))
					}
					fmt.Println(t2.String())
				}
				return
			}
			switch args[0] {
			case "set":
				if len(args) < 2 {
					fmt.Fprintln(os.Stderr, "usage: pricing set <model> --input <usd> --output <usd> [--cache-read <usd>]")
					os.Exit(1)
				}
				m := args[1]
				if cfg.CustomPricing == nil {
					cfg.CustomPricing = map[string]config.CustomPricing{}
				}
				cfg.CustomPricing[m] = config.CustomPricing{
					InputPerM:      input,
					OutputPerM:     output,
					CacheReadPerM:  cacheRead,
					CacheWritePerM: cacheWrite,
				}
				_ = config.SaveGlobal()
				fmt.Printf("Custom pricing set for %s\n", m)
			case "remove":
				if len(args) < 2 {
					fmt.Fprintln(os.Stderr, "usage: pricing remove <model>")
					os.Exit(1)
				}
				delete(cfg.CustomPricing, args[1])
				_ = config.SaveGlobal()
				fmt.Printf("Custom pricing removed for %s\n", args[1])
			default:
				fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
				os.Exit(1)
			}
		},
	}
	c.Flags().Float64Var(&input, "input", 0, "USD per 1M input tokens")
	c.Flags().Float64Var(&output, "output", 0, "USD per 1M output tokens")
	c.Flags().Float64Var(&cacheRead, "cache-read", 0, "USD per 1M cache-read tokens")
	c.Flags().Float64Var(&cacheWrite, "cache-write", 0, "USD per 1M cache-write tokens")
	return c
}

// lookupPricingWithCustom returns pricing, merging custom overrides with builtin.
func lookupPricingWithCustom(cfg *config.Config, modelName string) model.Pricing {
	// Check model aliases first.
	if cfg != nil && cfg.ModelAliases != nil {
		if canonical, ok := cfg.ModelAliases[modelName]; ok {
			modelName = canonical
		}
	}
	p := model.LookupPricing(modelName)
	// Apply custom pricing override.
	if cfg != nil && cfg.CustomPricing != nil {
		if cp, ok := cfg.CustomPricing[modelName]; ok {
			p.InputPerM = cp.InputPerM
			p.OutputPerM = cp.OutputPerM
			p.CacheReadPerM = cp.CacheReadPerM
			p.CacheWritePerM = cp.CacheWritePerM
			p.Free = false
		}
	}
	return p
}
