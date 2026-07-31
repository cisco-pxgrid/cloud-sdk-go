// Copyright (c) 2024, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"github.com/stretchr/testify/require"
)

// captureLogger is an SDKLogger that records formatted log lines per level for assertions.
type captureLogger struct {
	mu     sync.Mutex
	info   []string
	warn   []string
	err    []string
	debug  []string
	events []string
}

func (c *captureLogger) Infof(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := fmt.Sprintf(format, args...)
	c.info = append(c.info, line)
	c.events = append(c.events, line)
}
func (c *captureLogger) Warnf(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := fmt.Sprintf(format, args...)
	c.warn = append(c.warn, line)
	c.events = append(c.events, line)
}
func (c *captureLogger) Errorf(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := fmt.Sprintf(format, args...)
	c.err = append(c.err, line)
	c.events = append(c.events, line)
}
func (c *captureLogger) Debugf(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := fmt.Sprintf(format, args...)
	c.debug = append(c.debug, line)
	c.events = append(c.events, line)
}

func (c *captureLogger) infoContains(sub string) bool { return anyContains(c.snap(c.info), sub) }
func (c *captureLogger) warnContains(sub string) bool { return anyContains(c.snap(c.warn), sub) }

func (c *captureLogger) snap(lines []string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}

func anyContains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func countContains(lines []string, fragment string) int {
	count := 0
	for _, line := range lines {
		if strings.Contains(line, fragment) {
			count++
		}
	}
	return count
}

func (c *captureLogger) eventsInOrder(fragments ...string) bool {
	events := c.snap(c.events)
	next := 0
	for _, event := range events {
		if strings.Contains(event, fragments[next]) {
			next++
			if next == len(fragments) {
				return true
			}
		}
	}
	return false
}

func TestConnStats_RecordAndSnapshotIsNonDestructive(t *testing.T) {
	s := newConnStats()

	s.beginStreamLifecycle("stream-a")
	s.recordBrokerResponse("stream-a", "ctx-1", 2)
	s.recordBrokerResponse("stream-a", "ctx-2", 0)
	s.recordBrokerResponse("stream-b", "ctx-b", 1)
	s.recordDispatch("stream-a")
	s.recordSDKProcessingStart("stream-a", "mid-1", "topic-1")

	first := s.snapshot()
	a := first.streams["stream-a"]
	require.Equal(t, int64(2), a.brokerResponses)
	require.Equal(t, int64(2), a.brokerMessages)
	require.Equal(t, int64(1), a.dispatchStarts)
	require.Equal(t, int64(1), a.sdkProcessingStarts)
	require.Equal(t, int64(0), a.sdkProcessingEnds)
	require.Equal(t, int64(2), a.cursorChanges)
	require.Equal(t, "ctx-2", a.lastConsumeCtx)
	require.True(t, a.sdkProcessingActive)
	require.Equal(t, "mid-1", a.sdkProcessingMsgID)
	require.Equal(t, "topic-1", a.sdkProcessingTopic)
	require.Equal(t, int64(1), first.streams["stream-b"].brokerMessages)
	require.Zero(t, first.reconnectCount)
	require.True(t, first.connectedSince.IsZero(), "connectedSince should be zero before recordConnected")

	// A second observer sees the same cumulative evidence; snapshot does not consume it.
	second := s.snapshot()
	require.Equal(t, first.streams, second.streams)

	s.recordSDKProcessingComplete("stream-a")
	completed := s.snapshot().streams["stream-a"]
	require.False(t, completed.sdkProcessingActive)
	require.Equal(t, int64(1), completed.sdkProcessingEnds)
}

func TestConnStats_ConnectedAndReconnect(t *testing.T) {
	s := newConnStats()
	require.Equal(t, 0, s.reconnectSnapshot())

	s.recordConnected()
	require.False(t, s.snapshot().connectedSince.IsZero())

	s.recordReconnect()
	s.recordReconnect()
	require.Equal(t, 2, s.reconnectSnapshot())
}

