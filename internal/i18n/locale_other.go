//go:build !windows

package i18n

import (
	"os/exec"
	"runtime"
	"strings"
)

// osLocaleName returns the OS-level locale name on non-Windows platforms.
// On macOS it reads the user's AppleLocale preference; on Linux it runs
// `locale` as a last resort. Returns empty if detection fails.
func osLocaleName() string {
	switch runtime.GOOS {
	case "darwin":
		return macLocale()
	default:
		return linuxLocale()
	}
}

// macLocale reads the AppleLocale preference via `defaults read -g AppleLocale`.
// Returns values like "zh-CN", "en-US".
func macLocale() string {
	out, err := exec.Command("defaults", "read", "-g", "AppleLocale").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// linuxLocale runs `locale` and extracts the first relevant locale line.
// Falls back to empty if the command fails or no locale is set.
func linuxLocale() string {
	out, err := exec.Command("locale").Output()
	if err != nil {
		return ""
	}
	// `locale` output has lines like:
	//   LANG=en_US.UTF-8
	//   LC_CTYPE="en_US.UTF-8"
	//   LC_ALL=
	// We want the first non-empty LANG/LC_ALL/LC_MESSAGES value.
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"LC_ALL=", "LC_MESSAGES=", "LANG="} {
			if strings.HasPrefix(line, prefix) {
				val := strings.TrimPrefix(line, prefix)
				val = strings.Trim(val, `"`)
				if val != "" {
					return val
				}
			}
		}
	}
	return ""
}
