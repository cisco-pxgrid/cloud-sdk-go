package pubsub

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pubsubtest "github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub/test"
	"github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"
	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"github.com/cisco-pxgrid/websocket"
	"github.com/stretchr/testify/require"
)

func startStatusLoggerHarness(t *testing.T, stats *connStats, gapThreshold time.Duration, subscriptions ...string) (*Connection, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	params := make(map[string]subscriptionParams, len(subscriptions))
	for _, stream := range subscriptions {
		params[stream] = subscriptionParams{stream: stream, subscriptionID: "sub-" + stream}
	}
	connection := &Connection{
		config: Config{
			Domain:              "diagnostics.example.com",
			GroupID:             "diagnostics-group",
			StatusLogInterval:   2 * time.Millisecond,
			MessageGapThreshold: gapThreshold,
			stats:               stats,
		},
		conn:          &internalConnection{closed: make(chan struct{})},
		subscriptions: params,
	}
	done := make(chan struct{})
	go func() {
		connection.statusLogger(ctx)
		close(done)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("status logger did not stop")
		}
	}
	return connection, stop
}

func logLineContainsAll(lines []string, fragments ...string) bool {
	for _, line := range lines {
		match := true
		for _, fragment := range fragments {
			if !strings.Contains(line, fragment) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestStatusLoggerClassifiesEmptyFrozenCursorAsBrokerQuiet(t *testing.T) {
	originalLogger := log.Logger
	defer func() { log.Logger = originalLogger }()
	captured := &captureLogger{}
	log.Logger = captured

	stats := newConnStats()
	stats.beginStreamLifecycle("stream-a")
	stats.recordBrokerResponse("stream-a", "frozen-context", 0)
	_, stop := startStatusLoggerHarness(t, stats, 100*time.Millisecond, "stream-a")
	defer stop()

	require.Eventually(t, func() bool { return captured.infoContains("state=awaiting_activity") }, time.Second, 2*time.Millisecond)
	stats.recordBrokerResponse("stream-a", "frozen-context", 0)
	require.Eventually(t, func() bool {
		return logLineContainsAll(captured.infoSnapshot(), "Read-stream summary", "state=broker_quiet", "consumeIters=1", "received=0", "cursorChanges=0")
	}, time.Second, 2*time.Millisecond)
	require.False(t, captured.warnContains("state=consumer_stalled"), "valid empty responses must not warn")
}

func TestStatusLoggerWarnsOnceWhenBrokerResponsesStop(t *testing.T) {
	originalLogger := log.Logger
	defer func() { log.Logger = originalLogger }()
	captured := &captureLogger{}
	log.Logger = captured

	stats := newConnStats()
	stats.beginStreamLifecycle("stream-a")
	stats.mu.Lock()
	stats.streams["stream-a"].subscribedAt = time.Now().Add(-time.Minute)
	stats.mu.Unlock()
	_, stop := startStatusLoggerHarness(t, stats, 10*time.Millisecond, "stream-a")
	defer stop()

	require.Eventually(t, func() bool { return captured.warnContains("state=consumer_stalled") }, time.Second, 2*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, 1, len(captured.warnSnapshot()), "unchanged stalled state must not warn every interval")
}

func TestConsumeTimeoutReconnectEmitsCompleteDiagnosticSequence(t *testing.T) {
	originalLogger := log.Logger
	originalConsumeTimeout := consumeResponseTimeout
	originalProcessingTimeout := resultProcessingTimeout
	defer func() {
		log.Logger = originalLogger
		consumeResponseTimeout = originalConsumeTimeout
		resultProcessingTimeout = originalProcessingTimeout
	}()
	captured := &captureLogger{}
	log.Logger = captured
	consumeResponseTimeout = 20 * time.Millisecond
	resultProcessingTimeout = 50 * time.Millisecond

	var connections atomic.Int32
	server, _ := pubsubtest.NewRPCServer(t, pubsubtest.Config{
		ConnHandler: func() int {
			connections.Add(1)
			return 200
		},
		ConsumeHandler: func(_ *websocket.Conn, req *rpc.Request) *rpc.Response {
			if connections.Load() == 1 {
				return nil
			}
			params, err := req.ConsumeParams()
			require.NoError(t, err)
			return rpc.NewConsumeResponse(req.ID, "stable-context", params.SubscriptionID, "reconnect-stream", nil)
		},
	})
	defer server.Close()

	connection := newDiagnosticsTestConnection(t, server)
	defer connection.Disconnect()
	require.NoError(t, connection.Subscribe("reconnect-stream", func(error, string, map[string]string, []byte) {}))

	require.Eventually(t, func() bool {
		return captured.eventsInOrder(
			"Consume timeout. Disconnecting",
			"Consume timeout. Reconnecting",
			"Reconnected to PubSub server",
			"Resubscribed after reconnect",
			"Reconnect complete",
		)
	}, 3*time.Second, 5*time.Millisecond)
	require.GreaterOrEqual(t, connections.Load(), int32(2))
}