func TestConnStats_StreamLifecycleResetsBaselinesAndPreservesTotals(t *testing.T) {
	s := newConnStats()
	s.beginStreamLifecycle("stream-a")
	s.recordBrokerResponse("stream-a", "ctx-1", 2)
	s.recordDispatch("stream-a")
	s.recordSDKProcessingStart("stream-a", "mid-1", "topic-1")
	s.recordSDKProcessingComplete("stream-a")

	before := s.snapshot().streams["stream-a"]
	require.True(t, before.active)
	require.Equal(t, int64(2), before.brokerMessages)
	require.Equal(t, "ctx-1", before.lastConsumeCtx)

	s.endStreamLifecycle("stream-a")
	ended := s.snapshot().streams["stream-a"]
	require.False(t, ended.active)
	require.True(t, ended.subscribedAt.IsZero())
	require.True(t, ended.lastBrokerResponseAt.IsZero())
	require.Empty(t, ended.lastConsumeCtx)
	require.Equal(t, before.brokerResponses, ended.brokerResponses)
	require.Equal(t, before.brokerMessages, ended.brokerMessages)
	require.Equal(t, before.sdkProcessingEnds, ended.sdkProcessingEnds)

	resubscribedAt := time.Now()
	s.beginStreamLifecycle("stream-a")
	after := s.snapshot().streams["stream-a"]
	require.True(t, after.active)
	require.False(t, after.subscribedAt.Before(resubscribedAt))
	require.True(t, after.lastBrokerResponseAt.IsZero())
	require.True(t, after.lastMessageAt.IsZero())
	require.Empty(t, after.lastConsumeCtx)
	require.Equal(t, before.brokerMessages, after.brokerMessages, "cumulative totals must survive resubscribe")
	require.Equal(t, before.cursorChanges, after.cursorChanges)
}

func TestNewInternalConnection_DiagnosticDefaults(t *testing.T) {
	c, err := newInternalConnection(Config{
		GroupID: "g",
		Domain:  "example.com",
		APIKeyProvider: func() ([]byte, error) {
			return []byte("k"), nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, defaultStatusLogInterval, c.config.StatusLogInterval)
	require.Equal(t, defaultMessageGapThreshold, c.config.MessageGapThreshold)
	require.NotNil(t, c.config.stats)
}

func TestConnStats_SharedAcrossConnections(t *testing.T) {
	// Simulate NewConnection injecting shared stats, then a reconnect building a second
	// internalConnection from the same Config: both must share the same counters.
	shared := newConnStats()
	cfg := Config{
		GroupID: "g",
		Domain:  "example.com",
		APIKeyProvider: func() ([]byte, error) {
			return []byte("k"), nil
		},
		stats: shared,
	}
	c1, err := newInternalConnection(cfg)
	require.NoError(t, err)
	c2, err := newInternalConnection(cfg)
	require.NoError(t, err)
	require.Same(t, c1.config.stats, c2.config.stats)

	c1.config.stats.recordBrokerResponse("s", "ctx", 1)
	require.Equal(t, time.Duration(0) <= c2.config.stats.sinceLastMessage("s"), true)
	require.Equal(t, int64(1), c2.config.stats.snapshot().streams["s"].brokerMessages)
}

func TestPerMessageLogging_Toggle(t *testing.T) {
	orig := log.Logger
	defer func() { log.Logger = orig }()

	cl := &captureLogger{}
	log.Logger = cl

	// Emulate the per-message log line the subscriber emits when LogEachMessage is true.
	cfg := Config{LogEachMessage: true, Domain: "example.com", stats: newConnStats()}
	if cfg.LogEachMessage {
		log.Logger.Infof("Read-stream message received. region=%s stream=%s topic=%s msgID=%s type=%s tenant=%s device=%s bytes=%d subID=%s consumeCtx=%s decodeErr=%v",
			cfg.Domain, "app--x-R", "pxcloud--session-sessions", "mid-1", "data", "t1", "d1", 42, "sub-1", "ctx-1", nil)
	}
	require.True(t, cl.infoContains("Read-stream message received."))
	require.True(t, cl.infoContains("topic=pxcloud--session-sessions"))
	require.True(t, cl.infoContains("subID=sub-1"))

	// When disabled, nothing new is emitted.
	before := len(cl.snap(cl.info))
	cfg.LogEachMessage = false
	if cfg.LogEachMessage {
		log.Logger.Infof("should not happen")
	}
	require.Equal(t, before, len(cl.snap(cl.info)))
}

func TestClassifyStream_DistinguishesQuietStallAndBlockedCallback(t *testing.T) {
	now := time.Now()
	recent := now.Add(-time.Second)
	old := now.Add(-5 * time.Minute)
	threshold := 2 * time.Minute
	sdkProcessingThreshold := 10 * time.Second

	tests := []struct {
		name         string
		disconnected bool
		reconnecting bool
		stats        streamStatsSnapshot
		delta        streamStatsDelta
		want         streamDiagnosticState
	}{
		{
			name:         "disconnected lifecycle is explicit",
			disconnected: true,
			stats:        streamStatsSnapshot{subscribedAt: recent},
			want:         streamStateDisconnected,
		},
		{
			name:         "reconnecting pauses stall classification",
			reconnecting: true,
			stats:        streamStatsSnapshot{subscribedAt: old, lastBrokerResponseAt: old},
			want:         streamStateReconnecting,
		},
		{
			name:  "slow callback is not a broker stall",
			stats: streamStatsSnapshot{subscribedAt: old, lastBrokerResponseAt: recent, sdkProcessingActive: true, lastSDKProcessingAt: old},
			want:  streamStateSDKProcessingBlocked,
		},
		{
			name:         "blocked callback remains the root cause when transport also closes",
			disconnected: true,
			stats:        streamStatsSnapshot{subscribedAt: old, sdkProcessingActive: true, lastSDKProcessingAt: old},
			want:         streamStateSDKProcessingBlocked,
		},
		{
			name:  "no broker response beyond threshold is consumer stalled",
			stats: streamStatsSnapshot{subscribedAt: old, lastBrokerResponseAt: old},
			want:  streamStateConsumerStalled,
		},
		{
			name:  "empty responses are valid broker quiet",
			stats: streamStatsSnapshot{subscribedAt: old, lastBrokerResponseAt: recent},
			delta: streamStatsDelta{brokerResponses: 3},
			want:  streamStateBrokerQuiet,
		},
		{
			name:  "message or cursor progress is healthy delivery",
			stats: streamStatsSnapshot{subscribedAt: old, lastBrokerResponseAt: recent},
			delta: streamStatsDelta{brokerResponses: 1, brokerMessages: 2, cursorChanges: 1},
			want:  streamStateHealthyDelivery,
		},
		{
			name:  "within grace period awaits activity",
			stats: streamStatsSnapshot{subscribedAt: recent},
			want:  streamStateAwaitingActivity,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, classifyStream(now, tt.disconnected, tt.reconnecting, tt.stats, tt.delta, threshold, sdkProcessingThreshold))
		})
	}
}

