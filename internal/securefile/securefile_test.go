package securefile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aelder202/sable/internal/securefile"
)

func TestWriteFileCreatesRestrictedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := securefile.WriteFile(path, []byte("secret")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "secret" {
		t.Fatalf("data = %q, want secret", data)
	}
	if warning, err := securefile.PermissionWarning(path); err != nil {
		t.Fatalf("PermissionWarning: %v", err)
	} else if warning != "" {
		t.Fatalf("unexpected permission warning: %s", warning)
	}
}

func TestPermissionWarningFlagsBroadUnixMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits do not model Windows ACLs")
	}
	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(path, []byte("secret"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	warning, err := securefile.PermissionWarning(path)
	if err != nil {
		t.Fatalf("PermissionWarning: %v", err)
	}
	if warning == "" {
		t.Fatal("expected warning for group/other-readable file")
	}
}
