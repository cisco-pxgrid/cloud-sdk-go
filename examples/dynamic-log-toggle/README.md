# dynamic-log-toggle

Demonstrates driving `App.SetLogEachMessage` from an environment variable, `PXCLOUD_SDK_DEBUG`
(`TRUE`/`FALSE`), without restarting the SDK `App`.

Like `examples/multi-instance`, this example links to a tenant via an **app instance**
(`app.CreateAppInstance` + `appInstance.LinkTenant(otp)` on first run using an OTP, or
`app.SetAppInstance` + `appInstance.SetTenant` on subsequent runs using the persisted instance
credentials). The live per-message logging toggle is applied to the `appInstance`, since that's
the object carrying the live subscription.

## Config

Copy `config_sample.yaml` to a gitignored `config.yaml` (see `.gitignore`) and fill in real values
- never edit `config_sample.yaml` in place, since a successful OTP redemption automatically
rewrites the config file with the issued `appInstance.id`/`apiKey` and `tenant.id`/`name`/`token`.

```yaml
app:
  id: ...
  apiKey: ...
  ...
  logEachMessage: false   # startup default; PXCLOUD_SDK_DEBUG overrides it when explicitly set

appInstance:
  otp: ...        # single-use; leave blank after first successful run
  name: ...
  id:             # filled in automatically after OTP redemption
  apiKey:         # filled in automatically after OTP redemption
  tenant:
    id:
    name:
    token:
```

## Run

```powershell
$env:PXCLOUD_SDK_DEBUG = "true"
go run . -config config.yaml
```

On startup, the example reads `PXCLOUD_SDK_DEBUG` once to seed `LogEachMessage`, then starts a
background loop that re-checks the variable every `-poll-interval` (default 5s) and also on
`SIGHUP`, calling `appInstance.SetLogEachMessage(...)` whenever the value changes.

## Read this before using the pattern for real ops toggling

**A running process's environment variables are fixed at startup.** Running `export
PXCLOUD_SDK_DEBUG=false` in a shell, or updating a container's env and *not* restarting the
container, does **not** change what `os.Getenv` returns inside an already-running process - this
is an OS-level fact, not something the SDK or this example can work around. Sending `SIGHUP` to
this process does **not** cause it to see a new value either, for the same reason.

So in this example, the poll/`SIGHUP` loop will only ever observe a change if something *inside
this same process* calls `os.Setenv(debugEnvVar, ...)` - e.g. from a test harness driving this
binary. That's enough to demonstrate and test the `SetLogEachMessage` call path end-to-end, but it
is **not** a working mechanism for an external operator to flip logging on a live, already-running
downstream service.

## For genuine no-restart control in production

Pick one of these instead, and call `app.SetLogEachMessage(enabled)` directly - no env var involved:

- **HTTP admin endpoint** - expose a small route (e.g. `/debug/log-each-message?enabled=true`)
  that calls `app.SetLogEachMessage`. Easiest to test with `curl` and to gate behind existing
  internal auth.
- **Watched file** - have the downstream service poll (or `fsnotify`-watch) a local file that an
  operator or config-management tool can rewrite while the process keeps running, then apply the
  parsed value via `app.SetLogEachMessage`.
- **Existing config-reload hook** - if the downstream app already has a feature-flag or
  config-reload mechanism, call `app.SetLogEachMessage(newValue)` from inside that existing
  callback.

## What's already validated

This example has been run end-to-end against a real pxGrid Cloud tenant (app-instance link via
OTP, live PubSub subscription, and a runtime `logEachMessage` toggle applied without a restart or
reconnect). See [TEST-VALIDATION.md](TEST-VALIDATION.md) for the replication steps and captured
logs.

The underlying mechanism this example calls, `App.SetLogEachMessage`, is a thin wrapper that
forwards straight to `pubsub.Connection.SetLogEachMessage` (see
[app.go](../../app.go#L458-L466)). That exact code path is also covered by an automated test
against a local mock pxGrid pubsub server,
[internal/pubsub/log_each_message_live_toggle_integration_test.go](../../internal/pubsub/log_each_message_live_toggle_integration_test.go),
which runs a real connect -> subscribe -> receive -> toggle -> receive flow and asserts the
per-message log line only appears after the toggle flips - with no reconnect in between. Run it
with:

```powershell
go test ./internal/pubsub/... -run TestSetLogEachMessage_LiveToggleOnRealSubscription -v
```

See [read-stream-diagnostics](../read-stream-diagnostics/README.md) for a fuller diagnostics
example that also needs real credentials to run.
