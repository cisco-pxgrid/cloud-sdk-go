# Read-Stream Diagnostics Test Summary

Last updated: 2026-08-14  
Branch: `dev/read-stream-diagnostics`  
Base tested commit: `7d2bb1a8f8c96ba783c2eca2b4313b6f83aeeb3c`  
M-06 gap callback: implemented and manually validated; Linux race CI pending

## Purpose

This document tracks manual validation of the read-stream serviceability changes. Each test result
should include its source log, configuration, observed evidence, conclusion, and any remaining gap.
Credentials, tenant identifiers, subscription IDs, and message IDs are omitted or redacted.

## Overall status

| Test | Status | Evidence source |
| --- | --- | --- |
| Cycle: normal, slow, and blocked callbacks | Passed | `C:\Users\kmadhura\Downloads\Test1_sdk_test_12aug26_8_33_pm.txt` |
| Per-message logging enabled | Passed | `C:\Users\kmadhura\Downloads\Test1_sdk_test_12aug26_8_33_pm.txt` |
| Periodic summaries and state transitions | Passed | `C:\Users\kmadhura\Downloads\Test1_sdk_test_12aug26_8_33_pm.txt` |
| Callback warning, reminder, completion, and recovery | Passed | `C:\Users\kmadhura\Downloads\Test1_sdk_test_12aug26_8_33_pm.txt` |
| Valid idle/standby-region `broker_quiet` behavior | Passed | `C:\Users\kmadhura\Downloads\Test1_sdk_test_12aug26_8_33_pm.txt` |
| Dedicated normal-callback run | Passed | `C:\Users\kmadhura\Downloads\Test2_sdk_test_12aug26_8_47_pm.txt` |
| Per-message logging disabled | Passed | `examples/read-stream-diagnostics/test-logeach-disabled.log` |
| Ordinary shutdown and final status log | Passed after fix | Post-fix source `Test3_...txt` confirms final per-stream evidence and one stopped marker for both regions |
| Repeated post-fix multi-region shutdown | Passed | `examples/read-stream-diagnostics/test1-cycle-after-fix.log` confirms the result after an approximately 11-minute run |
| Shutdown during blocked callback | Passed | `examples/read-stream-diagnostics/test-blocked-shutdown.log` |
| Consume-timeout reconnect, resubscribe, and post-reconnect delivery | Passed | `examples/read-stream-diagnostics/test-blocked-shutdown.log` |
| Application gap callback: detected and recovered | Passed | Focused tests plus `examples/read-stream-diagnostics/test-gap-callback.log` |
| Linux `go test -race -v ./...` for base commit | Passed | GitHub Actions run `31612764594`, job `94167993802` on Ubuntu with Go 1.20.x |
| Linux race rerun after application gap callback | Pending | Run after M-06 is committed and pushed |
| Longevity | Pending | Not run yet |

## Test 1: cycle mode with per-message logging enabled

### Execution

Source log: `C:\Users\kmadhura\Downloads\Test1_sdk_test_12aug26_8_33_pm.txt`  
Log window: 2026-08-12 14:57:15Z through 15:03:30Z  
Result: **Passed with one external device-query issue**

```powershell
.\read-stream-diagnostics.exe `
  -config .\config_sample.yaml `
  -callback-mode cycle `
  -status-interval 10s `
  -publish-interval 3s `
  -slow-duration 12s `
  -block-duration 40s `
  -log-each-message=true
