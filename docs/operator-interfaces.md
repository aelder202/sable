# Operator Interfaces

## Web UI

Sessions live in the left sidebar. Anything quiet for 3–10 minutes goes yellow; past 10 minutes goes red. Hover for the exact last-seen timestamp. Press `/` to focus the filter.

The main console is split into Output and Task Builder. Output shows queued task echoes, task results, progress messages, errors, and saveable artifact rows. Use the output type filter to focus on shell output, operator events, artifacts, errors, or progress. Expand **Search Output** to filter rendered output rows only. Output rows can be pinned or copied without leaving the console. **Jump To Latest** resumes the live tail after scrolling up.

Use **Clear Output** to clear the selected session's output history on the server. Cleared output stays cleared after switching sessions or reloading the page. Use **Save Output** to snapshot the currently rendered output as a `.txt` artifact under **Session Details -> Artifacts**. Saved output, screenshots, downloads, PEAS, and snapshot results are stored as server-side artifacts so they remain available after a browser refresh.

The Task Builder groups actions by command, situational awareness, file handling, and session control. It keeps the command line on its own full-width row only for actions that need operator input, such as Shell, Download, Upload, and Sleep. One-click actions such as Processes, Screenshot, Snapshot, Persistence, PEAS, and Interactive hide the command line until input is actually needed. Download path autofill and the Download file browser both wait for the selected session to confirm the remote path browser is ready before their controls unlock. Drag the handle between Output and Task Builder to resize the console, or double-click it to reset the height.

Select sessions in the left sidebar to queue supported tasks across multiple sessions at once. Bulk queueing is available for Shell, Processes, Screenshot, Snapshot, Persistence, PEAS, and Sleep; file transfer, Interactive, and Kill remain single-session actions.

Session metadata and saved results open from **Session Details**. The default Timeline view combines queued jobs, running work, completed results, artifacts, and audit events for the selected session. Use the detail filter or tabs to focus on Jobs, Artifacts, Notes, or Audit.

When a task supports cancellation, the Task Builder shows a dedicated cancellation row above the action selector. PEAS runs as a background task and is currently the cancellable task type; use the visible **Cancel PEAS** control there instead of opening Session Details during execution.

### Console Keys

- **Enter** / **Send**: queue the task
- **Up / Down**: command history
- **Ctrl/Cmd + K**: focus the task input
- **Esc**: cancel an upload prompt or kill confirmation
- **Clear Output**: clear persisted output history for the selected session
- **Save Output**: save rendered output as a text artifact
- **Jump To Latest**: resume the live tail after scrolling up

## CLI

The server has to be running first. Open another terminal on the same host:

```sh
./sable-server --cli                  # Linux / macOS
.\sable-server.exe --cli              # Windows
```

For a non-default loopback port or an SSH-tunneled API, point at it explicitly:

```sh
./sable-server --cli --api https://127.0.0.1:9443
```

The CLI is queue-oriented and does not live-stream output or auto-decode downloads. Use the web UI or `GET /api/agents/:id/tasks` to review results.

| Command | Description |
|---------|-------------|
| `agents` | List sessions and last-seen times |
| `register <id> <secret-hex>` | Pre-register an agent |
| `use <agent-id>` | Select a session |
| `shell <command>` | Queue a shell command |
| `ps` | Queue a read-only process listing |
| `screenshot` | Queue one bounded screenshot |
| `persistence` | Queue a defensive persistence-location listing |
| `peas` | Run LinPEAS or winPEAS and return a text output artifact |
| `snapshot` | Queue a bounded host snapshot text artifact |
| `cancel <task-id>` | Cancel a running background task such as PEAS |
| `download <remote-path>` | Queue a file read |
| `upload <local-path> <remote-path>` | Read a local file, base64-encode, queue an upload |
| `sleep <seconds>` | Change the beacon interval |
| `kill` | Terminate the agent |
| `back` | Return to the main prompt |
| `help` | List all commands |
| `exit` / `quit` | Close the CLI |

## Adding More Agents

`sablectl install` creates the first identity in `config.env` with label `main`. Register it after the server is running:

```sh
./sablectl agent register main
```

If you didn't pass `--password-file` during install, add it explicitly: `./sablectl agent register main --password-file ./pw.txt`.

Every additional agent gets its own env file under `agents/<label>.env`:

```sh
./sablectl agent add linux --label web01
./sablectl agent build web01
./sablectl agent register web01
```

Labels must be lowercase with letters, digits, `-`, or `_`. `PC` and `VM` get rejected; use `pc` and `vm`.

To ship that agent:

```sh
scp builds/web01/agent-linux user@target:/tmp/agent
ssh user@target "chmod +x /tmp/agent && /tmp/agent &"
```

## Manual Registration

`sablectl agent register` is the easy path. The interactive server CLI works too:

```sh
./sable-server --cli
[sable]> register <agent-id-from-config.env> <secret-hex-from-config.env>
```

Or hit the REST API:

```sh
TOKEN=$(curl -sk -X POST https://127.0.0.1:8443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"password":"yourpassword"}' | jq -r .token)

curl -sk -X POST https://127.0.0.1:8443/api/agents \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"<agent-id>\",\"secret_hex\":\"<secret-hex>\"}"
```
