# Security Model

- Operator API binds to `127.0.0.1:8443` only. Off-host access goes through SSH.
- Agent secrets are excluded from API responses (`json:"-"`).
- Agents pin the server cert by SHA-256 fingerprint. A trusted-CA cert substitution does not help an attacker because the fingerprint check fails first.
- Operator password is hashed with Argon2id (t=3, m=64 MB, p=4). No plaintext storage.
- Nonce replay protection is an atomic check-and-record; no TOCTOU window between concurrent beacons.
- Per-source-IP rate limiting on both transports: 200 HTTPS / 1024 DNS requests per 10s window. DNS beacons span many label-sized queries, so their request budget is higher while concurrent sessions, total chunks, and assembled bytes remain bounded.
- Agent IDs are restricted to alphanumeric + hyphen at registration. No path traversal, no injection through the ID field.
- Task queues capped at 64 entries per agent; output history capped at 256.
- The SSE stream endpoint disables its write deadline for long-lived connections; a 15s keepalive comment keeps proxies from timing the stream out. Other endpoints enforce the 10s write deadline.
- `config.env`, `sable-state.json`, `server.crt`, `server.key`, `agents/*.env`, password files, and built agent binaries are sensitive. Do not commit them.
- Sable is designed for local deployment. Persisted state is plaintext JSON by default so local recovery and troubleshooting remain simple. Treat `sable-state.json` and its `.artifacts` directory as highly sensitive: they contain agent pre-shared secrets, queued tasks, command output, artifact metadata/data, notes, tags, and audit history.
- Optional at-rest encryption is enabled with `--state-key-file <path>` or `SABLE_STATE_KEY_FILE`. The key must be 32 raw bytes or 64 hexadecimal characters. AES-256-GCM protects both the metadata snapshot and artifact blobs; losing the key makes state unrecoverable. `sablectl install --state-key-file .sable/state.key` creates, restricts, records, and reuses a key file.
- `sablectl` only disables TLS chain verification for loopback operator API origins. Agent listener URLs must be pathless HTTPS origins and continue to use certificate fingerprint pinning.
- The server logs certificate expiry at startup and warns inside 30 days; `sablectl doctor` reports the same condition.
- Sable now tightens generated sensitive-file permissions at write time. On Unix-like systems it applies owner-only file modes. On Windows it replaces inherited file ACLs with entries for the current user, Administrators, and SYSTEM. Agent-producing `sablectl` and Makefile build paths also harden built agent binaries after compilation.
- Run `sablectl doctor` after install or when moving files. It warns if sensitive local files inherit broad permissions or grant access to unexpected principals. Run `sablectl doctor --fix-permissions` to harden existing generated files in place.
- Manual Unix-like remediation: `chmod 600 config.env sable-state.json server.key pw.txt` and `chmod 700 agents`.
- Manual Windows remediation when needed:
  ```powershell
  icacls config.env sable-state.json pw.txt server.key /inheritance:r
  icacls config.env sable-state.json pw.txt server.key /grant:r "$env:USERNAME:F" "*S-1-5-18:F" "*S-1-5-32-544:F"
  ```
- Agents are stripped (`-s -w`), but ldflags string literals (server URL, agent ID, cert fingerprint) remain readable in `.rodata` via `strings`. Treat built agents as sensitive artifacts.
