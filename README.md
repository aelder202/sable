<h1 align="center">Sable</h1>

<p align="center">
  Open source C2
</p>

<p align="center">
  Go | HTTPS + DNS transports | Web UI + CLI
</p>

Sable is a C2 written in Go. The server takes encrypted beacons from agents over HTTPS, with DNS as a fallback, and exposes a browser console and an interactive CLI for tasking.

---

## Interface Preview

Password-gated operator console on loopback HTTPS:

![Sable login screen](images/login.jpg)

Fleet Overview with sleep-aware status, command outcomes, recent activity, and
actionable failure alerts:

![Sable fleet Overview with warning totals and Needs Attention guidance](images/overview.jpg)

Shell commands that reach the agent but return an OS or process error are shown
as **Warnings**. **Failed** is reserved for delivery or agent communication
failures. A failed-task banner remains until every item under **Needs
Attention** is cleared with **Ignore** (for the current session) or
**Acknowledge** (persistently); either choice retains the task output and
outcome history.

Agent workspace with Success, Warning, and Failed output cards, the Task
Builder, and the full action menu:

![Sable web console with active session and warning output](images/landing_page.jpg)

Bulk tasking across selected agents:

![Sable bulk tasking across Linux and Windows sessions](images/bulk_tasking.jpg)

Agent details rail with outcome totals, jobs, artifacts, metadata, and audit
history:

![Session Details with success, warning, and failed totals](images/session_details.jpg)

Remote file browser for navigating paths and downloading files or directory ZIPs:

![Download file browser modal](images/file_browser.jpg)

---

## Authorized Use

Sable is intended for educational use, controlled labs, CTFs, owned systems, and engagements where you hold written authorization. Do not deploy it against systems you do not own or do not have explicit permission to test. The author accepts no responsibility for misuse.

---

## Architecture

```mermaid
flowchart LR
    A["Agent<br/><sub>statically compiled<br/>ldflags-configured</sub>"]
    O["Operator<br/><sub>Web UI / CLI</sub>"]
    S["Sable Server<br/><sub>:443 HTTPS · :53/udp DNS<br/>127.0.0.1:8443 API + Web UI</sub>"]

    A -->|HTTPS beacons :443| S
    A -.->|DNS fallback :53/udp| S
    O -->|loopback :8443| S
```

See [docs/architecture.md](docs/architecture.md) for the crypto details, network ports, and project layout.

---

## Prerequisites

- Go 1.26.5 or later (matches `go.mod` and includes the current security fixes)
- `make` (Linux, macOS, or Windows; PowerShell or cmd)
- Permission to bind `443` (and `53/udp` when DNS fallback is on). Prefer a dedicated unprivileged account with only the bind capability, or use high ports and an OS-level redirect.

Agents cross-compile through `GOOS`/`GOARCH`, so you can build from any host OS.

---

## Quick Start

### 1. Clone

```sh
git clone https://github.com/aelder202/sable
cd sable
```

### Guided Setup (Recommended)

From a fresh clone, run one command:

```sh
go run ./cmd/sablectl setup
```

The guide asks for the agent callback URL, target platforms, labels, beacon
profile, credential location, state encryption, and whether to start now. It
then creates the local configuration and TLS certificate, builds `sablectl`,
the server, and selected agents, starts the server, registers local identities,
and runs the health checks. The final summary includes each agent artifact's
SHA-256 checksum and authorized Linux or Windows deployment command templates.

Setup checks the operator API before asking any configuration questions. If a
server is already running, guided setup first warns that a clean setup will
stop it and permanently remove the current configuration, identities, state,
artifacts, keys, credentials, logs, and builds. The default answer is **No**.
Nothing is erased until the replacement is accepted and the final setup plan
is confirmed. For unattended replacement, add the explicit `--replace` flag:

```sh
go run ./cmd/sablectl setup --yes --replace
```

For an unattended local lab setup using the secure defaults:

```sh
go run ./cmd/sablectl setup --yes
```

For an unattended setup with an externally reachable agent listener and both
agent platforms:

```sh
go run ./cmd/sablectl setup --yes \
  --url https://<your-server-ip>:443 \
  --agents both \
  --windows-label win01
```

After setup, use the generated control binary:

```sh
./sablectl status
./sablectl down
./sablectl up
```

On Windows, use `.\sablectl.exe` in place of `./sablectl`. Setup stores the
generated operator password at `.sable/operator-password`, the state encryption
key at `.sable/state.key`, and server logs at `.sable/server.log` by default.

The manual steps below remain available for custom or development installs.

Modules pull on the first build. Run `go mod download` if you want to pre-warm the cache.

### Manual Install

Build the unified helper, then let it create the local config, TLS certificate, server binary, selected agent binaries, and `.sable/install.json` manifest.

```sh
make sablectl
./sablectl install --url https://<your-server-ip>:443 --password-file ./pw.txt
```

