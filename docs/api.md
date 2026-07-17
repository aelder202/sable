# REST API

The full machine-readable contract lives in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0.3). Use it for client generation, contract testing (e.g. `schemathesis`), or interactive exploration in tools like Swagger UI / ReDoc / Postman.

This page is a quick-scan summary. Everything except `POST /api/auth/login` requires `Authorization: Bearer <jwt>`. Error responses are plain text (`text/plain; charset=utf-8`); see `openapi.yaml` for canonical messages and per-endpoint status codes.

| Method | Path | Notes |
|--------|------|-------|
| `GET` | `/api/overview` | Fleet counters, per-agent summaries, task-outcome buckets, and recent activity. |
| `PUT` | `/api/overview/alerts/:alertID` | Acknowledge an Overview failed-task alert without changing retained agent output or outcome history. |
| `PUT` | `/api/agents/:id/lifecycle` | Retire or restore an agent without deleting evidence. |
| `POST` | `/api/admin/shutdown` | Gracefully stop the local server. Used by `sablectl down`. |
| `POST` | `/api/auth/login` | `{"password":"..."}` → `{"token":"..."}`. Rate-limited 5/min/IP. |
| `GET` | `/api/agents` | List agents (no `outputs`). Supports `limit`/`offset`. |
| `POST` | `/api/agents` | Register. `{"id":"...","secret_hex":"...","display_name":"..."}`. `display_name` is optional; `id` is 1–64 alphanumeric+hyphen. |
| `GET` | `/api/agents/:id` | Single agent with task output history. |
| `DELETE` | `/api/agents/:id` | Revoke an agent and delete its retained server-side state. |
| `POST` | `/api/agents/:id/rekey` | Rotate the agent secret. Returns the new 64-character `secret_hex` once. |
| `POST` | `/api/agents/:id/task` | Queue a task. Includes `download_archive` for bounded directory or multi-path ZIP results. |
| `GET` | `/api/agents/:id/queued` | List queued tasks not yet delivered. |
| `DELETE` | `/api/agents/:id/tasks/:taskID` | Remove a queued task before delivery. |
| `PUT` | `/api/agents/:id/metadata` | Update display name, notes, and tags. |
| `GET` | `/api/agents/:id/artifacts` | List artifact summaries (no `data`). Supports `limit`/`offset`. |
| `POST` | `/api/agents/:id/artifacts` | Save an operator artifact. |
| `GET` | `/api/agents/:id/artifacts/:artifactID` | Full artifact including base64 `data`. |
| `DELETE` | `/api/agents/:id/artifacts/:artifactID` | Delete artifact metadata and its retained blob. |
| `PUT` | `/api/agents/:id/artifacts/retention` | Set newest-artifact retention with `{"max_items":1..256}`. |
| `GET` | `/api/agents/:id/tasks` | Output history. Supports `limit`/`offset`. |
| `DELETE` | `/api/agents/:id/tasks` | Clear output history. |
| `GET` | `/api/agents/:id/terminal/stream` | SSE stream of task output. Used by the web UI for real-time interactive output and path completion. |
| `GET` | `/api/audit` | Recent operator and session audit events. Supports `limit`/`offset`. |

Paginated responses include `X-Total-Count`, `X-Limit`, and `X-Offset` headers. Queued-task summaries expose `status` (`queued` or `in_flight`), `delivery_attempts`, and `last_delivered_at`. Completed task outputs retain the sanitized task `payload`, `queued_at`, and `last_delivered_at` so clients can show the command and response duration. Agent records expose both the server-observed authenticated network source as `last_ip` and the route-selected local address reported by the agent as `host_ip`. The Agent Details UI displays `host_ip`. Artifact summaries include `size_bytes` when known.

Run `make validate-openapi` to lint the spec (requires Node.js; uses `npx`, nothing is committed to the repo).
