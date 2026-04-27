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

func TestListDirectoryReturnsStructuredEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatalf("Mkdir folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("hello"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	output, taskErr := listDirectory(root)
	if taskErr != "" {
		t.Fatalf("listDirectory error: %s", taskErr)
	}

	var result fileBrowserResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if result.Path == "" || result.Separator == "" {
		t.Fatalf("expected path metadata, got %#v", result)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %#v", result.Entries)
	}
	if !result.Entries[0].IsDir || result.Entries[0].Name != "folder" {
		t.Fatalf("directory should sort first, got %#v", result.Entries)
	}
	if result.Entries[1].IsDir || result.Entries[1].Name != "file.txt" || result.Entries[1].Size != 5 {
		t.Fatalf("unexpected file entry: %#v", result.Entries[1])
	}
}

func TestDetectImageType(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		mime string
		ext  string
	}{
		{name: "jpeg", data: []byte{0xff, 0xd8, 0xff, 0x00}, mime: "image/jpeg", ext: ".jpg"},
		{name: "png", data: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, mime: "image/png", ext: ".png"},
		{name: "unknown", data: []byte("not-image"), mime: "application/octet-stream", ext: ".bin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mime, ext := detectImageType(tt.data)
			if mime != tt.mime || ext != tt.ext {
				t.Fatalf("detectImageType() = %q, %q; want %q, %q", mime, ext, tt.mime, tt.ext)
			}
		})
	}
}

func TestCappedBufferRetainsWriteLength(t *testing.T) {
	var b cappedBuffer
	b.limit = 4
	n, err := b.Write([]byte("abcdef"))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != 6 {
		t.Fatalf("Write returned %d, want 6", n)
	}
	if got := b.buf.String(); got != "abcd" {
		t.Fatalf("buffer = %q, want %q", got, "abcd")
	}
	if !b.truncated {
		t.Fatal("expected truncated flag")
	}
}

func TestEncodeTextArtifact(t *testing.T) {
	output, taskErr := encodeTextArtifact("peas.txt", "hello")
	if taskErr != "" {
		t.Fatalf("encodeTextArtifact error: %s", taskErr)
	}

	var result fileArtifactResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if result.Filename != "peas.txt" || result.MIME != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected artifact metadata: %#v", result)
	}
	if result.Data != "aGVsbG8=" {
		t.Fatalf("unexpected artifact data: %q", result.Data)
	}
}
