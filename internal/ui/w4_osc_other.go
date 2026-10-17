//go:build !linux && !darwin

package ui

// queryBackground is not attempted here (Windows consoles have no non-blocking
// stdin read to bound the wait); COLORFGBG and BLAZE_THEME decide instead.
func queryBackground() (light, ok bool) { return false, false }
