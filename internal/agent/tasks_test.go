package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletePathReturnsDirectoryMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sable"), 0700); err != nil {
		t.Fatalf("Mkdir sable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "sandbox"), 0700); err != nil {
		t.Fatalf("Mkdir sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	output, taskErr := completePath(filepath.Join(root, "sa"))
	if taskErr != "" {
		t.Fatalf("completePath error: %s", taskErr)
	}

	var result pathCompletionResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(result.Items) != 2 {
		t.Fatalf("expected 2 matches, got %#v", result.Items)
	}
	if !strings.HasSuffix(result.Items[0], string(filepath.Separator)) ||
		!strings.HasSuffix(result.Items[1], string(filepath.Separator)) {
		t.Fatalf("directory completions should include trailing separator: %#v", result.Items)
	}
	if result.Common != filepath.Join(root, "sa") {
		t.Fatalf("unexpected common prefix %q", result.Common)
	}
}

func TestCompletePathSingleDirectoryMatchAddsSeparator(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sable"), 0700); err != nil {
		t.Fatalf("Mkdir sable: %v", err)
	}

	output, taskErr := completePath(filepath.Join(root, "sab"))
	if taskErr != "" {
		t.Fatalf("completePath error: %s", taskErr)
	}

	var result pathCompletionResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	want := filepath.Join(root, "sable") + string(filepath.Separator)
	if result.Common != want || len(result.Items) != 1 || result.Items[0] != want {
		t.Fatalf("unexpected completion result: %#v, want %q", result, want)
	}
}
