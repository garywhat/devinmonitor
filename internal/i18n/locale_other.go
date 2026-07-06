//go:build !windows

package i18n

// osLocaleName returns the OS-level locale name on non-Windows platforms.
// Unix systems typically set LANG/LC_* env vars which are already checked
// by detectLocale, so we return empty here (falls back to "en").
func osLocaleName() string {
	return ""
}
