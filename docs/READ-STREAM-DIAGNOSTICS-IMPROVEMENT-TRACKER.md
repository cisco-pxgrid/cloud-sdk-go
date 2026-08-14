# Read-Stream Diagnostics and Resiliency Improvement Tracker

Last reviewed: 2026-08-14  
Applies to: `dev/read-stream-diagnostics`

## Purpose

This document tracks improvements identified during the read-stream diagnostics design review. The
immediate goal is to fix only issues that can affect correctness, recovery, or the reliability of
the diagnosis. The approved scope now also includes an opt-in application callback for confirmed
consume gaps and recovery. Logging polish, automatic gap recovery, and API simplification remain
deferred.

## Severity and scope policy

For this tracker, severity is ordered as follows:

1. **Critical** — can cause a data race, inconsistent connection state, incorrect shutdown/reconnect,
   or a wider outage than the original failure.
2. **Major** — can materially misclassify a production incident, generate false alerts, hide the
   actual failure, or leave an important failure mode untested.
3. **High** — improves production operability and prevents configuration or logging problems, but
   does not block the core correctness work.
4. **Low** — cleanup, API simplification, or presentation improvements.

### Important-only delivery scope

- **Fix now:** C-01 and approved Major items that only improve diagnostic accuracy or coverage.
- **Out of scope:** C-02 and C-03 because they change established recovery behavior.
- **Defer:** High items unless they are very small and naturally touched by Critical/Major work.
- **Do not include in the current fix:** Low items.
- **Approved scope extension:** M-06 exposes confirmed consume-gap and recovery transitions to the
  application without adding another timeout or changing reconnect behavior.

## Summary

| ID | Severity | Effort | Status | Fix now? | Improvement |
| --- | --- | --- | --- | --- | --- |
| C-01 | Critical | Medium | Complete; Linux race CI passed | Yes | Synchronize `Connection` state and subscription access |
| C-02 | Critical | Large | Closed—outside serviceability scope | No | Add one serialized, context-aware reconnect manager |
| C-03 | Critical | Large | Closed—existing behavior retained | No | Recover failed regions independently |
| M-01 | Major | Medium | Complete; manually verified | Yes | Make diagnostic metrics truthful and non-destructive |
| M-02 | Major | Large | Complete; quiet, blocked, and consumer-stalled states verified | Yes | Classify stalls and suppress false gap alerts |
| M-03 | Major | Small | Complete; reconnect lifecycle manually verified | Yes | Reset stream baselines across subscribe/reconnect lifecycle |
| M-04 | Major | Medium | Complete; callback attribution and shutdown manually verified | Yes | Separate SDK processing delay from application callback delay |
| M-05 | Major | Large | Harness complete; CI race passed; longevity pending | Yes | Add race, reconnect, stale-broker, and callback integration tests |
| M-06 | Major | Medium | Implementation and manual validation complete; race-test cleanup fixed; Linux CI rerun pending | Yes | Notify applications of confirmed consume gaps and recovery |
| H-01 | High | Small | Deferred | No | Consolidate and correct diagnostic log levels |
| H-02 | High | Small | Deferred | No | Validate diagnostic durations and unsafe configurations |
| H-03 | High | Small | Deferred | No | Clarify and cache consume-cursor information |
| H-04 | High | Medium | Deferred | No | Expose a read-only health/state snapshot |
| L-01 | Low | Small | Deferred | No | Simplify `LogEachMessage` from `*bool` to `bool` |
| L-02 | Low | Small | Deferred | No | Make stream-list logging deterministic and structured |
| L-03 | Low | Small | Deferred | No | Remove stale statistics for permanently unsubscribed streams |
| L-04 | Low | Small | Deferred | No | Include the callback watchdog in connection wait-group accounting |

Effort is relative and is intended only for sequencing: Small, Medium, or Large.

## Critical items

### C-01 — Synchronize connection state and subscriptions

