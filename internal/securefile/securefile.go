// Package securefile centralizes permissions for local Sable secret files.
package securefile

import (
	"fmt"
	"os"
	"path/filepath"
)

const ownerOnlyMode os.FileMode = 0600

// WriteFile writes to an already-restricted temporary file and atomically
// replaces path. Sensitive bytes are never exposed through a broadly inherited
// ACL while the write is in progress.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck
	closeWithError := func(cause error) error {
		if closeErr := tmp.Close(); cause == nil {
			return closeErr
		}
		return cause
	}

	if err := Restrict(tmpPath); err != nil {
		return closeWithError(fmt.Errorf("restrict temporary %s: %w", path, err))
	}
	if _, err := tmp.Write(data); err != nil {
		return closeWithError(err)
	}
	if err := tmp.Sync(); err != nil {
		return closeWithError(err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFilePlatform(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	if err := Restrict(path); err != nil {
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	return syncParentDirPlatform(dir)
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
