# Changelog

## 2.0.0 - 2026-04-28

Sable 2.0 is a major operator-experience and tasking release. It expands the web console, adds setup and build workflows, introduces artifact-oriented results, and strengthens validation and result handling across agent, API, and listener paths.

### Web Console

- Reworked the browser UI around a session sidebar, active-session workspace, resizable output pane, and Task Builder.
- Added Session Details with Jobs, Artifacts, Notes, and Audit panels.
- Added session notes and tags with operator API persistence.
- Added a local artifact library for screenshots, PEAS output, snapshots, downloads, and saved console output.
- Added **Save Output** to capture currently rendered output as a text artifact.
- Fixed **Clear Output** so it clears persisted server-side output history instead of only clearing the browser DOM.
- Added confirmation for **Clear Output** before removing session output history.
- Added output search, jump-to-latest behavior, and more explicit empty states.
- Added task cancellation UI for cancellable background tasks such as PEAS.
- Added a remote file browser for Download/Upload path selection.
- Added path completion and path browse priming for remote file workflows.
- Added drag-and-drop upload support and clearer upload/download progress messaging.
- Refreshed README screenshots and simplified the image set to login, main console, and Session Details.

### Agent Tasks

- Added read-only process listing (`ps`).
- Added bounded screenshot capture with platform-aware capture paths.
- Added read-only persistence-location review for common autorun locations.
- Added bounded host snapshot text reports.
- Added PEAS execution with progress output and final text artifact results.
- Added optional embedded PEAS cache support for offline targets.
- Added background task cancellation support.
- Added remote directory listing and path completion helpers for the web file browser.
- Increased upload/download file size support to 50 MB, with HTTPS required for large uploads.
- Added download progress results for long-running file reads.
- Added interactive shell mode with fast temporary beaconing while active.

### Protocol, Session, And Listener Handling

- Added chunked task result metadata and server-side reassembly for large outputs.
- Added output history clearing in the session store and operator API.
- Added SSE task output streaming for real-time web UI updates.
- Added queued task summaries, recent job state, and audit events in session state.
- Added task output history caps, chunk size limits, stale chunk eviction, and tests for reassembly behavior.
- Added DNS beacon chunk assembly limits and large-upload rejection over DNS transport.
- Kept operator API binding loopback-only and preserved existing beacon encryption, MAC, nonce, and timestamp protections.

### API And Validation

- Added new task types and validation for `ps`, `screenshot`, `persistence`, `peas`, `snapshot`, `ls`, `cancel`, `complete`, and `pathbrowse`.
- Added `GET /api/audit` for recent operator/session audit events.
- Added `GET /api/agents/:id/queued` for queued task inspection.
- Added `DELETE /api/agents/:id/tasks/:taskID` to remove queued tasks before delivery.
- Added `PUT /api/agents/:id/metadata` for notes and tags.
- Added `DELETE /api/agents/:id/tasks` to clear output history.
- Added stricter payload normalization, path validation, upload validation, and DNS transport size checks.

### Setup, Build, And Tooling

- Added the interactive setup/rebuild wizard (`make wizard`, `make install`).
- Added support for labeled per-agent build output directories under `builds/<label>/`.
- Added `make register NEW=1` flow for creating and registering additional identities.
- Added profile-aware setup for default, fast, quiet, and DNS configurations.
- Added offline PEAS build targets and `make update-peas`.
- Added Windows-aware Makefile paths and build output handling.
- Added `.gitattributes` to keep source/docs line endings stable and mark screenshots as binary.

### Documentation

- Rewrote the setup flow around the wizard-first path.
- Expanded web UI, CLI, REST API, configuration, and task reference documentation.
- Added clear notes for embedded web assets, screenshot set expectations, and rebuild requirements.
- Updated screenshots to match the current 2.0 UI.