```

Resolved diagnostic configuration:

```text
DIAGNOSTIC TEST configuration. statusInterval=10s logEachMessage=true publishInterval=3s callbackMode=cycle slowDuration=12s blockDuration=40s
Read-stream diagnostics config. statusLogInterval=10s logEachMessage=true (0 duration falls back to SDK default 60s)
```

### Coverage totals

| Evidence | Count/result |
| --- | ---: |
| Regional PubSub connections | 2 |
| Read-stream subscriptions | 2 |
| Successful echo publications | 7 |
| Echo publication timeouts | 7, all for the other device |
| SDK per-message arrival logs | 7 |
| Application message-dispatch logs | 7 |
| Normal callbacks | 3 |
| Slow callbacks, 12 seconds | 2 |
| Blocked callbacks, 40 seconds | 2 |
| Initial SDK blocked/slow warnings | 4 |
| SDK repeated blocked reminders | 2 |
| SDK delayed-completion logs | 4 |
| Initial user-callback blocked/slow warnings | 4 |
| User-callback repeated blocked reminders | 3 |
| User-callback delayed-completion logs | 4 |

### Connection and subscription evidence

Both configured regional connections and their subscriptions were established. Identifiers are
redacted here; the source log retains the full correlation values.

```text
Connected to PubSub server. url=wss://<region-1>/api/v2/pubsub groupId=<redacted>
Created subscription. id=<redacted> stream=<redacted-read-stream>
Connected to PubSub server. url=wss://<region-2>/api/v2/pubsub groupId=<redacted>
Created subscription. id=<redacted> stream=<redacted-read-stream>
DIAGNOSTIC TEST ready. tenant=<redacted> devices=2
```

### Successful publication, receipt, and dispatch evidence

One active device successfully generated seven echo messages. Each message was visible at the SDK
arrival point and application dispatch point before its configured callback behavior ran.

```text
DIAGNOSTIC TEST echo published. sequence=2 device=<working-device>
Read-stream message received. region=<region> stream=<read-stream> topic=pxcloud--echo-echo msgID=<redacted> type=data ... partition=1 offset=13237 ... decodeErr=<nil>
App received read-stream message. msgID=<redacted> type=data topic=pxcloud--echo-echo ...
```

### Normal callback evidence

Three callbacks used the normal path and returned immediately. The final normal callback confirms
that processing continued after a blocked callback recovered.

```text
DIAGNOSTIC TEST callback started. sequence=1 action=normal msgID=<redacted> ...
DIAGNOSTIC TEST callback returned. sequence=1 action=normal msgID=<redacted>
...
DIAGNOSTIC TEST callback started. sequence=7 action=normal msgID=<redacted> ...
DIAGNOSTIC TEST callback returned. sequence=7 action=normal msgID=<redacted>
```

### Slow callback evidence

The 12-second callbacks produced independent user-callback and SDK-processing warnings at the
10-second threshold, followed by one delayed-completion log from each layer.

```text
DIAGNOSTIC TEST simulating slow application callback. sequence=2 duration=12s
User DeviceMessageHandler slow/blocked. region=<region> msgID=<redacted> topic=pxcloud--echo-echo elapsedSec=10
SDK read-stream processing slow/blocked; subscriber cannot consume until it returns. region=<region> ... elapsedSec=10
User DeviceMessageHandler returned after delay. region=<region> msgID=<redacted> topic=pxcloud--echo-echo durationSec=12
SDK read-stream processing returned after delay. region=<region> ... msgID=<redacted> topic=pxcloud--echo-echo durationSec=12
```

The same sequence was observed twice, for callback sequences 2 and 5.

### Blocked callback and reminder evidence

The 40-second callbacks produced the initial warning and rate-limited reminders while processing
remained active.

```text
DIAGNOSTIC TEST simulating blocked application callback. sequence=3 duration=40s
User DeviceMessageHandler slow/blocked. region=<region> msgID=<redacted> topic=pxcloud--echo-echo elapsedSec=10
SDK read-stream processing slow/blocked; subscriber cannot consume until it returns. region=<region> ... elapsedSec=10
User DeviceMessageHandler still blocked. region=<region> msgID=<redacted> topic=pxcloud--echo-echo elapsedSec=25
SDK read-stream processing still blocked; subscriber cannot consume until it returns. region=<region> ... elapsedSec=25
Read-stream summary. region=<region> ... state=sdk_processing_blocked ... sdkProcessingActive=true ...
```

Both blocked callbacks then returned and emitted completion evidence:

```text
User DeviceMessageHandler returned after delay. region=<region> msgID=<redacted> topic=pxcloud--echo-echo durationSec=40
SDK read-stream processing returned after delay. region=<region> ... msgID=<redacted> topic=pxcloud--echo-echo durationSec=40
```

### State and recovery evidence

The periodic status path distinguished active blocked processing from valid broker quiet. After the
blocked callback returned and the next message was processed, the state recovered to healthy.

```text
previous=healthy_delivery state=sdk_processing_blocked ... sdkProcessingActive=true ...
previous=sdk_processing_blocked state=healthy_delivery ... sdkProcessingActive=false ...
```

The final cumulative counters were internally consistent:

```text
receivedTotal=7
dispatchedTotal=7
sdkProcessingStartedTotal=7
sdkProcessingCompletedTotal=7
sdkProcessingActive=false
partition=1 offset=13237
```

The other subscribed region continued returning empty consume responses and was correctly reported
as connected and quiet rather than blocked or disconnected:

```text
state=broker_quiet received=0 consumeIters=<positive> noBrokerResponseForSec=0 disconnected=false reconnecting=false
```

### Non-SDK issue observed

The second active device failed all seven query attempts with an HTTP timeout:

```text
DIAGNOSTIC TEST echo publish failed. sequence=<n> device=<other-device> error=failed to query: 408 Request Timeout
```

This did not prevent the working device from validating the read-stream diagnostic paths. It should
be investigated as a device/echo-service availability issue if both devices are expected to work.

The PowerShell `NativeCommandError` wrapper at process startup was caused by piping native-process
stderr through `Tee-Object`; it was not an SDK startup failure.

### Not covered by this run

- `LogEachMessage=false` behavior.
- Cancellation while an application callback is blocked.
- Final `Read-stream status logger stopped` evidence.
- Consume timeout, reconnect, resubscribe, and reconnect counter increment.
- Long-running counter consistency and absence of false warnings.

## Test 2: normal callback and ordinary shutdown

### Execution

Source log: `C:\Users\kmadhura\Downloads\Test2_sdk_test_12aug26_8_47_pm.txt`  
Log window: 2026-08-12 15:11:56Z through 15:17:26Z  
Result: **Normal delivery passed; final multi-region shutdown evidence partially passed**

```powershell
.\read-stream-diagnostics.exe `
  -config .\config_sample.yaml `
  -callback-mode normal `
  -status-interval 10s `
  -publish-interval 3s `
  -log-each-message=true
```