`--password-file` is optional but recommended: when supplied, `install` creates the file (with a random password if it doesn't already exist) and records its path in `.sable/install.json`. `sablectl start` and `sablectl agent register` reuse that path automatically, so you don't need to retype `--password-file` on every command.

To build both Linux and Windows agents with separate identities:

```sh
./sablectl install --url https://<your-server-ip>:443 --password-file ./pw.txt --agents both --windows-label win01
```

`SERVER_URL` is the address agents beacon to, not the operator UI. `sablectl install` writes `config.env`, `server.crt`, `server.key`, `.sable/install.json`, and builds artifacts under `builds/<label>/`. These files are gitignored and include secrets.

### 3. Start

Keep the server binary, `server.crt`, and `server.key` in the same directory. If you ran `install --password-file ./pw.txt`, just run:

```sh
./sablectl start             # Linux / macOS
.\sablectl.exe start         # Windows
```

`start` reads the password file path from `.sable/install.json`. To override it for one run, or if you skipped the flag during install, point at a file directly:

**Linux / macOS**

```sh
printf '%s' 'yourpassword' > ./pw.txt
chmod 600 ./pw.txt
./sablectl start --password-file ./pw.txt
```

**Windows (PowerShell)**

```powershell
Set-Content -Encoding ascii -NoNewline .\pw.txt "yourpassword"
.\sablectl.exe start --password-file .\pw.txt
```

`SABLE_OPERATOR_PASSWORD` and stdin both work too.

By default the server persists operator metadata to `sable-state.json`, stores large artifact bodies under `sable-state.json.artifacts/`, and encrypts both with AES-256-GCM using `.sable/state.key`. Registered agents, reliable queued/in-flight tasks, output history, notes, tags, artifacts, and audit events survive a restart. Sable creates the key when missing and writes sensitive files through restricted temporary files and atomic replacement. Back up the key separately: encrypted state cannot be recovered without it. Move state with `--state-file <path>` or `SABLE_STATE_FILE=<path>`, disable persistence with `--state-file none`, or explicitly opt out of encryption with `--state-key-file none`.

The server prints its TLS fingerprint and listener status:

```text
[*] TLS cert fingerprint (SHA-256): 3a1f...b9c4
[*] Operator API on https://127.0.0.1:8443 | Agent listener on :443
```

The fingerprint is already baked into the agent binary because setup runs before compile.

The operator API binds to loopback only. Reach it on the server host directly, or tunnel:

```sh
ssh -L 8443:127.0.0.1:8443 user@sable-host
```

### 4. Register The Main Agent

The first agent identity is `main`. `sablectl install` builds it at `builds/main/agent-linux`, but it can only be registered after the server API is running.

In a second terminal on the server host, run:

```sh
./sablectl agent register main
```

If you skipped `--password-file` during install, pass it here (the flag may go before or after the label):

```sh
./sablectl agent register main --password-file ./pw.txt
./sablectl agent register --password-file ./pw.txt main   # same thing
```

`register` with no label registers every locally known identity. To start the server and register generated identities in one pass, run install with `--start`:

```sh
./sablectl install --url https://<your-server-ip>:443 --password-file ./pw.txt --start
```

### 5. Add Or Rebuild Agents

Create another local identity, then build it:

```sh
./sablectl agent create windows --label win01
```

`agent create` creates the identity, builds its artifact, and registers it when
the local server is running. If the server is offline, registration is deferred
until the next `sablectl up`.

After source changes, rebuild without remembering which target changed:

```sh
./sablectl rebuild
```

### 6. Deploy The Agent

Linux:

```sh
scp builds/main/agent-linux user@target:/tmp/agent
ssh user@target "chmod +x /tmp/agent && /tmp/agent &"
```

Windows:

```powershell
make build-agent-windows
Copy-Item .\builds\main\agent.exe C:\Temp\agent.exe
Start-Process -FilePath C:\Temp\agent.exe -WindowStyle Hidden
```

The agent shows up in the console within one beacon interval.

### 7. Open The Console

`https://127.0.0.1:8443` on the server host (or through the tunnel). Accept the self-signed cert and log in with the operator password.

After login the Overview dashboard summarizes the deployed fleet with
sleep-aware status, activity, and Success, Warning, and Failed outcome counters.
A shell command that reaches an agent but is rejected by the OS or exits with an
error is a Warning. A task becomes Failed when delivery or agent communication
breaks down, including when an agent misses its sleep-aware offline check-in
threshold.

Failed tasks appear under **Needs Attention** and keep the red Overview banner
active. Open each failed task there and choose **Ignore** to clear it for the
current browser session or **Acknowledge** to clear it persistently. Both
actions retain the output and outcome history. Open **Agents** for task output,
the Task Builder, metadata, artifacts, and Remote Files.

---

## Documentation

- [Architecture](docs/architecture.md) — diagram, crypto, network ports, project layout
- [Operator Interfaces](docs/operator-interfaces.md) — Web UI, CLI, adding agents, manual registration
- [Task Reference](docs/tasks.md) — Task Builder Actions, Task Notes, full task table
- [REST API](docs/api.md) — endpoint summary, with the full contract in [`docs/openapi.yaml`](docs/openapi.yaml) (OpenAPI 3.0.3)
- [DNS Fallback](docs/dns-fallback.md) — running with the optional DNS transport
- [Development](docs/development.md) — reinstall, rebuild, build targets, tests, configuration, profiles, password sources
- [Security Model](docs/security.md) — design notes for review and defenders
- [Troubleshooting](docs/troubleshooting.md) — common errors and what to check

---

## License

GPL-3.0. See [LICENSE](LICENSE).
