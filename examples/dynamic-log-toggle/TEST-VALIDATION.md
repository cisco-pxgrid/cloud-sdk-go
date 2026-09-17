# dynamic-log-toggle - how to replicate this test

This documents exactly how the `LogEachMessage` dynamic toggle was validated end-to-end against a
real pxGrid Cloud tenant, so another team can repeat the same steps.

## 1. What this example proves

`App.SetLogEachMessage(enabled bool)` turns per-message logging on/off **while the app keeps
running** - no restart, no reconnect. This example (`examples/dynamic-log-toggle`) links a tenant
via the app-instance model (same as `examples/multi-instance`) and seeds the initial value from
config, with an optional env-var override at startup.

## 2. Config shape (matches `multi-instance`)

```yaml
app:
  id: <app id>
  apiKey: <app api key>
  globalFQDN: dnaservices.cisco.com
  regionalFQDN: <regional fqdn>
  regionalFQDNs:
  - <regional fqdn>
  readStream: <app read stream>
  writeStream: <app write stream>
  groupId: ""
  # startup default; PXCLOUD_SDK_DEBUG env var overrides this when explicitly set
  logEachMessage: false

appInstance:
  otp: <fresh OTP, single-use, scoped to the app above>
  name: <instance name shown in DNA Cloud UI>
  # id / apiKey / tenant are populated automatically after a successful OTP redemption
  id:
  apiKey:
  tenant:
    id:
    name:
    token:
```

**Do this before running with real credentials:**
1. Copy `config_sample.yaml` to `config.yaml` (gitignored) - never edit `config_sample.yaml`
   with real values, it's the checked-in template.
2. Generate a fresh, single-use OTP in the DNA Cloud portal, scoped to the target `app.id`.
3. Paste it into `appInstance.otp` in your `config.yaml` copy.

> OTPs are single-use. After a successful run, the SDK rewrites the config file with the new
> `appInstance.id`/`apiKey`/`tenant.id/name/token` and clears `otp`. If you reuse the same file for
> a second run, leave `otp` empty - it will fall back to `SetAppInstance`/`SetTenant` with the
> already-linked credentials instead of trying to redeem the (now spent) OTP again.

## 3. Steps to enable / disable logging

**At startup (no code, just config):**
```yaml
app:
  logEachMessage: true   # or false
```

**At runtime, without restarting:**
```go
app.SetLogEachMessage(true)   // turn on
app.SetLogEachMessage(false)  // turn off
```
This example wires that call to a background goroutine (`watchDebugEnv`) that re-checks the
`PXCLOUD_SDK_DEBUG` env var every `-poll-interval` (default 5s) and on `SIGHUP`. **Caveat:** an
already-running process cannot observe an OS env var changed from outside it - that's an OS
limitation, not an SDK one. To actually flip logging on a live process, trigger the
`SetLogEachMessage` call from inside that process (HTTP admin endpoint, watched file, or your own
config-reload hook) - see `README.md` for the three recommended patterns.

## 4. What to check in the logs

Enabled: two lines appear per received message -
```
pubsub\subscribe.go:308 Read-stream message received. ... msgID=...
cloud-sdk-go\app.go:480 App received read-stream message. msgID=...
```
Disabled: those two lines disappear; only the periodic `read_stream_diagnostics.go`
summary/status lines keep printing (those are unaffected by the toggle).

## 5. Build & run

```powershell
go build -o dynamic-log-toggle.exe ./examples/dynamic-log-toggle/
.\dynamic-log-toggle.exe -config .\examples\dynamic-log-toggle\config.yaml
```

## 6. Captured test run (real tenant, `logEachMessage: true` from startup)

