//go:build !windows

package securefile

import (
	"fmt"
	"os"
)

func restrictPlatform(string) error {
	return nil
}

func replaceFilePlatform(from, to string) error {
	return os.Rename(from, to)
}

func syncParentDirPlatform(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func permissionWarningPlatform(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Sprintf("%s mode %03o allows group/other access; run chmod 600", path, info.Mode().Perm()), nil
	}
	return "", nil
}
