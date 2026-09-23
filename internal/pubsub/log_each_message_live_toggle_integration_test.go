package pubsub

import (
	"encoding/base64"
	"sync/atomic"
	"testing"
	"time"

	pubsubtest "github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub/test"
	"github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"
	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"github.com/cisco-pxgrid/websocket"
	"github.com/stretchr/testify/require"
)

// TestSetLogEachMessage_LiveToggleOnRealSubscription is a replica of a real client's
// connect-subscribe-receive flow (same pattern as the read-stream-diagnostics example), run
// against the mock RPC server used by the other integration tests in this package. It proves
// SetLogEachMessage flips the per-message log on/off for an already-running subscription, with no
// disconnect/reconnect in between.
func TestSetLogEachMessage_LiveToggleOnRealSubscription(t *testing.T) {
	originalLogger := log.Logger
	defer func() { log.Logger = originalLogger }()
	captured := &captureLogger{}
	log.Logger = captured

	const stream = "toggle-stream"
	var delivered atomic.Int32
	var deliverSecond atomic.Bool

	server, _ := pubsubtest.NewRPCServer(t, pubsubtest.Config{
		ConsumeHandler: func(_ *websocket.Conn, req *rpc.Request) *rpc.Response {
			params, err := req.ConsumeParams()
			require.NoError(t, err)

			switch {
			case delivered.CompareAndSwap(0, 1):
				return rpc.NewConsumeResponse(req.ID, "ctx-before", params.SubscriptionID, stream, []rpc.ConsumeMessage{{
					MsgID:   "before-toggle",
					Payload: base64.StdEncoding.EncodeToString([]byte("payload-before")),
					Headers: map[string]string{"stream": "pxcloud--" + stream, "messageType": "data"},
				}})
			case deliverSecond.Load() && delivered.CompareAndSwap(1, 2):
				return rpc.NewConsumeResponse(req.ID, "ctx-after", params.SubscriptionID, stream, []rpc.ConsumeMessage{{
					MsgID:   "after-toggle",
					Payload: base64.StdEncoding.EncodeToString([]byte("payload-after")),
					Headers: map[string]string{"stream": "pxcloud--" + stream, "messageType": "data"},
				}})
			default:
				return rpc.NewConsumeResponse(req.ID, "ctx-idle", params.SubscriptionID, stream, nil)
			}
		},
	})
	defer server.Close()

	connection := newDiagnosticsTestConnection(t, server)
	defer disconnectAndWaitForIntegrationTest(t, connection)

	received := make(chan string, 2)
	require.NoError(t, connection.Subscribe(stream, func(err error, msgID string, _ map[string]string, _ []byte) {
		require.NoError(t, err)
		received <- msgID
	}))

	// 1. Switch starts OFF: the message is still delivered to the app, but no per-message log line
	// is written.
	select {
	case msgID := <-received:
		require.Equal(t, "before-toggle", msgID)
	case <-time.After(2 * time.Second):
		t.Fatal("first message was not received")
	}
	require.False(t, captured.infoContains("before-toggle"), "no per-message log expected while the switch is off")

	// 2. Flip the switch live, on the exact same connection - no reconnect.
	connection.SetLogEachMessage(true)
	require.True(t, connection.config.logEachMessageEnabled())
	deliverSecond.Store(true)

	// 3. The next message on the same subscription is now logged.
	select {
	case msgID := <-received:
		require.Equal(t, "after-toggle", msgID)
	case <-time.After(2 * time.Second):
		t.Fatal("second message was not received")
	}
	require.Eventually(t, func() bool {
		return captured.infoContains("Read-stream message received") && captured.infoContains("after-toggle")
	}, time.Second, 5*time.Millisecond)

	// 4. Flip back off - takes effect immediately for any further traffic.
	connection.SetLogEachMessage(false)
	require.False(t, connection.config.logEachMessageEnabled())
}
