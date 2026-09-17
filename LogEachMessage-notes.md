# `LogEachMessage` - Live Toggle Discussion & Options

## The core problem (original)
`LogEachMessage` was originally a `*bool` on `Config`, read **once** at `sdk.New(...)` and baked into
each `pubsub.Connection`. Flipping it required tearing down/recreating the `App`, which meant a
restart - risky for downstream services depending on the stream staying connected.

## The 4 options considered, in order of how "big a change" they are

| # | Approach | Pros | Cons |
|---|----------|------|------|
| **1** | **Reuse the SDK's existing Debug level** instead of a bespoke flag (switch the two `Infof` call sites to `Debugf`) | Zero new API - any team using a logger with runtime level control (zap `AtomicLevel`, logrus `SetLevel`) gets live toggling for free | Coarse - turns on *all* Debug logs, not just per-message; still needs the `DefaultLogger.Level` race fixed |
| **2** | **Keep a dedicated flag, but make it truly dynamic** - `App.SetLogEachMessage(bool)` backed by `atomic.Bool`, shared across reconnects | Smallest, most surgical fix; doesn't touch other Debug logs; answers "no restart" directly | Yet another bespoke on/off switch instead of reusing the level system |
| **3** | **Callback/observer hook** - `Config.OnMessageReceived func(id string, headers map[string]string, size int)` instead of built-in logging | Downstream/upstream teams fully control what happens (log it, sample it, forward to metrics/tracing) - SDK stops owning formatting/volume decisions | Bigger API change; callback must be cheap/non-blocking since it's on the hot path |
| **4** | **Sampling** - log only 1-in-N messages or on a condition | Controls volume/cost for high-throughput streams | Doesn't solve "toggle without restart" by itself - complementary to #2 or #3, not a replacement |

These aren't mutually exclusive - e.g. do #2 now for the immediate "no restart" ask, and treat #1's
thread-safety bug as a separate follow-up.

## Current repo state (as of 2026-09-09)

Option 2 has been **fully implemented**:
- [app.go](app.go#L190-L192) - `logEachMessageEnabled atomic.Bool`
- [app.go](app.go#L458-L466) - `App.SetLogEachMessage(enabled bool)` propagates live to all connections
- [internal/pubsub/connection.go](internal/pubsub/connection.go#L141-L155) and
  [internal/pubsub/reconnect.go](internal/pubsub/reconnect.go#L74-L82) - the atomic flag survives
  reconnects
- Tests: [app_test.go](app_test.go#L64-L86), [internal/pubsub/connstats_test.go](internal/pubsub/connstats_test.go#L289-L329)

So the "no restart" ask is solved via the dynamic-flag route.

## Not yet acted on

1. **Option #1's thread-safety angle** - the per-message log calls still use `Infof`, not `Debugf`.
   `DefaultLogger.Level` in [log/log.go](log/log.go#L29-L33) is still a plain `int`, unsafe to flip
   concurrently if a caller ever tried to change log level live.
2. **Option #3, the callback hook** - not implemented. Still worth considering if downstream teams
   want more control than a log line (e.g. routing to their own metrics/tracing system instead of
   just logging).

## Open question
Pursue #1's thread-safety fix, add the callback hook as a complementary option, or treat the current
`SetLogEachMessage` dynamic-flag solution as sufficient as-is?