### Normal delivery evidence

Both regional PubSub connections and subscriptions were established. The working device produced
six successful echo-publication responses; five messages arrived before shutdown. Every received
message completed the full normal processing path without a slow/blocked warning.

| Evidence | Count/result |
| --- | ---: |
| Regional PubSub connections | 2 |
| Read-stream subscriptions | 2 |
| Successful echo publications | 6 |
| SDK per-message arrival logs | 5 |
| Application message-dispatch logs | 5 |
| Normal callback starts | 5 |
| Normal callback completions | 5 |
| SDK or user slow/blocked warnings | 0 |

Representative evidence:

```text
Read-stream message received. region=<region> stream=<read-stream> topic=pxcloud--echo-echo msgID=<redacted> ... partition=1 offset=13243 ... decodeErr=<nil>
App received read-stream message. msgID=<redacted> type=data topic=pxcloud--echo-echo ...
DIAGNOSTIC TEST callback started. sequence=5 action=normal msgID=<redacted> ...
DIAGNOSTIC TEST callback returned. sequence=5 action=normal msgID=<redacted>
Read-stream summary. region=<region> ... state=healthy_delivery received=1 receivedTotal=5 dispatched=1 dispatchedTotal=5 sdkProcessingStarted=1 sdkProcessingStartedTotal=5 sdkProcessingCompleted=1 sdkProcessingCompletedTotal=5 sdkProcessingActive=false partition=1 offset=13243
```

The delivered-message counters were consistent at shutdown:

```text
receivedTotal=5
dispatchedTotal=5
sdkProcessingStartedTotal=5
sdkProcessingCompletedTotal=5
sdkProcessingActive=false
```

The sixth accepted echo publication had not appeared on the read stream before the application was
stopped. This run does not establish whether it would have arrived later.

### Quiet-region evidence

The second regional subscription continued polling successfully without messages:

```text
state=broker_quiet received=0 receivedTotal=0 consumeIters=<positive> noBrokerResponseForSec=0 disconnected=false reconnecting=false
```

This confirms that the valid quiet region remained connected and did not generate blocked-callback
or reconnect evidence.

### Shutdown evidence and gap

The application received the interrupt and returned to the PowerShell prompt without a panic or
deadlock:

```text
Terminating diagnostics example
PubSub connection closed by server: StatusNormalClosure
PubSub connection closed
Read-stream status. region=<region-1> ... disconnected=true ... streams=[]
Read-stream status logger stopped. region=<region-1> ... reason=context canceled
PubSub connection closed by server: StatusNormalClosure
PubSub connection closed with error: failed to close WebSocket: received header with unexpected rsv bits set: true:true:true
```

Only one of the two regional connections emitted `Read-stream status logger stopped`. The final
snapshot for that region also contained `streams=[]`, so it did not include a final per-stream
summary. The other regional logger produced no final marker before process exit. This demonstrates
that the final logger message exists, but multi-region shutdown does not currently guarantee that
every asynchronous status logger flushes before `App.Close` returns.

Result classification:

- Application termination without panic/deadlock: **Passed**.
- At least one final status/logger-stopped line: **Passed**.
- Final status marker for every configured region: **Failed in this run**.
- Final per-stream snapshot during shutdown: **Not observed**.
- Shutdown while a callback is actively blocked: **Not tested**.

### Repeated external issue

The other device again returned HTTP 408 for all five attempts:

```text
DIAGNOSTIC TEST echo publish failed. sequence=<n> device=<other-device> error=failed to query: 408 Request Timeout
```

This is separate from the read-stream normal-callback validation. The WebSocket RSV-bits close
error should also be retained as a non-blocking transport observation; shutdown still completed.

