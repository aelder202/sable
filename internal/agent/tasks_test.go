package agent

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

func TestListDirectorySupportsBoundedPages(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 7; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%02d.txt", i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	request, _ := json.Marshal(directoryRequest{Path: root, Offset: 0, Limit: 3})
	output, taskErr := listDirectory(string(request))
	if taskErr != "" {
		t.Fatal(taskErr)
	}
	var first fileBrowserResult
	if err := json.Unmarshal([]byte(output), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 3 || !first.More || first.Offset != 0 || first.Limit != 3 {
		t.Fatalf("unexpected first page: %+v", first)
	}
	request, _ = json.Marshal(directoryRequest{Path: root, Offset: 3, Limit: 3})
	output, taskErr = listDirectory(string(request))
	if taskErr != "" {
		t.Fatal(taskErr)
	}
	var second fileBrowserResult
	if err := json.Unmarshal([]byte(output), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 3 || !second.More || second.Offset != 3 || second.Entries[0].Name == first.Entries[0].Name {
		t.Fatalf("unexpected second page: %+v", second)
	}
}

func TestReadBoundedShellLineDrainsOversizedLine(t *testing.T) {
	input := strings.Repeat("x", 128*1024) + "\nnext\n"
	reader := bufio.NewReaderSize(strings.NewReader(input), 1024)
	line, truncated, err := readBoundedShellLine(reader, 64)
	if err != nil || !truncated || len(line) != 64 {
		t.Fatalf("unexpected bounded line: len=%d truncated=%v err=%v", len(line), truncated, err)
	}
	next, truncated, err := readBoundedShellLine(reader, 64)
	if err != nil || truncated || next != "next\n" {
		t.Fatalf("oversized line was not fully drained: %q truncated=%v err=%v", next, truncated, err)
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

func TestNormalizeWindowsShellCommandAddsCommonAliases(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "pwd", in: "pwd", want: "cd"},
		{name: "pwd spaced", in: "  pwd  ", want: "cd"},
		{name: "ls", in: "ls", want: "dir"},
		{name: "ls path", in: `ls C:\Windows`, want: `dir C:\Windows`},
		{name: "ls flag untouched", in: "ls -la", want: "ls -la"},
		{name: "other untouched", in: "whoami", want: "whoami"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeWindowsShellCommand(tt.in); got != tt.want {
				t.Fatalf("normalizeWindowsShellCommand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRunShellWindowsCommonAliases(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows cmd alias regression")
	}

	for _, command := range []string{"pwd", "ls"} {
		t.Run(command, func(t *testing.T) {
			output, taskErr := runShell(command)
			if taskErr != "" {
				t.Fatalf("runShell(%q) error: %s\noutput:\n%s", command, taskErr, output)
			}
			if strings.TrimSpace(output) == "" {
				t.Fatalf("runShell(%q) returned empty output", command)
			}
		})
	}
}

func TestShellCommandErrorRecognizesUnknownCommand(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		output   string
		fallback string
		want     string
	}{
		{
			name:     "windows cmd",
			cmd:      "definitelymissing",
			output:   "'definitelymissing' is not recognized as an internal or external command,\r\noperable program or batch file.\r\n",
			fallback: "exit status 1",
			want:     "command was not recognized by the OS: definitelymissing",
		},
		{
			name:     "posix sh",
			cmd:      "definitelymissing --flag",
			output:   "/bin/sh: 1: definitelymissing: not found\n",
			fallback: "exit status 127",
			want:     "command was not recognized by the OS: definitelymissing",
		},
		{
			name:     "posix interactive",
			cmd:      "definitelymissing",
			output:   "/bin/sh: definitelymissing: command not found\n",
			fallback: "",
			want:     "command was not recognized by the OS: definitelymissing",
		},
		{
			name:     "ordinary failure",
			cmd:      "false",
			output:   "",
			fallback: "exit status 1",
			want:     "exit status 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellCommandError(tt.cmd, tt.output, tt.fallback); got != tt.want {
				t.Fatalf("shellCommandError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunShellUnknownCommandReturnsRecognizedError(t *testing.T) {
	command := "sable-command-definitely-not-recognized-zzzz"
	output, taskErr := runShell(command)
	want := "command was not recognized by the OS: " + command
	if taskErr != want {
		t.Fatalf("runShell(%q) error = %q, want %q\noutput:\n%s", command, taskErr, want, output)
	}
	if strings.TrimSpace(output) == "" {
		t.Fatalf("runShell(%q) returned empty output", command)
	}
}

func TestDownloadFileAllowsCurrentLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.bin")
	data := []byte("download me")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	output, taskErr := downloadFile(path)
	if taskErr != "" {
		t.Fatalf("downloadFile error: %s", taskErr)
	}
	decoded, err := base64.StdEncoding.DecodeString(output)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	if string(decoded) != string(data) {
		t.Fatalf("downloaded data = %q, want %q", decoded, data)
	}
}

func TestDownloadFileRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := file.Truncate(maxDownloadBytes + 1); err != nil {
		file.Close() //nolint:errcheck
		t.Fatalf("Truncate: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if output, taskErr := downloadFile(path); taskErr == "" {
		t.Fatalf("expected oversized download error, got output length %d", len(output))
	}
}

func TestArchiveDirectoryReturnsZipWithNestedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evidence")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "two.txt"), []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}

	output, taskErr := archiveDirectory(root)
	if taskErr != "" {
		t.Fatalf("archiveDirectory error: %s", taskErr)
	}
	var result archiveArtifactResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.MIME != "application/zip" || result.Filename != "evidence.zip" || result.FileCount != 2 {
		t.Fatalf("unexpected archive result: %#v", result)
	}
	data, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, file := range reader.File {
		names[file.Name] = true
	}
	if !names["evidence/one.txt"] || !names["evidence/nested/two.txt"] {
		t.Fatalf("archive entries = %#v", names)
	}
}

func TestArchiveSelectionSupportsMultiplePaths(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "one.txt")
	second := filepath.Join(root, "two.txt")
	if err := os.WriteFile(first, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(archiveSelectionRequest{Paths: []string{first, second}, Base: root})
	output, taskErr := archiveDirectory(string(payload))
	if taskErr != "" {
		t.Fatalf("archiveDirectory selection error: %s", taskErr)
	}
	var result archiveArtifactResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.FileCount != 2 || !strings.HasSuffix(result.Filename, "-selection.zip") {
		t.Fatalf("unexpected selection result: %#v", result)
	}
}

func TestArchiveDirectoryHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, taskErr := archiveDirectoryWithProgress(ctx, root, nil); taskErr != "archive cancelled" || output != "" {
		t.Fatalf("cancelled archive = %q, %q", output, taskErr)
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

func TestLinuxScreenshotCandidatesIncludeCommonFallbacks(t *testing.T) {
	candidates := linuxScreenshotCandidates("/tmp/sable_screenshot_test")
	commands := make(map[string]bool)
	for _, candidate := range candidates {
		if len(candidate) == 0 {
			t.Fatal("empty screenshot candidate")
		}
		commands[candidate[0]] = true
	}

	for _, command := range []string{"gnome-screenshot", "scrot", "grim", "maim", "import"} {
		if !commands[command] {
			t.Fatalf("missing screenshot candidate %q in %#v", command, candidates)
		}
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
