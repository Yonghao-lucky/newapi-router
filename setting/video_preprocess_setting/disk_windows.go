//go:build windows

package video_preprocess_setting

// diskSpaceForPath is unavailable on Windows. The settings that depend on it
// degrade to "unknown": the temp-storage warning is skipped and the runtime
// card shows zeroes rather than a wrong number.
func diskSpaceForPath(string) (total uint64, free uint64, ok bool) {
	return 0, 0, false
}