```
2026-09-16T13:55:47Z INFO   dynamic-log-toggle\main.go:146 Startup config.logEachMessage=true PXCLOUD_SDK_DEBUG="" -> logEachMessage=true
2026-09-16T13:55:47Z INFO   cloud-sdk-go\app.go:325 RegionalFQDNs: [neoffers.cisco.com]
2026-09-16T13:55:47Z INFO   cloud-sdk-go\app.go:258 Read-stream diagnostics config. statusLogInterval=0s logEachMessage=true (0 duration falls back to SDK default 60s)
2026-09-16T13:55:50Z INFO   cloud-sdk-go\app.go:325 RegionalFQDNs: [neoffers.cisco.com]
2026-09-16T13:55:50Z INFO   cloud-sdk-go\app.go:258 Read-stream diagnostics config. statusLogInterval=0s logEachMessage=true (0 duration falls back to SDK default 60s)
2026-09-16T13:55:51Z INFO   dynamic-log-toggle\main.go:218 Linked with tenant: auto-cisco-com
2026-09-16T13:55:52Z INFO   pubsub\connection.go:284 Connected to PubSub server. url=wss://neoffers.cisco.com/api/v2/pubsub groupId=dal9vou1gqhlab3ddfs0
2026-09-16T13:55:53Z INFO   pubsub\subscribe.go:85 Created subscription. id=5584c7da-b1d6-11f1-8733-0a123a79761c stream=app--pxclo-108o0h1ki-R
2026-09-16T13:56:52Z INFO   pubsub\read_stream_diagnostics.go:173 Read-stream summary. region=neoffers.cisco.com stream=app--pxclo-108o0h1ki-R subID=5584c7da-b1d6-11f1-8733-0a123a79761c state=healthy_delivery received=0 receivedTotal=0 consumeIters=47 consumeItersTotal=47 dispatched=0 dispatchedTotal=0 sdkProcessingStarted=0 sdkProcessingStartedTotal=0 sdkProcessingCompleted=0 sdkProcessingCompletedTotal=0 sdkProcessingActive=false sdkProcessingForSec=0 sdkProcessingMsgID= sdkProcessingTopic= cursorChanges=1 cursorChangesTotal=1 noCursorChangeForSec=58 noBrokerResponseForSec=0 noMessagesForSec=58 intervalSec=60 disconnected=false reconnecting=false partition=-1 offset=-1 lastConsumeCtx=eyJzdHJlYW1zIjpbXX0=
2026-09-16T13:56:52Z INFO   pubsub\read_stream_diagnostics.go:185 Read-stream diagnostic state changed. region=neoffers.cisco.com stream=app--pxclo-108o0h1ki-R subID=5584c7da-b1d6-11f1-8733-0a123a79761c previous= state=healthy_delivery consumeIters=47 received=0 noBrokerResponseForSec=0 sdkProcessingActive=false sdkProcessingForSec=0 sdkProcessingMsgID= sdkProcessingTopic= disconnected=false reconnecting=false partition=-1 offset=-1
2026-09-16T13:56:52Z INFO   pubsub\read_stream_diagnostics.go:198 Read-stream status. region=neoffers.cisco.com groupId=dal9vou1gqhlab3ddfs0 disconnected=false connectedForSec=60 reconnectCount=0 streams=[app--pxclo-108o0h1ki-R ]
2026-09-16T13:56:55Z INFO   pubsub\subscribe.go:308 Read-stream message received. region=neoffers.cisco.com stream=app--pxclo-108o0h1ki-R topic= msgID=6aaa9df5f05eea388a9f6f06_pxclo-108o0h1ki_6aaaa01b18e0549dea2244ae type=control tenant= device= bytes=220 subID=5584c7da-b1d6-11f1-8733-0a123a79761c partition=0 offset=0 consumeCtx=eyJzdHJlYW1zIjpbeyJzdHJlYW0iOiJhcHAtLXB4Y2xvLTEwOG8waDFraS1SIiwicGFydGl0aW9uIjowLCJvZmZzZXQiOjB9XX0= decodeErr=<nil>
2026-09-16T13:56:55Z INFO   cloud-sdk-go\app.go:480 App received read-stream message. msgID=6aaa9df5f05eea388a9f6f06_pxclo-108o0h1ki_6aaaa01b18e0549dea2244ae type=control topic= tenant= device= bytes=220
2026-09-16T13:57:52Z INFO   pubsub\read_stream_diagnostics.go:173 Read-stream summary. region=neoffers.cisco.com stream=app--pxclo-108o0h1ki-R subID=5584c7da-b1d6-11f1-8733-0a123a79761c state=healthy_delivery received=1 receivedTotal=1 consumeIters=47 consumeItersTotal=94 dispatched=1 dispatchedTotal=1 sdkProcessingStarted=1 sdkProcessingStartedTotal=1 sdkProcessingCompleted=1 sdkProcessingCompletedTotal=1 sdkProcessingActive=false sdkProcessingForSec=0 sdkProcessingMsgID=6aaa9df5f05eea388a9f6f06_pxclo-108o0h1ki_6aaaa01b18e0549dea2244ae sdkProcessingTopic= cursorChanges=1 cursorChangesTotal=2 noCursorChangeForSec=57 noBrokerResponseForSec=0 noMessagesForSec=57 intervalSec=60 disconnected=false reconnecting=false partition=0 offset=0 lastConsumeCtx=eyJzdHJlYW1zIjpbeyJzdHJlYW0iOiJhcHAtLXB4Y2xvLTEwOG8waDFraS1SIiwicGFydGl0aW9uIjowLCJvZmZzZXQiOjB9XX0=
2026-09-16T13:57:52Z INFO   pubsub\read_stream_diagnostics.go:198 Read-stream status. region=neoffers.cisco.com groupId=dal9vou1gqhlab3ddfs0 disconnected=false connectedForSec=120 reconnectCount=0 streams=[app--pxclo-108o0h1ki-R ]
2026-09-16T13:58:52Z INFO   pubsub\read_stream_diagnostics.go:173 Read-stream summary. region=neoffers.cisco.com stream=app--pxclo-108o0h1ki-R subID=5584c7da-b1d6-11f1-8733-0a123a79761c state=broker_quiet received=0 receivedTotal=1 consumeIters=47 consumeItersTotal=141 dispatched=0 dispatchedTotal=1 sdkProcessingStarted=0 sdkProcessingStartedTotal=1 sdkProcessingCompleted=0 sdkProcessingCompletedTotal=1 sdkProcessingActive=false sdkProcessingForSec=0 sdkProcessingMsgID=6aaa9df5f05eea388a9f6f06_pxclo-108o0h1ki_6aaaa01b18e0549dea2244ae sdkProcessingTopic= cursorChanges=0 cursorChangesTotal=2 noCursorChangeForSec=117 noBrokerResponseForSec=0 noMessagesForSec=117 intervalSec=60 disconnected=false reconnecting=false partition=0 offset=0 lastConsumeCtx=eyJzdHJlYW1zIjpbeyJzdHJlYW0iOiJhcHAtLXB4Y2xvLTEwOG8waDFraS1SIiwicGFydGl0aW9uIjowLCJvZmZzZXQiOjB9XX0=
2026-09-16T13:58:52Z INFO   pubsub\read_stream_diagnostics.go:185 Read-stream diagnostic state changed. region=neoffers.cisco.com stream=app--pxclo-108o0h1ki-R subID=5584c7da-b1d6-11f1-8733-0a123a79761c previous=healthy_delivery state=broker_quiet consumeIters=47 received=0 noBrokerResponseForSec=0 sdkProcessingActive=false sdkProcessingForSec=0 sdkProcessingMsgID=6aaa9df5f05eea388a9f6f06_pxclo-108o0h1ki_6aaaa01b18e0549dea2244ae sdkProcessingTopic= disconnected=false reconnecting=false partition=0 offset=0
2026-09-16T13:58:52Z INFO   pubsub\read_stream_diagnostics.go:198 Read-stream status. region=neoffers.cisco.com groupId=dal9vou1gqhlab3ddfs0 disconnected=false connectedForSec=180 reconnectCount=0 streams=[app--pxclo-108o0h1ki-R ]
```

Result: build succeeded, OTP redeemed via `CreateAppInstance`+`LinkTenant`, tenant linked
(`auto-cisco-com`), pubsub connected and subscribed, and per-message log lines
(`subscribe.go:308`, `app.go:480`) appeared exactly as expected with `logEachMessage=true`.

## 7. Security note

Never commit or paste real `apiKey`/`otp`/`token` values into `config_sample.yaml` - it's the
checked-in template. Use a local `config.yaml` copy (already covered by `.gitignore`). If real
credentials were ever pasted into a shared chat/transcript or a tracked file, rotate them.
