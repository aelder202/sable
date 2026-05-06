# REST API

The full machine-readable contract lives in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0.3). Use it for client generation, contract testing (e.g. `schemathesis`), or interactive exploration in tools like Swagger UI / ReDoc / Postman.

This page is a quick-scan summary. Everything except `POST /api/auth/login` requires `Authorization: Bearer <jwt>`. Error responses are plain text (`text/plain; charset=utf-8`); see `openapi.yaml` for canonical messages and per-endpoint status codes.

| Method | Path | Notes |
|--------|------|-------|
| `POST` | `/api/auth/login` | `{"password":"..."}` → `{"token":"..."}`. Rate-limited 5/min/IP. |
| `GET` | `/api/agents` | List agents (no `outputs`). |
| `POST` | `/api/agents` | Register. `{"id":"...","secret_hex":"..."}`. `id` is 1–64 alphanumeric+hyphen. |
| `GET` | `/api/agents/:id` | Single agent with task output history. |
| `POST` | `/api/agents/:id/task` | Queue a task. `{"type":"shell","payload":"id"}`. Types: `shell`, `ps`, `screenshot`, `persistence`, `peas`, `snapshot`, `ls`, `cancel`, `download`, `upload`, `complete`, `pathbrowse`, `sleep`, `kill`, `interactive`. |
| `GET` | `/api/agents/:id/queued` | List queued tasks not yet delivered. |
| `DELETE` | `/api/agents/:id/tasks/:taskID` | Remove a queued task before delivery. |
| `PUT` | `/api/agents/:id/metadata` | Update operator notes and tags. |
| `GET` | `/api/agents/:id/artifacts` | List artifact summaries (no `data`). |
| `POST` | `/api/agents/:id/artifacts` | Save an operator artifact. |
| `GET` | `/api/agents/:id/artifacts/:artifactID` | Full artifact including base64 `data`. |
| `GET` | `/api/agents/:id/tasks` | Output history. |
| `DELETE` | `/api/agents/:id/tasks` | Clear output history. |
| `GET` | `/api/agents/:id/terminal/stream` | SSE stream of task output. Used by the web UI for real-time interactive output and path completion. |
| `GET` | `/api/audit` | Recent operator and session audit events. |

Run `make validate-openapi` to lint the spec (requires Node.js; uses `npx`, nothing is committed to the repo).
