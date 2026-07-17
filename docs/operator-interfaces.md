# Operator Interfaces

## Web UI

The web UI opens on **Overview**, with a plain-language fleet health summary, fleet counters, a searchable agent inventory, actionable attention items, scrollable recent fleet activity, compact platform/transport composition, and task-outcome charts for the last 24 hours or 7 days. Failed-task attention items can be opened, ignored for the current browser session, or acknowledged from Overview. Acknowledgement uses an optional persistent browser warning and retains the agent's task output, Recent Activity entry, and outcome-chart history. Registration does not prove deployment: **Never seen** identifies an agent that has not beaconed. After first contact, **Active**, **Overdue**, and **Offline** are calculated from the agent's reported sleep interval and jitter allowance. Retired agents remain available when explicitly included.

Open **Agents** for the task workspace. The agent rail can be collapsed to give Output more room. Operator display names are independent of immutable IDs and beacon-reported hostnames and can be changed with **More -> Edit info** on an agent card. Press `/` to focus the agent filter.

The main console is split into Output and Task Builder. Output shows queued task echoes, task results, progress messages, errors, and saveable artifact rows. Use the output type filter to focus on shell output, operator events, artifacts, errors, or progress. Expand **Search Output** to filter rendered output rows only. Output rows can be pinned or copied without leaving the console. **Jump To Latest** resumes the live tail after scrolling up.

Use **Clear Output** to clear the selected agent's output history on the server. Cleared output stays cleared after switching agents or reloading the page. Use **Save Output** to snapshot the currently rendered output as a `.txt` artifact under **Agent Details -> Artifacts**. Saved output, screenshots, downloads, PEAS, and snapshot results are stored as server-side artifacts so they remain available after a browser refresh.

The Task Builder groups actions by command, situational awareness, file handling, and agent control. It keeps the command line on its own full-width row only for actions that need operator input, such as Shell, Download, Upload, and Sleep. One-click actions such as Processes, Screenshot, Snapshot, Persistence, PEAS, and Interactive hide the command line until input is actually needed. Download path autofill and Remote Files start only for the selected agent and do not block navigation or actions elsewhere. Drag the handle between Output and Task Builder to resize the console, or double-click it to reset the height.

Use the Task Builder's **Current agent** / **Selected agents** target toggle to choose single- or multi-agent execution before running a command. Selecting the multi-agent target enables sidebar selection and changes the primary action to show exactly how many agents will receive the task. Bulk queueing is available for Shell, Processes, Screenshot, Snapshot, Persistence, PEAS, and Sleep; file transfer, Interactive, and Kill remain single-agent actions. Retired agents retain their evidence but cannot be tasked until restored.

The header's **active jobs** control opens a fleet-wide modal. A job is active only after the host has received it and while the server is waiting for its terminal result; tasks still waiting for first delivery remain queued and are not counted as active.

Agent identity, connection details, source IP, activity, jobs, artifacts, files, and audit history open in the closeable **Agent Details** drawer. Display name, tags, and notes are edited from the agent card's **More -> Edit info** action. The Artifacts panel supports deletion and a 1–256 item retention limit. Retirement hides an inactive identity while preserving history and artifacts; revocation permanently deletes retained state.

Remote Files keeps per-agent navigation history, cache, scroll position, sorting, filtering, and selection. Files save directly; directories and multi-selections become one ZIP with visible progress, cancellation, retry, and retained-artifact saving. Use **Load More** for bounded directory pages.

When a task supports cancellation, the Task Builder shows a dedicated cancellation row above the action selector. PEAS, file downloads, and directory archives can be cancelled there; transfers can also be cancelled from Remote Files.

### Console Keys

- **Enter** / **Send**: queue the task
- **Up / Down**: command history
- **Ctrl/Cmd + K**: focus the task input
- **Esc**: cancel an upload prompt or kill confirmation
- **Clear Output**: clear persisted output history for the selected agent
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
./sablectl agent create linux --label web01
```

`agent create` creates the identity, builds it, and registers it when the local
server is reachable. An offline build remains valid and is registered on the
next `sablectl up`.

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
