//go:build windows

package i18n

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// osLocaleName returns the user's default locale name (e.g. "zh-CN", "en-US")
// via the Windows GetUserDefaultLocaleName API.
func osLocaleName() string {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := kernel32.NewProc("GetUserDefaultLocaleName")
	buf := make([]uint16, 85) // LOCALE_NAME_MAX_LENGTH
	r, _, _ := proc.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}
