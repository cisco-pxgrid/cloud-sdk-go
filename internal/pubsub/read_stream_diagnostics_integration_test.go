package pubsub

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pubsubtest "github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub/test"
	"github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"
	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"github.com/cisco-pxgrid/websocket"
	"github.com/stretchr/testify/require"
)

func startStatusLoggerHarness(t *testing.T, stats *connStats, subscriptions ...string) (*Connection, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	params := make(map[string]subscriptionParams, len(subscriptions))
	for _, stream := range subscriptions {
		params[stream] = subscriptionParams{stream: stream, subscriptionID: "sub-" + stream}
	}
	connection := &Connection{
		config: Config{
			Domain:            "diagnostics.example.com",
			GroupID:           "diagnostics-group",
			StatusLogInterval: 2 * time.Millisecond,
			stats:             stats,
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
	_, stop := startStatusLoggerHarness(t, stats, "stream-a")

	require.Eventually(t, func() bool { return captured.infoContains("state=awaiting_activity") }, time.Second, 2*time.Millisecond)
	stats.recordBrokerResponse("stream-a", "frozen-context", 0)
	require.Eventually(t, func() bool {
		return logLineContainsAll(captured.infoSnapshot(), "Read-stream summary", "state=broker_quiet", "consumeIters=1", "received=0", "cursorChanges=0")
	}, time.Second, 2*time.Millisecond)
	stop()
	require.True(t, captured.infoContains("Read-stream status logger stopped."))
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

	gapEvents := make(chan ReadStreamGapEvent, 2)
	gapTracker := NewReadStreamGapTracker(func(event ReadStreamGapEvent) {
		gapEvents <- event
	})
	connection := newDiagnosticsTestConnectionWithGapTracker(t, server, gapTracker)
	defer disconnectAndWaitForIntegrationTest(t, connection)
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

	var observedGapEvents []ReadStreamGapEvent
	require.Eventually(t, func() bool {
		for {
			select {
			case event := <-gapEvents:
				observedGapEvents = append(observedGapEvents, event)
			default:
				return len(observedGapEvents) == 2
			}
		}
	}, 3*time.Second, 5*time.Millisecond)
	require.Equal(t, ReadStreamGapDetected, observedGapEvents[0].State)
	require.Equal(t, ReadStreamGapRecovered, observedGapEvents[1].State)
	require.Equal(t, ReadStreamGapReasonBrokerResponseTimeout, observedGapEvents[0].Reason)
	require.Equal(t, observedGapEvents[0].Reason, observedGapEvents[1].Reason)
	require.Equal(t, "reconnect-stream", observedGapEvents[0].Stream)
	require.Equal(t, observedGapEvents[0].Stream, observedGapEvents[1].Stream)
	require.NotEmpty(t, observedGapEvents[0].Region)
	require.GreaterOrEqual(t, observedGapEvents[1].Duration, observedGapEvents[0].Duration)
	require.Zero(t, observedGapEvents[0].ReconnectCount)
	require.Equal(t, int64(1), observedGapEvents[1].ReconnectCount)

	beforeDisconnect := len(captured.infoSnapshot())
	connection.Disconnect()
	shutdownLogs := captured.infoSnapshot()[beforeDisconnect:]
	require.True(t, logLineContainsAll(shutdownLogs, "Read-stream status logger stopped."))
	require.True(t, logLineContainsAll(shutdownLogs,
		"Read-stream summary", "stream=reconnect-stream", "sdkProcessingActive=false"),
		"disconnect must wait for a final per-stream snapshot before stream teardown")
}

func TestProcessingBackpressureEmitsGapAndRecovery(t *testing.T) {
	originalConsumeTimeout := consumeResponseTimeout
	originalProcessingTimeout := resultProcessingTimeout
	defer func() {
		consumeResponseTimeout = originalConsumeTimeout
		resultProcessingTimeout = originalProcessingTimeout
	}()
	consumeResponseTimeout = time.Second
	resultProcessingTimeout = 20 * time.Millisecond

	var connections atomic.Int32
	server, _ := pubsubtest.NewRPCServer(t, pubsubtest.Config{
		ConnHandler: func() int {
			connections.Add(1)
			return 200
		},
		ConsumeHandler: func(_ *websocket.Conn, req *rpc.Request) *rpc.Response {
			params, err := req.ConsumeParams()
			require.NoError(t, err)
			if connections.Load() > 1 {
				return rpc.NewConsumeResponse(req.ID, "recovered-context", params.SubscriptionID, "backpressure-stream", nil)
			}
			messages := []rpc.ConsumeMessage{{
				MsgID:   "blocked-message",
				Payload: base64.StdEncoding.EncodeToString([]byte("payload")),
				Headers: map[string]string{"stream": "pxcloud--echo-echo", "messageType": "data"},
			}}
			return rpc.NewConsumeResponse(req.ID, "blocked-context", params.SubscriptionID, "backpressure-stream", messages)
		},
	})
	defer server.Close()

	gapEvents := make(chan ReadStreamGapEvent, 2)
	gapTracker := NewReadStreamGapTracker(func(event ReadStreamGapEvent) {
		gapEvents <- event
	})
	connection := newDiagnosticsTestConnectionWithGapTracker(t, server, gapTracker)
	defer disconnectAndWaitForIntegrationTest(t, connection)

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	var blockOnce sync.Once
	require.NoError(t, connection.Subscribe("backpressure-stream", func(error, string, map[string]string, []byte) {
		blockOnce.Do(func() {
			close(callbackStarted)
			<-releaseCallback
		})
	}))

	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("application callback did not start")
	}

	var detected ReadStreamGapEvent
	select {
	case detected = <-gapEvents:
	case <-time.After(3 * time.Second):
		t.Fatal("processing-backpressure gap was not emitted")
	}
	require.Equal(t, ReadStreamGapDetected, detected.State)
	require.Equal(t, ReadStreamGapReasonProcessingBackpressure, detected.Reason)
	require.Equal(t, "backpressure-stream", detected.Stream)

	close(releaseCallback)
	var recovered ReadStreamGapEvent
	select {
	case recovered = <-gapEvents:
	case <-time.After(3 * time.Second):
		t.Fatal("processing-backpressure recovery was not emitted")
	}
	require.Equal(t, ReadStreamGapRecovered, recovered.State)
	require.Equal(t, detected.Reason, recovered.Reason)
	require.GreaterOrEqual(t, connections.Load(), int32(2))
}

// disconnectAndWaitForIntegrationTest joins both connection layers before a test restores
// package-level timeouts or other shared fixtures. Connection.Disconnect intentionally does not
// expose these internal completion signals as public API, but integration tests in this package can
// wait for them and avoid leaking reconnect/close work into the next test.
func disconnectAndWaitForIntegrationTest(t *testing.T, connection *Connection) {
	t.Helper()
	internal := connection.connectionSnapshot()
	connection.Disconnect()

	select {
	case <-connection.Error:
	case <-time.After(3 * time.Second):
		t.Error("outer connection error handler did not stop")
	}

	if internal == nil {
		return
	}
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-internal.Error:
			if !ok {
				return
			}
		case <-timeout:
			t.Error("internal connection close path did not stop")
			return
		}
	}
}
