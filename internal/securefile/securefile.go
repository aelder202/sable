// Package securefile centralizes permissions for local Sable secret files.
package securefile

import (
	"fmt"
	"os"
)

const ownerOnlyMode os.FileMode = 0600

// WriteFile writes data and then tightens platform-specific permissions.
func WriteFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, ownerOnlyMode); err != nil {
		return err
	}
	if err := Restrict(path); err != nil {
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	return nil
}

// Restrict tightens permissions on an existing sensitive file.
func Restrict(path string) error {
	if err := os.Chmod(path, ownerOnlyMode); err != nil {
		return err
	}
	return restrictPlatform(path)
}

// PermissionWarning returns a human-readable warning when path is readable or
// writable by principals beyond the local operator/admin set.
func PermissionWarning(path string) (string, error) {
	return permissionWarningPlatform(path)
}
