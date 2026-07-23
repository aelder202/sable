//go:build windows

package main

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

func waitForProcessExit(pid int, timeout time.Duration) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(handle)

	waitMillis := uint32(timeout / time.Millisecond)
	status, err := windows.WaitForSingleObject(handle, waitMillis)
	if err != nil {
		return err
	}
	switch status {
	case windows.WAIT_OBJECT_0:
		return nil
	case uint32(windows.WAIT_TIMEOUT):
		return fmt.Errorf("process is still running after %s", timeout)
	default:
		return fmt.Errorf("unexpected process wait status %d", status)
	}
}