**Problem:** `Connection.conn` and `Connection.subscriptions` are read and written by public methods,
the status logger, and the reconnect handler without a shared lock. This permits data races,
concurrent-map access, and operations being applied to an obsolete connection during reconnect.

**Implemented serviceability design:**

- Add a `Connection`-level mutex.
- Read or replace the internal connection pointer while holding the lock.
- Copy subscription parameters under lock for diagnostic logging and reconnect logging.
- Release the lock before all network operations.
- Preserve the existing reconnect and public-operation semantics.

**Acceptance criteria:**

- [x] The status logger cannot iterate a map while subscriptions modify it.
- [x] Connection-pointer replacement and diagnostic reads use protected snapshots.
- [x] No new lifecycle state machine or operation policy was introduced.
- [x] Focused tests pass under `go test -race` in CI for the base diagnostics lifecycle change.

### C-02 — Serialized, context-aware reconnect manager

**Problem:** reconnect is performed directly inside the error handler, uses a background context,
and makes a single connection attempt. Shutdown and multiple failure signals can compete with this
flow.

**Decision:** no change. A reconnect manager, retry loop, jitter, and new escalation policy are
resiliency behavior rather than serviceability. The temporary implementation and its tests were
reverted. The existing single consume-timeout reconnect attempt, resubscription, and app-level
escalation remain unchanged; only additional logs around that flow are retained.

### C-03 — Recover regions independently

**Problem:** when one regional connection reports a terminal error, the application closes every
regional connection. A single-region problem can therefore interrupt healthy regions.

**Decision:** no change. Coordinated app-wide recovery across configured regions is established
behavior and is intentionally retained. The temporary independent-region implementation and its
test were reverted.

## Major items

### M-01 — Truthful, non-destructive metrics

**Problem:** a consume response is counted only after it is handed to the subscriber. A blocked
callback can therefore make a responsive broker appear stalled. The status logger also resets
counters, preventing other consumers such as a watchdog or health API from using the same data.

**Implemented design:** maintain cumulative per-stream values for:

- broker responses and `lastBrokerResponseAt`;
- messages present in broker responses;
- subscriber dispatch starts;
- callback starts and completions;
- latest cursor and `lastCursorChangeAt`;
- current callback identity and start time.

The status logger should calculate interval deltas from its previous snapshot without mutating the
source counters.

**Acceptance criteria:**

- [x] Broker responses are recorded before subscriber handoff.
- [x] A blocked callback remains distinguishable from a silent broker.
- [x] Multiple observers can read metrics without resetting them.
- [x] Batch message counts represent all messages already received from the broker.

### M-02 — Explicit serviceability classification

**Problem:** any quiet connected stream eventually emits repeated gap warnings. Idle topics and
standby regions are valid states, so these warnings can be false and noisy.

**Implemented classifications:**

| State | Evidence | Default action |
| --- | --- | --- |
| Healthy delivery | Broker responses and messages/cursor progress | Periodic INFO summary |
| Broker quiet | Broker responses continue; no messages | INFO only |
| Consumer stalled | No broker response for the configured threshold while transport is connected | One transition WARN; no recovery action |
| Callback blocked | Callback remains in flight beyond its threshold | Rate-limited warning; do not mislabel as broker failure |
| Disconnected/reconnecting | Explicit lifecycle state | Connection lifecycle event |

No expected-traffic configuration or delivery-stall recovery was added. Without an explicit traffic
contract, continuing empty responses are classified as valid `broker_quiet`. This keeps M-02 within
the serviceability-only goal and preserves all existing reconnect behavior.

**Acceptance criteria:**

- [x] Quiet standby regions do not emit repeated warnings or reconnect.
- [x] Consumer stalls and callback blocks produce distinct evidence.
- [x] No delivery-stall recovery or expected-traffic behavior was introduced.
- [x] Consumer-stall warnings are transition-based; callback warnings remain rate-limited.

### M-03 — Correct stream lifecycle baselines

**Problem:** last-response/message timestamps previously survived reconnects and the original stream
initializer did not reset an existing stream. Disconnected time could therefore cause an immediate
false `consumer_stalled` classification after resubscription.

