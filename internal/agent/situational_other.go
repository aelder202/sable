//go:build !windows

package agent

import "fmt"

// captureScreenshotWindows is a compilation stub; the real implementation
// lives in situational_windows.go and is never called on non-Windows.
func captureScreenshotWindows() ([]byte, error) {
	return nil, fmt.Errorf("windows screenshot not available on this platform")
}
