package pubsub

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	pubsubtest "github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub/test"
	"github.com/stretchr/testify/require"
)

func newDiagnosticsTestConnection(t *testing.T, server *httptest.Server) *Connection {
	t.Helper()
	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	connection, err := NewConnection(Config{
		GroupID: "diagnostics-concurrency-test",
		Domain:  u.Host,
		APIKeyProvider: func() ([]byte, error) {
			return []byte("test-key"), nil
		},
		PollInterval:      5 * time.Millisecond,
		StatusLogInterval: 2 * time.Millisecond,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		}},
	})
	require.NoError(t, err)
	connectCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, connection.Connect(connectCtx))
	return connection
}

// This test exercises the only synchronization added for serviceability: the diagnostic status
// goroutine repeatedly snapshots the connection and subscriptions while public calls update them.
func TestDiagnosticSnapshotsDuringSubscriptionChanges(t *testing.T) {
	server, _ := pubsubtest.NewRPCServer(t, pubsubtest.Config{})
	defer server.Close()

	connection := newDiagnosticsTestConnection(t, server)
	defer connection.Disconnect()

	const subscriptionCount = 8
	var wg sync.WaitGroup
	reconnectSnapshotsDone := make(chan struct{})
	go func() {
		defer close(reconnectSnapshotsDone)
		for i := 0; i < subscriptionCount*4; i++ {
			// Exercise the same protected pointer/state handoff used by reconnect while status
			// collection and public subscription updates are active. The separate integration test
			// covers the network reconnect sequence.
			connection.setReconnecting(true)
			connection.setConnection(connection.connectionSnapshot())
			connection.setReconnecting(false)
		}
	}()
	errors := make(chan error, subscriptionCount)
	for i := 0; i < subscriptionCount; i++ {
		stream := fmt.Sprintf("diagnostic-stream-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := connection.Subscribe(stream, func(error, string, map[string]string, []byte) {}); err != nil {
				errors <- err
				return
			}
			if err := connection.Unsubscribe(stream); err != nil {
				errors <- err
			}
		}()
	}
	wg.Wait()
	<-reconnectSnapshotsDone
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Empty(t, connection.subscriptionSnapshot())
}

func TestDisconnectCancelsInFlightConnectAttempt(t *testing.T) {
	attemptCtx, attemptCancel := context.WithCancel(context.Background())
	connection := &Connection{attemptConnectCancel: attemptCancel}

	// Simulate Connect holding lifecycle serialization while it waits on network I/O. Disconnect
	// must cancel the attempt before waiting for lifecycleMu.
	connection.lifecycleMu.Lock()
	done := make(chan struct{})
	go func() {
		connection.Disconnect()
		close(done)
	}()
	select {
	case <-attemptCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("in-flight connection attempt was not canceled")
	}
	connection.lifecycleMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not finish after lifecycle operation completed")
	}

	activeCtx, activeCancel := context.WithCancel(context.Background())
	connection.mu.Lock()
	connection.attemptConnectCancel = nil
	connection.ctxCancel = activeCancel
	connection.mu.Unlock()
	connection.Disconnect()

	select {
	case <-activeCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("active lifecycle was not canceled")
	}
}