**Implemented design:**

- Distinguish cumulative totals from current-subscription timing baselines.
- Reset current baselines after successful subscribe/resubscribe.
- Pause gap calculations while disconnected or reconnecting.
- Clear current lifecycle state when permanently unsubscribed.

**Acceptance criteria:**

- [x] A newly subscribed or resubscribed stream receives a full threshold grace period.
- [x] Disconnected duration is not counted as connected message silence.
- [x] Reconnect count and cumulative totals remain available.

### M-04 — Attribute callback delays correctly

**Problem:** the pub/sub watchdog times the complete SDK subscription callback. Parsing, device
lookup, control-message handling, and the user's `DeviceMessageHandler` are currently reported under
the same "App message handler" label.

**Implemented design:**

- Measure subscriber-to-SDK dispatch separately from the user callback.
- Instrument the actual user handler invocation in the application layer.
- Formally document the fixed policy: initial warning after 10 seconds and reminders no more than
  every 15 seconds.
- Emit one initial blocked warning and rate-limit reminders.

**Acceptance criteria:**

- [x] Logs distinguish SDK processing delay from user callback delay.
- [x] Callback logs include message ID, topic, elapsed duration, and region.
- [x] A callback completion after a delay is logged exactly once.
- [x] A shutdown can complete when the example's simulated callback is released.

### M-05 — Failure-mode and concurrency test harness

**Problem:** several current tests reproduce log conditions instead of invoking the production
status/reconnect paths. The current suite does not prove concurrent safety or the complete recovery
sequence.

**Implemented automated scenarios:**

- consume responses continue with empty message batches;
- consume responses stop entirely;
- consume cursor remains frozen;
- application callback blocks;
- application connection attempts fail and later recover through the established app reconnect loop;
- subscribe/unsubscribe overlaps status collection and reconnect;
- shutdown occurs during reconnect backoff.

The earlier "one region remains healthy" scenario was removed because C-03 was explicitly closed:
coordinated app-wide regional recovery is established behavior and is outside this serviceability
change. The consume-timeout integration test retains the existing single reconnect attempt and
verifies its complete diagnostic sequence without adding retries.

**Acceptance criteria:**

- [x] Tests invoke production classification, status logging, callback instrumentation, and reconnect code rather than duplicating conditions.
- [x] `go test -race ./...` is a required CI check in an environment with CGO/race support (`.github/workflows/test.yaml`).
- [x] Every implemented Critical and Major serviceability item has a deterministic regression test.
- [ ] A 48–72 hour longevity run is completed before release tagging.

### M-06 — Application read-stream gap notification

**Scope extension:** the release now exposes an opt-in application callback in addition to logs.
This is an application-visible serviceability contract, but it does not introduce a new timeout,
classify ordinary no-message periods as failures, or change established reconnect behavior.

**Implemented design:**

- Add `Config.ReadStreamGapHandler func(ReadStreamGapEvent)`; `nil` preserves existing behavior.
- Emit one `detected` event when the established consume timeout confirms either a broker-response
  timeout or processing backpressure.
- Deduplicate repeated timeout signals until the gap recovers.
- Emit one `recovered` event only after reconnect/resubscribe is ready and a subsequent broker
  response is observed.
- Dispatch application notifications asynchronously and serially through a bounded queue so the
  callback cannot block subscription, reconnect, status logging, or shutdown code.
- Contain callback panics and treat notification delivery as best-effort operational signaling.
- Propagate the handler from a parent application to app instances.

**Explicitly not included:**

- No callback for a responsive broker returning empty message batches (`broker_quiet`).
- No expected-traffic policy or generic no-message timer.
- No automatic recovery beyond the established consume-timeout reconnect/resubscribe path.

**Acceptance criteria:**

