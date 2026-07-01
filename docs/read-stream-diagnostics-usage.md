# Read-Stream Diagnostics — App Integration Guide (v0.5.24-dev)

This release adds diagnostic INFO/WARN logging on the app's **read stream** so you can see, in
real time, whether messages (session, endpoint, echo, etc.) are actually arriving — and whether
the connection is alive but silently not delivering.

## What you get

Three kinds of log lines:

1. **Per-message arrival** — one INFO line the instant each message is received, *before* any
   processing, with correlation fields (`region`, `stream`, `topic`, `msgID`, `type`, `tenant`,
   `device`, `bytes`, `subID`, `consumeCtx`).
2. **Periodic status + summary** — every interval: a per-stream count of messages received in that
   window, plus connection state (`disconnected`, `connectedForSec`, `reconnectCount`).
3. **Gap detection** — a WARN when a subscribed stream receives **no** messages beyond a threshold
   *while the connection is up* (catches the "silent broker drop" case).

## What the app team needs to change

**Nothing is required.** Upgrading to `v0.5.24-dev` enables sensible defaults automatically.

To tune the behaviour, set three new **optional** fields on `sdk.Config`:

| Field                 | Type            | Default if unset | Meaning                                                   |
| --------------------- | --------------- | ---------------- | --------------------------------------------------------- |
| `StatusLogInterval`   | `time.Duration` | `60s`            | How often the status + per-stream summary is logged       |
| `LogEachMessage`      | `*bool`         | `true`           | Per-message arrival logging; set to `&false` to disable   |
| `MessageGapThreshold` | `time.Duration` | `2m`             | Silence duration (connection up) before a gap WARN fires  |

```go
disable := false
cfg := sdk.Config{
    // ...existing fields...
    StatusLogInterval:   30 * time.Second,
    MessageGapThreshold: 90 * time.Second,
    LogEachMessage:      &disable, // optional: turn off the per-message line in production
}
```

## Important notes

- **Multi-instance apps:** set these fields on the **parent** `sdk.Config`. They now auto-propagate
  to the app instance created by `CreateAppInstance` / `SetAppInstance`. No per-instance wiring is
  needed.
- **Log level:** these lines are emitted at `INFO` (gaps at `WARN`). Ensure your custom
  `log.SDKLogger` passes INFO through — do not filter above INFO.
- **No breaking changes.** All three fields are optional; zero values fall back to the defaults
  above. `LogEachMessage` is a `*bool` so that `nil` (unset) means enabled, distinct from an
  explicit `false`.
- **Volume:** `LogEachMessage=true` emits one line per message. On high-traffic tenants, consider
  relying on the `StatusLogInterval` summaries and setting `LogEachMessage:&false`.
- **Privacy:** no payload bodies are logged (only byte counts) — safe for PII.

## How to read the logs

- **Missing session data?** Check the `Read-stream summary ... received=N` count for the window and
  whether a `Read-stream gap detected` WARN fired.
- **Connection wedged?** `Read-stream status ... disconnected=false connectedForSec=... reconnectCount=...`
  distinguishes a live-but-silent connection from a reconnect storm.

## Example log lines

```
INFO  Read-stream message received. region=us stream=pxcloud--session-sessions topic=... msgID=... type=data tenant=... device=... bytes=812 subID=... consumeCtx=... decodeErr=<nil>
INFO  Read-stream summary. region=us stream=pxcloud--session-sessions subID=... received=14 intervalSec=30 disconnected=false
INFO  Read-stream status. region=us groupId=... disconnected=false connectedForSec=930 reconnectCount=1 streams=[...]
WARN  Read-stream gap detected. region=us stream=pxcloud--session-sessions noMessagesForSec=125 thresholdSec=90 (connection up)
```