## Test 3: Linux race-detector suite

### Execution

Source log: `C:\Users\kmadhura\Downloads\sdk_test_race_unit_test_failure.txt`  
Command: `go test -race -v ./...`  
Environment: GitHub Actions Linux runner, Go 1.19.13  
Result: **Failed with one data-race report**

The log does not contain the tested commit SHA. It is associated with the current PR branch run,
but the workflow metadata should be used if an exact SHA is required.

### Failing test

```text
--- FAIL: TestConsumeTimeoutReconnectEmitsCompleteDiagnosticSequence (0.05s)
testing.go:1319: race detected during execution of test
FAIL github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub
Error: Process completed with exit code 1.
```

The captured output contains one `WARNING: DATA RACE` report.

### Race evidence

Writer:

```text
TestConsumeTimeoutReconnectEmitsCompleteDiagnosticSequence.func1()
internal/pubsub/read_stream_diagnostics_integration_test.go:91
```

Line 91 is the test cleanup restoring the package-global logger:

```go
log.Logger = originalLogger
```

Concurrent reader:

```text
(*Connection).statusLogger()
internal/pubsub/read_stream_diagnostics.go:144
(*Connection).Connect.func3()
internal/pubsub/reconnect.go:99
```

Line 144 emits the final asynchronous status-logger marker through `log.Logger`:

```go
log.Logger.Infof("Read-stream status logger stopped. ...")
```

### Diagnosis

The immediate shared variable is the mutable package-global `log.Logger`, but the underlying
lifecycle problem is that `Connection.Disconnect()` cancels the status logger without waiting for
that goroutine to finish. The test's deferred cleanup can therefore restore `log.Logger` while the
status goroutine is still emitting its final line.

This race is consistent with Test 2, where only one of two regional status loggers emitted its final
marker before process exit. It is not evidence of a race in the protected stream counters; it is a
shutdown-ordering race involving the asynchronous status logger and global logger replacement.

### Required correction and acceptance criteria

The connection lifecycle should provide deterministic status-logger completion. A technical fix
should cancel the status logger and wait for its completion before `Disconnect()` returns. Waiting
before tearing down stream state would also allow the requested final per-stream snapshot to retain
active-stream evidence.

Acceptance criteria:

- [x] `Disconnect()` does not return while its status logger is still running.
- [x] The final status snapshot and `Read-stream status logger stopped` line are emitted exactly once
  for every connected region.
- [x] The final snapshot retains the last per-stream evidence instead of only `streams=[]`.
- [x] The reconnect integration test does not restore `log.Logger` until all connection diagnostic
  goroutines have stopped.
- [x] `go test -race -v ./...` passes in Linux CI.

### Fix implemented after Test 3

Implementation status: **Fixed and validated by Linux race CI.**

The connection now creates a completion channel for each status-logger lifecycle. `Disconnect()`:

1. cancels the connection diagnostic context;
2. waits for the status logger to emit its final per-stream snapshot and stopped marker;
3. only then disconnects the internal connection and tears down stream lifecycle evidence.

Regression coverage now verifies:

- `Disconnect()` remains blocked until the status logger signals completion;
- the reconnect integration test explicitly disconnects before restoring the global test logger;
- shutdown logs emitted during that disconnect include `Read-stream status logger stopped`;
- the shutdown log window retains a `Read-stream summary` for `reconnect-stream` before teardown.

Local validation on 2026-08-12:

```text
go test -count=50 ./internal/pubsub -run "TestDisconnectWaitsForStatusLoggerCompletion|TestConsumeTimeoutReconnectEmitsCompleteDiagnosticSequence|TestDisconnectCancelsInFlightConnectAttempt"
PASS

go test . ./internal/pubsub ./internal/rpc ./log ./examples/basic-consumer ./examples/echo-query ./examples/multi-instance ./examples/read-stream-diagnostics
PASS

go vet . ./internal/pubsub ./internal/rpc ./log ./examples/basic-consumer ./examples/echo-query ./examples/multi-instance ./examples/read-stream-diagnostics
PASS
```

The focused lifecycle tests also passed 25 additional runs after the final assertion refinement.
Windows cannot execute `go test -race` in the current environment because CGO is disabled, so the
Linux workflow remains the authoritative acceptance check.

### Post-fix Linux race validation