- [x] Existing applications with a nil handler remain source/behavior compatible when using keyed configuration.
- [x] Detection and recovery are edge-triggered and ordered.
- [x] Recovery requires a post-reconnect broker response.
- [x] Broker timeout and processing-backpressure reasons are distinguishable.
- [x] A blocked or panicking application handler cannot block SDK producers.
- [x] Parent-to-app-instance handler propagation is covered.
- [x] Manual example evidence captures both `detected` and `recovered` callback events.
- [ ] Linux race CI is rerun after adding the application callback dispatcher and transition tracker.

## Test execution status

### Completed

- [x] Focused unit and integration tests and `go vet` pass.
- [x] Gap detection/recovery, reason attribution, handler ordering, non-blocking dispatch, panic
  containment, and app-instance propagation pass 50 repeated focused runs.
- [x] Tests and vet pass for every Go package tracked by the branch after the M-06 scope extension.
- [x] The first post-M-06 Linux race run exposed teardown from
  `TestProcessingBackpressureEmitsGapAndRecovery` overlapping the reconnected internal connection's
  close path. The test-only cleanup now avoids replacing the global logger and joins both connection
  layers before restoring shared timeout fixtures; the corrected test passes 50 repeated local runs.
- [x] The manual M-06 run in
  `examples/read-stream-diagnostics/test-gap-callback.log` confirms two independent
  `processing_backpressure` gaps. Each blocked callback emits exactly one `detected` event at the
  established 60-second threshold, followed by callback completion, consume-timeout disconnect,
  reconnect/resubscribe, and exactly one `recovered` event. The quiet secondary region emits no
  false gap notification, processing resumes after both recoveries, and both regional status
  loggers stop during shutdown.
- [x] The manual diagnostics run in
  `debug-logs/sdk-serviceablity-test/280726_0749PM.txt` confirms explicit non-default
  configuration, per-message arrival logging, application dispatch, truthful interval/cumulative
  metrics, cursor/partition/offset evidence, and `healthy_delivery`/`broker_quiet` states.
- [x] The same run confirms normal, slow, and blocked callbacks; separate SDK-processing and user
  callback attribution; initial and rate-limited blocked warnings; exactly one delayed-completion
  line per layer; `sdk_processing_blocked` classification; and message processing after the blocked
  callback returns.
- [x] The shutdown run in `debug-logs/sdk-serviceablity-test/280726_0800PM.txt` confirms a blocked
  callback is released by cancellation, returns normally, emits both delayed-completion diagnostics,
  and allows the example to terminate without panic or deadlock.
- [x] The reconnect run in `debug-logs/sdk-serviceablity-test/280726_0810PM.txt` confirms both
  regions transition to `disconnected`, emit the complete consume-timeout reconnect/resubscribe
  sequence, reuse their subscription IDs, increment `reconnectCount`, and resume message callbacks.
- [x] The same reconnect run confirms lifecycle baselines reset after resubscription:
  no-broker-response and cursor ages restart near zero, cumulative consume/cursor totals survive,
  disconnected time does not cause an immediate false stall, and post-reconnect states return to
  `healthy_delivery`/`broker_quiet`.
- [x] The local `examples/read-stream-stall-simulator` run, captured in
  `debug-logs/sdk-serviceablity-test/290726_consumer_stalled_simulator.txt`, connects through the
  real SDK `App`, app-instance, tenant, subscription, and TLS/WebSocket paths. It confirms one
  transition from `broker_quiet` to `consumer_stalled` after broker responses stop, then recovery to
  `broker_quiet` after responses resume, with zero consume timeouts, disconnects, reconnects, or
  blocked callbacks.

### Reference logs

| Reference log | Verified coverage |
| --- | --- |
| `debug-logs/sdk-serviceablity-test/280726_0749PM.txt` | Explicit diagnostics configuration, per-message evidence, normal/slow/blocked callbacks, blocked-state classification, and callback recovery |
| `debug-logs/sdk-serviceablity-test/280726_0800PM.txt` | Shutdown releases an active blocked callback and completes without panic or deadlock |
| `debug-logs/sdk-serviceablity-test/280726_0810PM.txt` | Disconnect, consume timeout, reconnect, resubscribe, lifecycle-baseline reset, counter continuity, and resumed delivery |
| `debug-logs/sdk-serviceablity-test/290726_consumer_stalled_simulator.txt` | Real-App local TLS/WebSocket path, one `consumer_stalled` transition, response recovery to `broker_quiet`, and no timeout/disconnect/reconnect |

