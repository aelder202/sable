# Task Reference

## Task Builder Actions

| Action | Input | Result |
|--------|-------|--------|
| **Shell** | Command string | Runs one bounded shell command (`/bin/sh -c` or `cmd /C`). Output cap 512 KB, timeout 60 seconds. Use Interactive for shell state that must persist across commands. |
| **Processes** | None | Returns a read-only process listing. |
| **Screenshot** | None | Captures one operator-initiated screenshot. The agent downscales/compresses it and returns a saveable artifact row. |
| **Snapshot** | None | Collects a bounded host snapshot report covering identity, network, route, disk, and environment basics. Returns a text artifact. |
| **Persistence** | None | Lists common autorun and persistence locations for defensive review. It does not modify the host. |
| **PEAS** | None | Runs the matching PEASS-ng helper for the session OS. Progress appears in Output; the final result is a saveable text artifact. |
| **Download** | Remote path | Reads a remote file up to 50 MB. Path suggestions and **Browse** help select a path; large results are chunked and reassembled server-side before the save action appears. |
| **Upload** | Local file and remote path | Sends a local file up to 50 MB to the selected session. Drag a file onto Output or use **Choose File**. Keep large uploads on HTTPS transport. |
| **Sleep** | Seconds, `1`-`86400` | Changes the selected session's beacon interval. |
| **Interactive** | None | Opens a persistent `/bin/sh` or `cmd.exe` session. The agent uses a 100 ms beacon interval while interactive mode is active. Use `exit`, `quit`, or **Exit** to return to normal tasking. |

## Task Notes

### Shell

Type `shell <command>` (or just type, with the `Shell` task type selected) and queue it.

Output is captured to 512 KB; the command runs under a 60-second deadline. For state that persists across commands (`cd`, environment variables, `source`), use interactive mode.

### Situational Awareness

Use **PS** to request a read-only process listing. Use **Persistence** to list common autorun locations such as Run keys, startup folders, scheduled tasks, systemd units, cron locations, and LaunchAgent folders depending on the agent OS.

Use **Screenshot** to take a single operator-initiated screenshot. The agent downscales and compresses the image, then sends it through bounded result chunks; it is not a continuous capture stream.

Use **PEAS** to run the matching PEASS-ng helper for the selected session OS: LinPEAS on Linux/macOS and winPEAS on Windows. Agent builds can embed cached PEAS scripts for offline targets; otherwise the agent downloads the matching helper at run time. Progress entries are posted while it prepares and runs, and the final output is captured as a text artifact and returned as a saveable result.

For offline PEAS support, run `sablectl rebuild --offline-peas` (which refreshes the PEAS cache before rebuilding agents), or run `make update-peas` once and then build agents normally with `sablectl rebuild`, `sablectl agent build <label>`, or the `make build-agent-*` targets. The updater caches the latest PEASS-ng scripts under `internal/agent/peas/`; those local copies are embedded into subsequently built agent binaries and are ignored by git.

Use **Snapshot** for a bounded text report covering identity, network, route, disk, and environment basics.

### Interactive Shell

Select **Interactive** in the composer to bring up `/bin/sh` (Linux) or `cmd.exe` (Windows), bound to the agent for the life of the session.

The agent flips to a 100 ms beacon interval while interactive mode is on. Output streams over SSE as fast as each beacon round-trips. The console border turns green and the prompt picks up the agent hostname. Input stays locked with `waiting for agent...` until the agent confirms it is in fast-beacon mode.

`exit`, `quit`, or the **Exit** button drops back to normal mode.

The shell runs over pipes, not a PTY. Anything that needs a real TTY (`vim`, `top`, `sudo` with a password prompt) will misbehave. A command that runs silently for 60+ seconds (`sleep 9999`) trips the timeout and respawns the shell.

### Download

`download <path>`. The composer prepares a remote path browser as soon as the session is online and keeps it ready while the agent stays online. Use **Browse** in the Download task to open the modal file explorer with parent navigation, refresh, and file download actions.

You may also choose to instead type a partial path on the command line for live suggestions: click a directory to keep browsing, the `...` row to go up, or a file to fill the final path.

The agent reads files up to 50 MB, base64-encodes them, and the browser auto-decodes and saves them. Large results are split into bounded chunks and reassembled server-side before the web UI offers the save action. Use HTTPS transport for large downloads.

### Upload

Click **Upload** or drag a file onto the output area. Enter the remote destination path when prompted.

Uploads cap at 50 MB. Large uploads are still delivered as a single HTTPS task payload, so keep upload tasks on HTTPS rather than DNS transport.

### Kill safety

Queueing `kill` requires a second confirmation click. There is no undo.

## Task Reference Table

| Command | Syntax | Notes |
|---------|--------|-------|
| `shell` | `shell <command>` | One-shot in normal mode (`/bin/sh -c` or `cmd /C`). In interactive mode, writes to the persistent shell. 512 KB output cap, 60s timeout. |
| `ps` | `ps` | Read-only process listing. Output cap 48 KB. |
| `screenshot` | `screenshot` | One operator-initiated bounded screenshot. Returns a chunked image result, not a stream. |
| `persistence` | `persistence` | Read-only listing of common persistence locations for the agent OS. Output cap 48 KB. |
| `peas` | `peas` | Runs embedded LinPEAS/winPEAS when available, otherwise downloads the matching helper. Returns output as a text artifact. |
| `snapshot` | `snapshot` | Collects a bounded host snapshot report and returns it as a text artifact. |
| `ls` | Internal File Browser task | Read-only structured directory listing used by the Download task's Browse button. |
| `cancel` | `cancel <task-id>` | Cancels a running background task when supported. |
| `interactive` | Web UI / API | Open or close a persistent shell on the agent. |
| `download` | `download <remote-path>` | Read a file off the agent. 50 MB cap; results are chunked and reassembled server-side. |
| `upload` | `upload <local> <remote>` (CLI) or **Upload** (Web UI) | Push a file to the agent. 50 MB cap; use HTTPS transport for large files. |
| `pathbrowse` | Web UI Download field | Internal: primes fast beaconing for the path browser. |
| `complete` | Web UI Download field | Internal: lists matching paths under the typed prefix. Extends the fast path-browse window. |
| `sleep` | `sleep <seconds>` | Change beacon interval. Range 1–86400. |
| `kill` | `kill` | Terminate the agent process. Web UI confirms twice. |