Source: [GitHub Actions run 31612764594, job 94167993802](https://github.com/cisco-pxgrid/cloud-sdk-go/actions/runs/31612764594/job/94167993802?pr=47#logs)  
Environment: `ubuntu-latest`, Go 1.20.x  
Command: `go test -race -v ./...`  
Result: **Passed**

The previously failing `internal/pubsub` package passed under the race detector in 9.501 seconds.
No race warning was reported, and the workflow command completed successfully.

## Test 4: post-fix cycle and multi-region shutdown

The source file is named `Test3_sdk_test_12aug26_9_24_pm.txt`; this document calls it Test 4 because
the Linux race-detector result above is tracked as Test 3.

### Execution

Source log: `C:\Users\kmadhura\Downloads\Test3_sdk_test_12aug26_9_24_pm.txt`  
Log window: 2026-08-12 15:49:08Z through 15:53:42Z  
Tested commit: `7d2bb1a8f8c96ba783c2eca2b4313b6f83aeeb3c`  
Result: **Passed for cycle behavior and the post-fix ordinary multi-region shutdown**

The executable was rebuilt before this run and executed under PowerShell transcription:

```powershell
go build
Start-Transcript -Path .\test1-cycle-after-fix.log -Force
.\read-stream-diagnostics.exe `
  -config .\config_sample.yaml `
  -callback-mode cycle `
  -status-interval 10s `
  -publish-interval 3s `
  -slow-duration 12s `
  -block-duration 40s `
  -log-each-message=true
```

### Cycle coverage

| Evidence | Count/result |
| --- | ---: |
| Regional PubSub connections | 2 |
| Read-stream subscriptions | 2 |
| Successful echo publications | 5 |
| SDK message arrivals before shutdown | 4 |
| Application message dispatches | 4 |
| Normal callback starts/completions | 2 / 2 |
| Slow callback starts/completions | 1 / 1 |
| Blocked callback starts/completions | 1 / 1 |
| Initial SDK slow/blocked warnings | 2 |
| SDK blocked reminder | 1 |
| SDK delayed-completion logs | 2 |
| Initial user callback slow/blocked warnings | 2 |
| User blocked reminders | 2 |
| User delayed-completion logs | 2 |

The sequence covered normal, slow, blocked, and a subsequent normal callback. The blocked callback
was classified at the 10-second threshold and produced a rate-limited reminder:

```text
DIAGNOSTIC TEST callback started. sequence=3 action=blocked msgID=<redacted> ...
DIAGNOSTIC TEST simulating blocked application callback. sequence=3 duration=40s
User DeviceMessageHandler slow/blocked. region=<region> msgID=<redacted> ... elapsedSec=10
SDK read-stream processing slow/blocked; subscriber cannot consume until it returns. region=<region> ... elapsedSec=10
Read-stream diagnostic state changed. ... previous=healthy_delivery state=sdk_processing_blocked ... sdkProcessingActive=true sdkProcessingForSec=10
User DeviceMessageHandler still blocked. ... elapsedSec=25
SDK read-stream processing still blocked; subscriber cannot consume until it returns. ... elapsedSec=25
```

The callback then returned normally with both delayed-completion records:

```text
DIAGNOSTIC TEST callback returned. sequence=3 action=blocked msgID=<redacted>
User DeviceMessageHandler returned after delay. ... durationSec=40
SDK read-stream processing returned after delay. ... durationSec=40
```

Broker polling resumed (`awaiting_activity` then `broker_quiet`), and a subsequent message completed
the normal path. The final delivered-message counters were consistent:

```text
Read-stream summary. region=<active-region> ... state=healthy_delivery received=1 receivedTotal=4 dispatched=1 dispatchedTotal=4 sdkProcessingStarted=1 sdkProcessingStartedTotal=4 sdkProcessingCompleted=1 sdkProcessingCompletedTotal=4 sdkProcessingActive=false partition=1 offset=13248
```

Five echo publications received a successful HTTP response, while four messages reached the read
stream before shutdown. The fifth accepted publication was still outstanding when the test ended.

### Post-fix shutdown evidence

The lifecycle fix is manually confirmed. After `Terminating diagnostics example`, both configured
regions emitted a per-stream summary, a connection status containing the stream, and exactly one
status-logger stopped marker. No shutdown status contained `streams=[]`.

Quiet region:

```text
Read-stream summary. region=<quiet-region> stream=<read-stream> ... state=broker_quiet ...
Read-stream status. region=<quiet-region> ... streams=[<read-stream> ]
Read-stream status logger stopped. region=<quiet-region> ... reason=context canceled
```

Active region:

```text
Read-stream summary. region=<active-region> stream=<read-stream> ... receivedTotal=4 dispatchedTotal=4 sdkProcessingStartedTotal=4 sdkProcessingCompletedTotal=4 sdkProcessingActive=false ...
Read-stream status. region=<active-region> ... streams=[<read-stream> ]
Read-stream status logger stopped. region=<active-region> ... reason=context canceled
```

Shutdown results:

- Application returned to the PowerShell prompt without panic or deadlock: **Passed**.
- Final per-stream summary for both regions: **Passed**.
- Exactly one `Read-stream status logger stopped` marker per region: **Passed**.
- Empty final stream list: **Not present**.
- Shutdown while a callback was still blocked: **Not tested**; the 40-second callback returned
  before Ctrl+C.

### Non-blocking observations

The same secondary device returned HTTP 408 for five attempted queries. This remains independent of
the working device and read-stream validation.

During shutdown, the active region reported `Consumer has been closed` at both the pub/sub and App
layers. The server also returned normal WebSocket closure followed by the previously observed RSV
bits close-frame error. Neither condition prevented both final logger markers or process exit. They
remain shutdown log-hygiene/transport observations rather than failures of this lifecycle fix.

## Test 5: repeated post-fix shutdown after an extended cycle run

### Execution

Source log: `examples/read-stream-diagnostics/test1-cycle-after-fix.log`  
Transcript start: 2026-08-13 21:20:59 IST  
Transcript end: 2026-08-13 21:32:28 IST  
Approximate run duration: 11 minutes 29 seconds  
Result: **Passed for repeated multi-region final logging; full cycle output was not captured**

### Transcript limitation

PowerShell transcription did not retain the complete native executable output. Although the process
ran for approximately 11.5 minutes, the file contains SDK timestamps only from 16:02:22Z through
16:02:26Z. It therefore records the shutdown tail but cannot independently prove startup, all
message arrivals, or the complete callback sequence.

The retained tail begins with callback sequence 12 completing its 40-second blocked behavior:

```text
User DeviceMessageHandler still blocked. ... elapsedSec=40
DIAGNOSTIC TEST callback returned. sequence=12 action=blocked msgID=<redacted>
User DeviceMessageHandler returned after delay. ... durationSec=40
SDK read-stream processing returned after delay. ... durationSec=40
Terminating diagnostics example
```

Because the callback returned before termination, this run did not cover shutdown during an active
blocked callback. That scenario was subsequently completed by Test 7. No `callback released by
shutdown` marker is present in this Test 5 transcript.

### Cumulative evidence at shutdown

The final active-region summary proves that 12 messages completed the SDK processing lifecycle
during the run:

```text
Read-stream summary. region=<active-region> stream=<read-stream> state=broker_quiet receivedTotal=12 dispatchedTotal=12 sdkProcessingStartedTotal=12 sdkProcessingCompletedTotal=12 sdkProcessingActive=false ... partition=1 offset=13260
```

The final quiet-region summary confirms continuing broker polling without messages:

```text
Read-stream summary. region=<quiet-region> stream=<read-stream> state=broker_quiet receivedTotal=0 consumeItersTotal=609 ... noBrokerResponseForSec=0 disconnected=false reconnecting=false
```

### Repeated shutdown-fix evidence

Both regions again emitted their final stream evidence before exactly one stopped marker. Neither
final status used `streams=[]`.

```text
Read-stream summary. region=<quiet-region> stream=<read-stream> ...
Read-stream status. region=<quiet-region> ... streams=[<read-stream> ]
Read-stream status logger stopped. region=<quiet-region> ... reason=context canceled

Read-stream summary. region=<active-region> stream=<read-stream> ... receivedTotal=12 dispatchedTotal=12 sdkProcessingStartedTotal=12 sdkProcessingCompletedTotal=12 ...
Read-stream status. region=<active-region> ... streams=[<read-stream> ]
Read-stream status logger stopped. region=<active-region> ... reason=context canceled
```

This independently repeats the successful post-fix shutdown result from Test 4.

### Non-blocking shutdown observations

After both final status markers, shutdown again produced two `Consumer has been closed` ERROR lines
at the pub/sub and application layers. Both connections then logged normal server closure and closed.
The earlier RSV-bits WebSocket error did not appear in this transcript.

PowerShell printed `The pipeline has been stopped` after the executable returned. This is host/
transcription noise and occurred after both SDK final markers; it is not evidence of an SDK panic or
deadlock.

## Test 6: per-message logging disabled

### Execution

Source log: `examples/read-stream-diagnostics/test-logeach-disabled.log`  
Log window: 2026-08-14 09:14:10Z through 09:17:01Z  
Tested commit: `7d2bb1a8f8c96ba783c2eca2b4313b6f83aeeb3c`  
Result: **Passed**

```powershell
.\read-stream-diagnostics.exe `
  -config .\config_sample.yaml `
  -callback-mode normal `
  -status-interval 10s `
  -publish-interval 3s `
  -log-each-message=false
```

The resolved configuration confirmed that per-message logging was disabled for both regional
connections:

```text
Read-stream diagnostics config. statusLogInterval=10s logEachMessage=false ...
```

### Logging and processing evidence

| Evidence | Count/result |
| --- | ---: |
| Diagnostic configuration lines with `logEachMessage=false` | 2 |
| SDK `Read-stream message received` lines | 0 |
| App `App received read-stream message` lines | 0 |
| Callback starts | 3 |
| Callback completions | 3 |
| Periodic/final read-stream summaries | 34 |
| Successful echo publications | 3 |
| Echo publication timeouts for the other device | 3 |
| Final status-logger stopped markers | 2 |

Both individual arrival logs are controlled by the same `LogEachMessage` setting, so zero SDK and
zero App per-message lines are the expected result. Message processing was independently confirmed
by three normal callbacks starting and returning, and by the cumulative stream counters:

```text
DIAGNOSTIC TEST callback started. sequence=<n> action=normal ...
DIAGNOSTIC TEST callback returned. sequence=<n> action=normal ...
Read-stream summary. region=<active-region> ... receivedTotal=3 dispatchedTotal=3 sdkProcessingStartedTotal=3 sdkProcessingCompletedTotal=3 sdkProcessingActive=false partition=1 offset=13263
```

The other region remained connected and correctly reported `broker_quiet`. Periodic summaries
continued while per-message logs were disabled, confirming that the serviceability summaries are
independent of the optional high-volume arrival logging.

### Shutdown evidence

Both regional connections emitted a final per-stream snapshot and one status-logger stopped marker.
Shutdown returned without panic or deadlock:

```text
Read-stream status logger stopped. region=<region-1> ... reason=context canceled
Read-stream status logger stopped. region=<region-2> ... reason=context canceled
```

The recurring HTTP 408 for the secondary device and WebSocket RSV-bits close errors were observed
again. They did not prevent message processing, final diagnostic logging, or shutdown, and remain
external/non-blocking observations for this test.

## Test 7: shutdown during an actively blocked callback

### Execution

Source log: `examples/read-stream-diagnostics/test-blocked-shutdown.log`  
Log window: 2026-08-14 09:27:57Z through 09:34:36Z  
Tested commit: `7d2bb1a8f8c96ba783c2eca2b4313b6f83aeeb3c`  
Result: **Passed; the run also covered reconnect, resubscribe, and post-reconnect delivery**

The effective test configuration was:

```text
statusInterval=5s logEachMessage=true publishInterval=3s callbackMode=blocked slowDuration=12s blockDuration=5m0s
```

### Blocked callback evidence

The first message entered a deliberately blocked callback at 09:28:51Z. The 10-second warnings
appeared at both the application and SDK-processing layers, followed by regular reminders while the
callback remained blocked for its configured five minutes:

```text
DIAGNOSTIC TEST callback started. sequence=1 action=blocked ...
DIAGNOSTIC TEST simulating blocked application callback. sequence=1 duration=5m0s
User DeviceMessageHandler slow/blocked. ... elapsedSec=10
SDK read-stream processing slow/blocked; subscriber cannot consume until it returns. ... elapsedSec=10
User DeviceMessageHandler still blocked. ... elapsedSec=<n>
SDK read-stream processing still blocked; subscriber cannot consume until it returns. ... elapsedSec=<n>
Read-stream summary. ... state=sdk_processing_blocked ... sdkProcessingActive=true ...
```

The first callback returned normally after 300 seconds. After reconnect, a second message entered a
blocked callback. Shutdown began while that callback had been active for 33 seconds. Cancellation
released it before its configured duration, and both diagnostic layers recorded its completion:

```text
Terminating diagnostics example
DIAGNOSTIC TEST callback released by shutdown. sequence=2 action=blocked
DIAGNOSTIC TEST callback returned. sequence=2 action=blocked ...
User DeviceMessageHandler returned after delay. ... durationSec=33
SDK read-stream processing returned after delay. ... durationSec=33
```

Five additional messages that were queued behind the blocked callback were dispatched during
shutdown. Their callbacks observed the canceled context and returned immediately. The final
active-stream counters were consistent: seven messages were received, dispatched, started, and
completed, with `sdkProcessingActive=false`. Both regional status loggers emitted exactly one
stopped marker. The application exited without a panic, fatal error, or deadlock.

### Consume-timeout reconnect evidence

Returning from the 300-second callback crossed the consume-timeout threshold. The log contains
the complete serviceability sequence:

```text
Consume timeout. Disconnecting. ... sinceLastMsgSec=300
Consume timeout. Reconnecting. ...
Read-stream diagnostic state changed. ... previous=sdk_processing_blocked state=disconnected ...
Reconnected to PubSub server. ...
Reuse subscription ID=<redacted>
Read-stream diagnostic state changed. ... previous=disconnected state=reconnecting ...
Resubscribed after reconnect. ...
Reconnect complete. ... streams=1 durationMs=1907 reconnectCount=1
```

This passes the consume-timeout disconnect, reconnect, subscription-reuse/resubscribe, state-change,
duration, and reconnect-counter logging checks.

### Post-reconnect delivery evidence

Reconnect completed at 09:33:53Z, before shutdown. At 09:33:58Z a new read-stream message was
received and entered callback sequence 2. Additional queued messages were delivered at shutdown.
The cursor advanced from offset 13269 before reconnect to 13275 afterward, and the final counters
confirmed all seven delivered messages completed processing:

```text
Read-stream message received. ... partition=1 offset=13275 ...
DIAGNOSTIC TEST callback started. sequence=2 action=blocked ...
Read-stream summary. ... receivedTotal=7 dispatchedTotal=7 sdkProcessingStartedTotal=7 sdkProcessingCompletedTotal=7 sdkProcessingActive=false partition=1 offset=13275
```

This closes the prior post-reconnect delivery gap. Reconnect did not overlap shutdown in this
updated run.

The recurring secondary-device HTTP 408 and WebSocket RSV-bits close errors were again non-blocking
observations.

## Test 8: application read-stream gap callback

### Scope and implementation

Status: **Implemented; automated tests and the manual example passed; Linux race evidence pending.**

The opt-in `Config.ReadStreamGapHandler` receives an edge-triggered `detected` event for the
established consume timeout and a `recovered` event only after reconnect/resubscribe is ready and a
subsequent broker response is observed. Events distinguish `broker_response_timeout` from
`processing_backpressure`, include duration and reconnect count, and are dispatched serially outside
the SDK subscriber/reconnect goroutines.

The scope explicitly excludes generic no-message notifications, quiet standby-region alerts, new
timeout policy, and automatic recovery changes.

### Automated evidence

Focused scenarios passed 50 repeated runs:

```text
TestReadStreamGapTrackerEmitsDetectedAndRecoveredOnce
TestReadStreamGapTrackerWaitsForPostReconnectBrokerResponse
TestReadStreamGapTrackerDoesNotReuseBrokerResponseFromFailedAttempt
TestConsumeTimeoutReconnectEmitsCompleteDiagnosticSequence
TestProcessingBackpressureEmitsGapAndRecovery
TestReadStreamGapHandlerIsOrderedAndDoesNotBlockSDKProducer
TestReadStreamGapHandlerPanicIsContained
TestNewAppConfigPropagatesReadStreamGapHandler
```

Tests and vet also pass for every package tracked by the branch. The broad local `./...` command is
currently affected by unrelated untracked example folders that reference APIs absent from this
branch; those user-owned folders were not changed.

### Manual acceptance evidence

Source log: `examples/read-stream-diagnostics/test-gap-callback.log`  
Log window: 2026-08-14 13:06:36Z through 13:12:10Z  
Result: **Passed**

The example ran with gap notifications enabled, a 75-second blocked callback, a five-second status
interval, a two-minute publish interval, and per-message logging disabled. It completed two
independent processing-backpressure cycles:

| Evidence | Count | Result |
| --- | ---: | --- |
| `state=detected reason=processing_backpressure` | 2 | One per blocked-callback gap |
| `Consume timeout. Disconnecting.` | 2 | One per gap after the callback returned |
| `Reconnect complete.` | 2 | Reconnect counts advanced from 0 to 1 and then 1 to 2 |
| `state=recovered reason=processing_backpressure` | 2 | One per successful post-reconnect broker response |
| `Read-stream status logger stopped.` | 2 | Both regional loggers completed shutdown |

In each cycle, `detected` was delivered at the established 60-second processing threshold while the
application callback was still blocked. It preceded callback completion and the existing disconnect
log by approximately 15 seconds. `recovered` followed reconnect/resubscribe and a subsequent broker
response. There were no duplicate transition events, dropped gap notifications, or panics.

The secondary Singapore stream remained responsive and `broker_quiet` throughout the run without a
gap callback, providing negative evidence that ordinary no-message periods do not notify the
application. Three HTTP 408 echo-publish failures for the secondary device and two shutdown-time
WebSocket RSV-bit close messages were non-blocking observations; neither affected the validated
callback flow.

Linux `go test -race -v ./...` must still be rerun after this change is committed and pushed.

## Pending test result template

Copy this section for each additional run.

### Test N: description

- Date/time:
- Source log:
- Tested commit:
- Command/configuration:
- Expected behavior:
- Observed evidence:
- Result: Pending / Passed / Failed / Partially passed
- Issues or follow-up:
