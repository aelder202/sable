# Security Model

- Operator API binds to `127.0.0.1:8443` only. Off-host access goes through SSH.
- Agent secrets are excluded from API responses (`json:"-"`).
- Agents pin the server cert by SHA-256 fingerprint. A trusted-CA cert substitution does not help an attacker because the fingerprint check fails first.
- Operator password is hashed with Argon2id (t=3, m=64 MB, p=4). No plaintext storage.
- Nonce replay protection is an atomic check-and-record; no TOCTOU window between concurrent beacons.
- Per-source-IP rate limiting on both transports: 200 HTTPS / 1024 DNS requests per 10s window. Limiter maps are capped and swept periodically. DNS authenticates every chunk before allocating state and caps concurrent reassembly to eight sessions per source and 256 globally.
- Agent IDs are restricted to alphanumeric + hyphen at registration. No path traversal, no injection through the ID field.
- Task queues are capped at 64 entries per agent. Results must correlate to a delivered or tracked background task; incomplete chunk assemblies, aggregate assembly bytes, output count, and retained output bytes are all bounded.
- SSE connections may remain idle indefinitely, but every event and keepalive write receives a fresh five-second deadline so a slow or abandoned client cannot pin a writer goroutine.
- `config.env`, `sable-state.json`, `server.crt`, `server.key`, `agents/*.env`, password files, and built agent binaries are sensitive. Do not commit them.
- Persisted state is encrypted by default with AES-256-GCM through `.sable/state.key`. The key is generated when missing and protects both metadata and artifact blobs; losing it makes state unrecoverable. `--state-key-file none` is the explicit plaintext opt-out.
- Operator CLI and `sablectl` clients require a loopback HTTPS origin and pin the exact leaf certificate from `server.crt`; they reject redirects and never accept an arbitrary self-signed listener. Agent listener URLs remain pathless HTTPS origins and use their build-time certificate fingerprint.
- `sablectl down` uses a JWT-authenticated shutdown endpoint on the loopback-only operator API; it enters the server's normal graceful state-flush path.
- The server logs certificate expiry at startup and warns inside 30 days; `sablectl doctor` reports the same condition.
- Sensitive bytes are written only after a temporary file has owner-only permissions/protected Windows ACLs; the restricted file is synced and atomically replaces its destination. Agent-producing `sablectl` and Makefile build paths also harden built agent binaries after compilation.
- The optional pprof listener is loopback-only TLS, uses the pinned server certificate, requires the operator password as a bearer token, and applies bounded headers and I/O deadlines.
- PEAS helpers are never fetched by an agent at runtime. The build-time updater uses fixed release URLs and verifies pinned SHA-256 digests before embedding an asset.
- CI runs on main and release branches with pinned Actions/tool versions, Windows tests, cross-builds, race/static/vulnerability/secret scans, OpenAPI validation, and Dependabot updates.
- Run `sablectl doctor` after install or when moving files. It warns if sensitive local files inherit broad permissions or grant access to unexpected principals. Run `sablectl doctor --fix-permissions` to harden existing generated files in place.
- Manual Unix-like remediation: `chmod 600 config.env sable-state.json server.key pw.txt` and `chmod 700 agents`.
- Manual Windows remediation when needed:
  ```powershell
  icacls config.env sable-state.json pw.txt server.key /inheritance:r
  icacls config.env sable-state.json pw.txt server.key /grant:r "$env:USERNAME:F" "*S-1-5-18:F" "*S-1-5-32-544:F"
  ```
- Agents are stripped (`-s -w`), but ldflags string literals (server URL, agent ID, cert fingerprint) remain readable in `.rodata` via `strings`. Treat built agents as sensitive artifacts.