### Pending

- [ ] Rerun `go test -race -v ./...` in Linux CI after M-06 is committed.
- [ ] Complete and review a 48–72 hour longevity run for false warnings, reconnect churn, counter
  consistency, panics, deadlocks, and clean shutdown.

### Non-blocking observations

- Shutdown cancellation currently records in-flight diagnostic echo publications as ERROR with
  `interrupt signal received`; this is expected cancellation noise and belongs to deferred log
  hygiene rather than callback-shutdown correctness.
- One shutdown run recorded a WebSocket close-frame error after the callback and application had
  already terminated normally. It did not block shutdown and should be correlated with the
  transport/platform if it repeats.

## High items — deferred

### H-01 — Diagnostic log hygiene

- Keep one canonical per-message line rather than logging at both pub/sub and application layers.
- Emit per-message details at DEBUG.
- Keep summaries at INFO.
- Reserve WARN for actionable state transitions.

### H-02 — Configuration validation

- Reject negative durations.
- Define safe minimum intervals to prevent accidental log storms.
- Document whether zero means default or disabled for every duration.
- Validate relationships such as callback threshold versus watchdog interval.

### H-03 — Consume-cursor handling

- Decode and cache the cursor when it is received instead of decoding it for every log line.
- Name fields `cursorPartition` and `cursorOffset`; the cursor may represent a response batch rather
  than an individual message offset.
- Move the raw base64 cursor to DEBUG or emit it only on state transitions.

### H-04 — Read-only health snapshot

Expose a thread-safe health snapshot containing regional connection state and the classified stream
state. This should reuse the cumulative metrics and must not reset counters.

## Low items — deferred

### L-01 — Simplify `LogEachMessage`

Now that disabled is the default, a plain `bool` can represent the configuration. Consider changing
the new field before release if source compatibility with development builds is not required.

### L-02 — Deterministic stream logging

Sort stream names and use structured fields instead of a map-order-dependent string with trailing
spaces.

### L-03 — Remove stale stream statistics

Delete retained last-message and cursor data when a stream is permanently unsubscribed. This is
mostly memory hygiene because the application normally has a small, fixed stream set.

### L-04 — Track watchdog goroutine completion

Add callback watchdog goroutines to explicit lifecycle accounting so connection shutdown can prove
that every child goroutine has exited.

## Recommended implementation order

1. **C-01:** retain minimal locked connection/subscription snapshots required by diagnostic logging.
2. **M-01:** improve metric observation points without changing recovery behavior.
3. **M-02 and M-03:** improve log classification and timing baselines without automatic recovery.
4. **M-04:** separate SDK and user-callback timing.
5. **M-05:** add tests alongside each preceding change, then complete race and longevity gates.

High-severity items may be included only when they are contained changes in code already being
modified. Low-severity work should not delay the Critical/Major delivery.

## Release gate

The important-only effort is complete when:

- [x] C-01 is implemented; C-02 and C-03 are closed as recovery behavior outside this release.
- [x] M-01 through M-06 implementations and deterministic regression tests are complete.
- [x] Focused unit and integration tests pass.
- [x] Tests and vet pass for all packages tracked by the branch.
- [x] Race-detector CI passes for the base diagnostics commit.
- [ ] Race-detector CI is rerun after the M-06 callback scope extension.
- [x] Manual diagnostics example confirms healthy, quiet, slow-callback, blocked-callback, and
  post-callback recovery states.
- [x] Manual diagnostics example confirms ordered, deduplicated M-06 `detected` and `recovered`
  callbacks across two independent processing-backpressure/reconnect cycles.
- [x] Manual shutdown test confirms a blocked callback is released and shutdown completes.
- [x] Manual diagnostics example confirms disconnect, consume-timeout reconnect/resubscribe,
  lifecycle grace, counter continuity, and post-reconnect recovery states.
- [x] Local real-App simulator confirms the connected-but-broker-silent `consumer_stalled` state,
  exactly one transition WARN, and recovery without reconnecting.
- [ ] Manual example evidence confirms `ReadStreamGapHandler` detected/recovered transitions.
- [ ] Longevity testing shows no repeated false warnings or reconnect churn.

## Decision log

| Date | Decision |
| --- | --- |
| 2026-07-22 | Limit the current implementation scope to Critical and Major items. |
| 2026-07-22 | Keep High and Low items visible in the tracker but defer them by default. |
| 2026-07-22 | Implement C-01 with locked state, serialized lifecycle operations, and atomic timeout signaling. |
| 2026-07-22 | Implement C-02 with one reconnect manager, typed reasons, bounded exponential backoff with jitter, and lifecycle cancellation. |
| 2026-07-22 | Implement C-03 with one app supervisor per region so healthy regions remain connected. |
| 2026-07-22 | Focused tests and vet pass; local race execution is pending because this Windows environment has no C compiler. |
| 2026-07-22 | Revert C-03 after confirming coordinated regional restart is established behavior; retain terminal-error propagation to the App supervisor. |
| 2026-07-22 | Revert C-02 after limiting the release to serviceability; retain the original reconnect attempt and escalation behavior plus diagnostic logs. |
| 2026-07-22 | Implement M-01 with broker-before-handoff observations and non-destructive cumulative snapshots. |
| 2026-07-22 | Implement M-02 as log-only state classification; quiet brokers are INFO, consumer-stall WARNs are transition-based, and recovery behavior is unchanged. |
| 2026-07-22 | Implement M-03 with lifecycle-scoped baselines, cumulative totals, and diagnostic-only reconnecting state. |
| 2026-07-22 | Implement M-04 with separate SDK-processing and user `DeviceMessageHandler` timing under a documented fixed warning policy. |
| 2026-07-22 | Implement the M-05 automated harness; focused tests/vet pass, race remains a Linux CI gate, and the 48–72 hour longevity gate remains pending. |
| 2026-07-28 | Manual diagnostics run `280726_0749PM.txt` verifies explicit configuration, per-message and summary evidence, normal/slow/blocked callback attribution, blocked-state classification, and post-callback recovery. |
| 2026-07-28 | Manual shutdown run `280726_0800PM.txt` verifies cancellation releases the blocked callback and the example exits without panic or deadlock; live fault injection, race CI, and longevity remain pending. |
| 2026-07-28 | Manual reconnect run `280726_0810PM.txt` verifies both regional consume-timeout reconnect/resubscribe sequences, subscription reuse, reconnect counters, lifecycle-baseline reset, cumulative-counter continuity, and resumed delivery; `consumer_stalled` remains pending because the outage was immediately classified as disconnected. |
| 2026-07-29 | Add and run `examples/read-stream-stall-simulator`: a local TLS/WebSocket fake cloud connected through the real SDK App path. Reference log `290726_consumer_stalled_simulator.txt` verifies exactly one `consumer_stalled` transition and recovery to `broker_quiet` with no timeout, disconnect, reconnect, or callback block. |
| 2026-08-14 | Extend scope with M-06: an opt-in, asynchronous application callback for confirmed consume gaps and recovery; retain existing timeout and reconnect behavior and exclude generic no-message alerts. |
| 2026-08-14 | Validate M-06 manually with `examples/read-stream-diagnostics/test-gap-callback.log`: two processing-backpressure detections, two reconnects, two recoveries, no quiet-region false positive, no panic/drop, and two final status-logger shutdown markers. |
| 2026-08-14 | Correct the M-06 integration-test teardown after the first Linux race run: remove its unused global logger replacement and wait for both outer reconnect-handler and internal close-path completion before restoring test fixtures. SDK runtime behavior is unchanged; Linux race rerun remains pending. |
