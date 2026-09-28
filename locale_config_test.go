package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests observe the locale through the CLI's own help text, because that
// is what the reported defect was about: `config set locale zh` persisted
// cfg.Locale, but nothing read it, so the setting was accepted and ignored.
//
// Each case runs in a subprocess (see TestMain in datadir_flag_test.go) because
// config.Global() caches the loaded file in a process-global singleton and
// cannot be re-pointed mid-test.

const (
	enTagline = "Token & cost monitor for Devin CLI"
	zhTagline = "Devin CLI 的 Token 与成本监控"

	enCmdSessions = "Sessions"
	zhCmdSessions = "会话"
)

// writeConfigDir creates an isolated config directory. DEVINMONITOR_CONFIG_DIR
// keeps these tests away from the developer's real ~/.devinmonitor.
func writeConfigDir(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
	}
	return dir
}

// taglineFor runs `help` and reports which language the tagline came out in.
func taglineFor(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, code := runCLI(t, args...)
	if code != 0 {
		t.Fatalf("%v exited %d: %s", args, code, strings.TrimSpace(stderr))
	}
	switch {
	case strings.Contains(stdout, zhTagline):
		return "zh"
	case strings.Contains(stdout, enTagline):
		return "en"
	default:
		t.Fatalf("%v printed neither tagline; stdout was:\n%s", args, stdout)
		return ""
	}
}

// TestConfigLocaleIsApplied is the direct regression guard: a config file that
// sets locale=zh must produce Chinese output.
func TestConfigLocaleIsApplied(t *testing.T) {
	t.Setenv("DEVINMONITOR_CONFIG_DIR", writeConfigDir(t, `{"locale":"zh"}`))
	t.Setenv("DEVINMONITOR_LOCALE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "en_US.UTF-8")

	if got := taglineFor(t, "help"); got != "zh" {
		t.Errorf("help tagline locale = %q, want \"zh\" from the config file", got)
	}
}

// TestLocaleFlagOutranksConfigFile pins the top of the precedence chain.
func TestLocaleFlagOutranksConfigFile(t *testing.T) {
	t.Setenv("DEVINMONITOR_CONFIG_DIR", writeConfigDir(t, `{"locale":"zh"}`))
	t.Setenv("DEVINMONITOR_LOCALE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "en_US.UTF-8")

	if got := taglineFor(t, "--locale", "en", "help"); got != "en" {
		t.Errorf("`--locale en` over locale=zh gave %q, want \"en\"", got)
	}
}

// TestLocaleEnvOutranksConfigFile pins the second link of the chain.
func TestLocaleEnvOutranksConfigFile(t *testing.T) {
	t.Setenv("DEVINMONITOR_CONFIG_DIR", writeConfigDir(t, `{"locale":"zh"}`))
	t.Setenv("DEVINMONITOR_LOCALE", "en")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "en_US.UTF-8")

	if got := taglineFor(t, "help"); got != "en" {
		t.Errorf("DEVINMONITOR_LOCALE=en over locale=zh gave %q, want \"en\"", got)
	}
}

// TestSystemDetectionSurvivesWithoutConfigLocale is the guard against "fixing"
// the defect by letting config.Load()'s built-in Locale default of "en" outrank
// detection. If that happened, every user with a config file — and every user
// without one — would silently get English, and the whole LANG/LC_ALL branch of
// detectLocale would become dead code.
func TestSystemDetectionSurvivesWithoutConfigLocale(t *testing.T) {
	t.Setenv("DEVINMONITOR_CONFIG_DIR", writeConfigDir(t, `{"theme":"dark"}`))
	t.Setenv("DEVINMONITOR_LOCALE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "zh_CN.UTF-8")

	if got := taglineFor(t, "help"); got != "zh" {
		t.Errorf("config file without a locale key + LANG=zh gave %q, want \"zh\" "+
			"(the config default must not shadow system detection)", got)
	}
}

// TestSystemDetectionWithNoConfigFileAtAll covers the user who has never run a
// config command.
func TestSystemDetectionWithNoConfigFileAtAll(t *testing.T) {
	t.Setenv("DEVINMONITOR_CONFIG_DIR", writeConfigDir(t, ""))
	t.Setenv("DEVINMONITOR_LOCALE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "zh_CN.UTF-8")

	if got := taglineFor(t, "help"); got != "zh" {
		t.Errorf("no config file + LANG=zh gave %q, want \"zh\"", got)
	}
}

// TestConfigLocaleAppliesToSubcommandHelp checks that the locale is settled
// before the command tree is built. Every feature command's Short string is
// resolved through i18n.T() at registration time, so a hook placed too late
// would leave `sessions --help` in the previously detected language.
func TestConfigLocaleAppliesToSubcommandHelp(t *testing.T) {
	t.Setenv("DEVINMONITOR_CONFIG_DIR", writeConfigDir(t, `{"locale":"zh"}`))
	t.Setenv("DEVINMONITOR_LOCALE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "en_US.UTF-8")

	stdout, stderr, code := runCLI(t, "sessions", "--help")
	if code != 0 {
		t.Fatalf("sessions --help exited %d: %s", code, strings.TrimSpace(stderr))
	}
	// cmd.sessions is "Sessions" in en.toml and "会话" in zh.toml. This string is
	// resolved at command-registration time, so it only comes out translated if
	// the config locale was applied before the command tree was built.
	if !strings.Contains(stdout, zhCmdSessions) {
		t.Errorf("sessions --help did not pick up locale=zh (missing %q); stdout was:\n%s",
			zhCmdSessions, stdout)
	}
	if strings.Contains(stdout, enCmdSessions) {
		t.Errorf("sessions --help still shows the English Short %q; stdout was:\n%s",
			enCmdSessions, stdout)
	}
}
