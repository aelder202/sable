# Troubleshooting

| Symptom | Check |
|---------|-------|
| `bind: permission denied` | Run with root / admin, or change the listener ports. |
| `bind: address already in use` | Something else holds `443` or `8443`. Stop it or move ports. |
| Browser warns about the cert | Expected for the self-signed operator UI. Confirm you are hitting `https://127.0.0.1:8443`. |
| Web UI unreachable from another host | Loopback only. Use `ssh -L 8443:127.0.0.1:8443 user@sable-host`. |
| Agent does not appear | Confirm registration, matching `AGENT_ENV`, reachable `SERVER_URL`, correct cert fingerprint. |
| `sablectl agent register` cannot connect | Run it on the server host with the server up, or hit the REST API through a tunnel. |
| Web UI changes do not show | Rebuild and restart the server. `web/` assets are embedded. |
| Upload rejected | Keep the file at or below 50 MB and use HTTPS transport for large uploads. DNS transport is intended for check-ins and small responses. |
| Download, screenshot, PEAS, or snapshot result is large | Results are split into bounded chunks and reassembled server-side before the UI offers the save action. Check output history if a chunked result appears delayed. |
| DNS fallback receives nothing | Confirm `--dns-domain` / `SABLE_DNS_DOMAIN`, UDP 53 reachability, the agent built with the same `DNS_DOMAIN`, and the NS record. |
| Server listening but unresponsive | Restart with `--debug-addr 127.0.0.1:6060`, then capture `http://127.0.0.1:6060/debug/pprof/goroutine?debug=2`. |
| `sablectl agent register` returns `login failed (HTTP 401)` | Password sent doesn't match the one the server hashed. Confirm `SABLE_OPERATOR_PASSWORD` / `C2_OPERATOR_PASSWORD` aren't set in your shell (server prefers env vars over the password file). If the password file was written by PowerShell's default `Set-Content` it may be UTF-16; rewrite it with `Set-Content -Encoding ascii -NoNewline .\pw.txt "yourpassword"` and retry. |
| `sablectl reset` says `Access is denied` | The server (or another process) is holding `sable-server.exe`. Stop it (`Get-Process sable-server \| Stop-Process` on Windows, `pkill sable-server` on Linux) and rerun. `reset` is best-effort — it removes everything else and exits non-zero so you can finish cleanup. |
