## Observability

This document describes the log and metrics schema exposed by rpa.
Success grace period: 2 seconds (used before marking `last_success_unix`).

### Logs (JSON lines)

The agent writes one JSON object per line.

Common fields:
- `ts`: RFC3339 timestamp
- `level`: `INFO` or `ERROR`
- `event`: event name
- `msg`: optional human-readable message

Event-specific fields:
- `agent_start`: `summary`
- `ssh_started`: `summary`
- `ssh_start_failed`: `error`
- `ssh_exited`: `exit`, `class`, optional `stderr`
- `restart_triggered`: `reason`
- `restart_skipped`: `reason`, `detail`
- `restart_scheduled`: `delay_ms`
- `stop_during_backoff`: none
- `agent_stop_requested`: none
- `client_start`: `summary`
- `client_stop`: none
- `client_stop_requested`: none
- `restart_policy_stop`: `policy`, `class`, optional `reason`

Example:
```json
{"ts":"2025-01-01T00:00:00Z","level":"INFO","event":"ssh_started","summary":"user@host:22"}
```

### Status fields

`rpa status` returns:
- `state`: `STOPPED|CONNECTING|CONNECTED`
- `summary`: `user@host:port`
- `remote_forwards`: comma-separated remote forward specs (optional)
- `uptime`: agent uptime
- `socket`: unix socket path
- `restarts`: restart count
- `last_exit`: last exit description
- `last_class`: exit classification
- `last_trigger`: last restart trigger reason
- `last_success_unix`: unix timestamp of the last SSH session that stayed up past the success grace period (optional)
- `backoff_ms`: current backoff (optional)

`rpa client status` returns:
- `state`: `STOPPED|CONNECTING|CONNECTED`
- `summary`: `user@host:port (local=...)`
- `local_forwards`: comma-separated local forward specs (optional)
- `uptime`: client uptime
- `socket`: unix socket path
- `restarts`: restart count
- `last_exit`: last exit description
- `last_class`: exit classification
- `last_trigger`: last restart trigger reason
- `last_success_unix`: unix timestamp of the last SSH session that stayed up past the success grace period (optional)
- `backoff_ms`: current backoff (optional)

### Metrics keys

`rpa metrics` returns:
- `rpa_agent_state`
- `rpa_agent_restart_total`
- `rpa_agent_uptime_sec`
- `rpa_agent_start_success_total`
- `rpa_agent_start_failure_total`
- `rpa_agent_exit_success_total`
- `rpa_agent_exit_failure_total`
- `rpa_agent_last_trigger`
- `rpa_agent_last_success_unix` (optional, set after the success grace period)
- `rpa_agent_backoff_ms` (optional)

`rpa client metrics` returns:
- `rpa_client_state`
- `rpa_client_restart_total`
- `rpa_client_uptime_sec`
- `rpa_client_start_success_total`
- `rpa_client_start_failure_total`
- `rpa_client_exit_success_total`
- `rpa_client_exit_failure_total`
- `rpa_client_last_trigger`
- `rpa_client_last_success_unix` (optional, set after the success grace period)
- `rpa_client_backoff_ms` (optional)
