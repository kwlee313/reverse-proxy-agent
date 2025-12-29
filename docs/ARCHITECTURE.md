## Architecture and Recovery Logic

This document explains how the rpa codebase is structured and how the recovery
logic works in detail.

### Overview

rpa keeps SSH tunnels alive for two modes:
- **Agent**: remote forward tunnels (server-side listeners).
- **Client**: local forward tunnels (local listeners).

Both modes share the same supervision core so behavior is consistent:
`internal/supervisor` drives process lifecycle, recovery policy, and monitoring.

### Core components

- `internal/supervisor`
  - Owns the SSH process lifecycle, state machine, and recovery policy.
  - Emits structured logs (JSON) and tracks metrics (restart counts, last exit).
- `internal/agent` / `internal/client`
  - Wrap the supervisor and build SSH commands for each mode.
  - Provide runtime mutation of forwards (add/remove/clear) and trigger restarts.
- `pkg/monitor`
  - Detects sleep/wake and network change events.
  - On macOS with cgo enabled it uses native frameworks.
  - When cgo is disabled or on non-darwin, it falls back to polling.
- Sleep prevention (optional):
  - `agent.prevent_sleep` / `client.prevent_sleep` wraps the runtime with
    `caffeinate` to keep the system awake while tunnels are running.
- `pkg/restart`
  - Backoff policy with exponential delay, jitter, and debounce window.
- `pkg/sshutil`
  - Buffers SSH stderr and classifies exit failures for diagnostics.
- `pkg/ipc`
  - Unix socket RPC for status/logs/metrics and runtime config changes.

### Recovery logic (detailed)

The recovery logic lives in `internal/supervisor`. The flow is:

1) **Start attempt**
   - Build the SSH command (`internal/agent/ssh.go` or `internal/client/ssh.go`).
   - Transition state to CONNECTING, then CONNECTED when the process starts.
   - Record start success/failure counters.

2) **Success marking (grace period)**
   - A "success" is recorded only after the SSH process stays alive for a short
     grace period (2 seconds). This avoids counting rapid failures as success.

3) **Monitor triggers**
   - Sleep/wake and network change monitors run in goroutines.
   - On events, `RequestRestart` is called with a debounce window to avoid
     restart storms (for example, multiple network events in quick succession).

4) **Process exit classification**
   - When SSH exits, stderr lines are buffered and classified into categories:
     `auth`, `hostkey`, `dns`, `network`, `refused`, `timeout`, `unknown`.
   - Classification is used for:
     - User-facing hints (`client run` and `doctor`).
     - Policy decisions (stop vs retry).

5) **Policy and backoff**
   - Policies:
     - `always`: retry on any exit.
     - `on-failure`: retry only on non-zero exit.
   - Certain classes (`auth`, `hostkey`) stop immediately to avoid infinite
     retries that require manual action.
   - Backoff:
     - Exponential delay with min/max, factor, and jitter.
     - Reset when a clean exit occurs.
   - Debounce:
     - Prevents repeated restarts from the same burst of triggers.

6) **Periodic restart (optional)**
   - A periodic timer can trigger restarts to avoid long-lived half-dead states.

### Runtime updates

- `agent add/remove/clear` and `client add/remove/clear` modify the config on
  disk and also attempt to update the running process via IPC.
- If the daemon is running, it restarts the SSH process to apply new forwards.
- If the daemon is not running, changes are persisted and take effect on next
  start.
- `clear` removes all forwards and stops the service (agent/client).

### Observability

- Logs are JSON lines for easy ingestion.
- `status` returns state, uptime, restart counts, last exit, last success time.
- `metrics` exports counters and timestamps suitable for scraping.
- Detailed schema: `docs/OBSERVABILITY.md`.

### File map (entry points)

- CLI entry: `cmd/rpa/main.go`
- CLI routing: `internal/cli/cli.go`
- Agent runtime: `internal/agent/agent.go`, `internal/agent/ssh.go`
- Client runtime: `internal/client/client.go`, `internal/client/ssh.go`
- Supervisor core: `internal/supervisor/supervisor.go`
- IPC servers: `internal/agent/ipc/server.go`, `internal/client/ipc/server.go`