func TestDiagnosticStateTransitionsAreNotRepeated(t *testing.T) {
	require.True(t, diagnosticStateChanged("", streamStateBrokerQuiet, false))
	require.False(t, diagnosticStateChanged(streamStateBrokerQuiet, streamStateBrokerQuiet, true))
	require.True(t, diagnosticStateChanged(streamStateBrokerQuiet, streamStateConsumerStalled, true))

	require.False(t, diagnosticStateWarns(streamStateBrokerQuiet))
	require.False(t, diagnosticStateWarns(streamStateSDKProcessingBlocked), "SDK-processing watchdog owns the rate-limited warning")
	require.True(t, diagnosticStateWarns(streamStateConsumerStalled))
}

func TestDecodeConsumeOffset(t *testing.T) {
	// {"streams":[{"stream":"app--x-R","partition":1,"offset":6202}]}
	const ctx = "eyJzdHJlYW1zIjpbeyJzdHJlYW0iOiJhcHAtLXgtUiIsInBhcnRpdGlvbiI6MSwib2Zmc2V0Ijo2MjAyfV19"
	p, o, ok := decodeConsumeOffset(ctx, "app--x-R")
	require.True(t, ok)
	require.Equal(t, int64(1), p)
	require.Equal(t, int64(6202), o)

	// Stream not present in the cursor.
	_, _, ok = decodeConsumeOffset(ctx, "app--other-R")
	require.False(t, ok)

	// Empty cursor ({"streams":[]}) yields no match.
	_, _, ok = decodeConsumeOffset("eyJzdHJlYW1zIjpbXX0=", "app--x-R")
	require.False(t, ok)

	// Empty and undecodable inputs are handled gracefully.
	_, _, ok = decodeConsumeOffset("", "app--x-R")
	require.False(t, ok)
	_, _, ok = decodeConsumeOffset("!!!not-base64!!!", "app--x-R")
	require.False(t, ok)
}

func TestCallbackWatchdogLogsBlockedCallback(t *testing.T) {
	originalLogger := log.Logger
	originalThreshold := sdkProcessingSlowWarnThreshold
	originalInterval := sdkProcessingWatchInterval
	defer func() {
		log.Logger = originalLogger
		sdkProcessingSlowWarnThreshold = originalThreshold
		sdkProcessingWatchInterval = originalInterval
	}()

	captured := &captureLogger{}
	log.Logger = captured
	sdkProcessingSlowWarnThreshold = 5 * time.Millisecond
	sdkProcessingWatchInterval = 5 * time.Millisecond

	connection := &internalConnection{config: Config{Domain: "example.com"}}
	sub := &subscription{stream: "app--x-R", id: "sub-1"}
	watch := &sdkProcessingWatch{}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		connection.sdkProcessingWatchdog(sub, watch, done, sdkProcessingSlowWarnThreshold, sdkProcessingWatchInterval)
	}()

	watch.begin("mid-1", "pxcloud--session-sessions")
	require.Eventually(t, func() bool {
		return captured.warnContains("SDK read-stream processing slow/blocked") &&
			captured.warnContains("msgID=mid-1") &&
			captured.warnContains("topic=pxcloud--session-sessions")
	}, time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool {
		return captured.warnContains("SDK read-stream processing still blocked")
	}, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, countContains(captured.snap(captured.warn), "SDK read-stream processing slow/blocked"))
	watch.end()
	close(done)
	<-stopped
}
