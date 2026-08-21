# Read-stream diagnostics example

This example enables the optional per-message log and read-stream gap callback, shortens the
periodic status interval, publishes echo messages to active devices, and exercises normal, slow,
and blocked application callbacks. It also verifies that diagnostic settings and the callback
propagate from a parent application to its app instance.

## Build

```bash
go build ./examples/read-stream-diagnostics
```

## Configure

Create a local `config.yaml` with your application values. Do not commit populated credentials.

```yaml
app:
  id: <application-id>
  apiKey: <application-api-key>
  globalFQDN: <global-fqdn>
  regionalFQDN: <regional-fqdn>
  regionalFQDNs:
    - <regional-fqdn>
  readStream: app--<application-id>-R
  writeStream: app--<application-id>-W
  groupId: <optional-group-id>
appInstance:
  otp: <one-time-password>
  name: read-stream-diagnostics
  id: ""
  apiKey: ""
  tenant:
    id: ""
    name: ""
    token: ""
```

On the first successful run, the example redeems the OTP and rewrites this file with the app
instance and tenant values using owner-only file permissions. Later runs reuse those stored values.

## Run

```bash
./read-stream-diagnostics -config ./config.yaml
```

The example explicitly sets:

- a 10-second status interval;
- per-message arrival logging enabled;
- confirmed read-stream gap and recovery notifications enabled;
- an echo publish every 3 seconds;
- callback mode `cycle`: normal, slow for 12 seconds, then blocked for 40 seconds.

Useful focused runs:

```bash
# Verify the SDK default/no-per-message path while retaining summaries.
./read-stream-diagnostics -config ./config.yaml -log-each-message=false

# Focus on blocked-callback detection and recovery.
./read-stream-diagnostics -config ./config.yaml -callback-mode blocked -status-interval 5s

# Cross the established 60-second processing timeout to verify detected/recovered gap callbacks.
./read-stream-diagnostics -config ./config.yaml -callback-mode blocked -block-duration 75s -status-interval 5s
```

Available controls are `-status-interval`, `-log-each-message`, `-publish-interval`,
`-callback-mode`, `-slow-duration`, and `-block-duration`. Press Ctrl+C to stop; an active simulated
callback is released during shutdown.

## Expected output

Exact identifiers and counters vary. A healthy message should produce correlated arrival,
application-dispatch, callback, and summary evidence:

```text
INFO Read-stream message received. region=<region> stream=app--<id>-R topic=pxcloud--echo-echo msgID=<id> ... partition=1 offset=6202
INFO App received read-stream message. msgID=<id> type=data topic=pxcloud--echo-echo ...
INFO DIAGNOSTIC TEST callback started. sequence=1 action=normal msgID=<id> ...
INFO DIAGNOSTIC TEST callback returned. sequence=1 action=normal msgID=<id>
INFO Read-stream summary. region=<region> stream=app--<id>-R ... state=healthy_delivery received=1 ...
```

A blocked callback should be attributed separately at the application and SDK layers:

```text
WARN User DeviceMessageHandler slow/blocked. region=<region> msgID=<id> topic=pxcloud--echo-echo elapsedSec=10
WARN SDK read-stream processing slow/blocked; subscriber cannot consume until it returns. region=<region> ... msgID=<id> ... elapsedSec=10
INFO Read-stream summary. region=<region> ... state=sdk_processing_blocked ... sdkProcessingActive=true ...
WARN User DeviceMessageHandler still blocked. region=<region> msgID=<id> ...
WARN SDK read-stream processing still blocked; subscriber cannot consume until it returns. region=<region> ...
WARN User DeviceMessageHandler returned after delay. region=<region> msgID=<id> ... durationSec=40
WARN SDK read-stream processing returned after delay. region=<region> ... msgID=<id> ... durationSec=40
```

A callback that remains blocked past the established processing timeout, or a consume request that
does not receive a broker response, produces one gap notification. Recovery is emitted only after
reconnect/resubscribe completes and the stream receives a subsequent broker response:

```text
WARN Consume timeout. Disconnecting. region=<region> stream=<stream> ... reason=processing_backpressure ...
WARN DIAGNOSTIC TEST read-stream gap notification. state=detected reason=processing_backpressure region=<region> stream=<stream> reconnectCount=0 ...
INFO Reconnect complete. region=<region> ... reconnectCount=1
INFO DIAGNOSTIC TEST read-stream gap notification. state=recovered reason=processing_backpressure region=<region> stream=<stream> reconnectCount=1 ...
```

The SDK dispatches `ReadStreamGapHandler` asynchronously and serially. The handler should return
promptly; notifications are operational and best-effort. A responsive broker returning no messages
is valid `broker_quiet` behavior and does not invoke the gap callback.

Normal shutdown includes a final status snapshot and termination marker:

```text
INFO Read-stream status. region=<region> groupId=<group> ...
INFO Read-stream status logger stopped. region=<region> groupId=<group> reason=context canceled
```

The existing 15-second consume timeout remains responsible for detecting a broker response timeout
and initiating the established reconnect/resubscribe flow. The callback exposes that confirmed
condition to the application; it does not introduce a second stall timeout or change recovery
behavior.
