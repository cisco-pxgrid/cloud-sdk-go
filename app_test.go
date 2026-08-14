package cloud

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub"
	"github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub/test"
	"github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"
	"github.com/cisco-pxgrid/websocket"
	"github.com/stretchr/testify/require"
)

type appCaptureLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *appCaptureLogger) Infof(string, ...interface{})  {}
func (l *appCaptureLogger) Errorf(string, ...interface{}) {}
func (l *appCaptureLogger) Debugf(string, ...interface{}) {}
func (l *appCaptureLogger) Warnf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(format, args...))
}

func (l *appCaptureLogger) warningCount(fragment string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, line := range l.warns {
		if strings.Contains(line, fragment) {
			count++
		}
	}
	return count
}

func (l *appCaptureLogger) warningContains(fragments ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.warns {
		matches := true
		for _, fragment := range fragments {
			matches = matches && strings.Contains(line, fragment)
		}
		if matches {
			return true
		}
	}
	return false
}

func TestLogEachMessageDefaultsToDisabled(t *testing.T) {
	app := &App{}
	require.False(t, app.logEachMessage())

	enabled := true
	app.config.LogEachMessage = &enabled
	require.True(t, app.logEachMessage())

	disabled := false
	app.config.LogEachMessage = &disabled
	require.False(t, app.logEachMessage())
}

func TestReadStreamGapHandlerIsOrderedAndDoesNotBlockSDKProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	received := make(chan ReadStreamGapState, 2)
	app := &App{
		config: Config{
			RegionalFQDNs: []string{"region.example.com"},
			ReadStreamGapHandler: func(event ReadStreamGapEvent) {
				received <- event.State
				if event.State == ReadStreamGapDetected {
					close(handlerStarted)
					<-releaseHandler
				}
			},
		},
		ctx: ctx,
	}
	app.configureReadStreamGapNotifications()
	require.Len(t, app.readStreamGapTrackers, 1)

	app.enqueueReadStreamGapEvent(pubsub.ReadStreamGapEvent{
		State:  pubsub.ReadStreamGapDetected,
		Reason: pubsub.ReadStreamGapReasonBrokerResponseTimeout,
		Region: "region.example.com",
		Stream: "stream-a",
	})
	<-handlerStarted

	producerReturned := make(chan struct{})
	go func() {
		app.enqueueReadStreamGapEvent(pubsub.ReadStreamGapEvent{
			State:  pubsub.ReadStreamGapRecovered,
			Reason: pubsub.ReadStreamGapReasonBrokerResponseTimeout,
			Region: "region.example.com",
			Stream: "stream-a",
		})
		close(producerReturned)
	}()
	select {
	case <-producerReturned:
	case <-time.After(time.Second):
		t.Fatal("blocked application handler prevented the SDK producer from returning")
	}

	close(releaseHandler)
	require.Equal(t, ReadStreamGapDetected, <-received)
	require.Equal(t, ReadStreamGapRecovered, <-received)
}

func TestReadStreamGapHandlerPanicIsContained(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Uint32
	app := &App{
		config: Config{
			RegionalFQDNs: []string{"region.example.com"},
			ReadStreamGapHandler: func(ReadStreamGapEvent) {
				calls.Add(1)
				panic("test panic")
			},
		},
		ctx: ctx,
	}
	app.configureReadStreamGapNotifications()
	app.enqueueReadStreamGapEvent(pubsub.ReadStreamGapEvent{State: pubsub.ReadStreamGapDetected})
	app.enqueueReadStreamGapEvent(pubsub.ReadStreamGapEvent{State: pubsub.ReadStreamGapRecovered})
	require.Eventually(t, func() bool { return calls.Load() == 2 }, time.Second, 2*time.Millisecond)
}

func TestNewAppConfigPropagatesReadStreamGapHandler(t *testing.T) {
	called := false
	app := &App{config: Config{
		ReadStreamGapHandler: func(ReadStreamGapEvent) { called = true },
	}}

	instanceConfig := app.newAppConfig("instance-id", "instance-key")
	require.NotNil(t, instanceConfig.ReadStreamGapHandler)
	instanceConfig.ReadStreamGapHandler(ReadStreamGapEvent{})
	require.True(t, called)
}

func TestDeviceMessageHandlerDiagnosticsAttributeUserCallback(t *testing.T) {
	captured := &appCaptureLogger{}

	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	handlerIDs := make(chan string, 1)
	app := &App{config: Config{DeviceMessageHandler: func(messageID string, _ *Device, _ string, _ []byte) {
		handlerIDs <- messageID
		close(started)
		<-release
	}}}
	go func() {
		app.invokeDeviceMessageHandlerWithPolicy(captured, 10*time.Millisecond, 10*time.Millisecond,
			"us.example.com", "mid-1", "handler-mid-1", &Device{}, "pxcloud--session-sessions", []byte("payload"))
		close(returned)
	}()

	<-started
	require.Equal(t, "handler-mid-1", <-handlerIDs, "diagnostics must not alter the established callback argument")
	require.Eventually(t, func() bool {
		return captured.warningContains("User DeviceMessageHandler slow/blocked", "region=us.example.com", "msgID=mid-1", "topic=pxcloud--session-sessions")
	}, time.Second, 2*time.Millisecond)
	require.Eventually(t, func() bool {
		return captured.warningContains("User DeviceMessageHandler still blocked", "msgID=mid-1")
	}, time.Second, 2*time.Millisecond)

	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("instrumented user callback did not return after release")
	}
	require.Equal(t, 1, captured.warningCount("User DeviceMessageHandler returned after delay"))
}

func TestDeviceMessageHandlerDiagnosticsFastCallbackDoesNotStartWatchdog(t *testing.T) {
	captured := &appCaptureLogger{}

	app := &App{config: Config{DeviceMessageHandler: func(string, *Device, string, []byte) {}}}
	app.invokeDeviceMessageHandlerWithPolicy(captured, time.Second, time.Second,
		"us.example.com", "mid-fast", "handler-mid-fast", &Device{}, "pxcloud--session-sessions", []byte("payload"))

	require.False(t, captured.warningContains("mid-fast"))
}

func TestReconnect(t *testing.T) {
	// Prepare test
	originalBackoff := reconnectBackoff
	originalDelay := reconnectDelay
	defer func() {
		reconnectBackoff = originalBackoff
		reconnectDelay = originalDelay
	}()
	reconnectBackoff = 1 * time.Second
	reconnectDelay = 1 * time.Second
	message := `{
					"readStream":[
						{
							"msgId": "msg1",
							"headers": {
								"messageType": "data",
								"tenant": "tenant1",
								"device": "device1",
								"key1": "val1"
							},
							"payload": "VGhpcyBpcyBhIHRlc3Q="
						}
					]
				}`
	var messageChan = make(chan string, 1)
	var closeChan = make(chan struct{}, 1)
	var connCount atomic.Uint32
	s, _ := test.NewRPCServer(t, test.Config{
		ConnHandler: func() int {
			connCount.Add(1)
			return http.StatusOK
		},
		ConsumeHandler: func(conn *websocket.Conn, req *rpc.Request) *rpc.Response {
			var payload string
			select {
			case <-closeChan:
				conn.Close(websocket.StatusNormalClosure, "")
				return nil
			case payload = <-messageChan:
			default:
				payload = "{}"
			}
			result := `{
					"consumeContext": "ctx1",
					"subscriptionId": "sub1",
					"messages":` + payload + `}`
			return &rpc.Response{
				Version: "1.0",
				ID:      req.ID,
				Result:  json.RawMessage(result),
			}
		},
	})
	defer s.Close()

	// Create New app
	var messageCount atomic.Uint32
	u, _ := url.Parse(s.URL)
	config := Config{
		ID:            "appId",
		RegionalFQDN:  u.Host,
		GlobalFQDN:    u.Host,
		WriteStreamID: "writeStream",
		ReadStreamID:  "readStream",
		GetCredentials: func() (*Credentials, error) {
			return &Credentials{ApiKey: []byte("dummy")}, nil
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
		DeviceMessageHandler: func(messageID string, device *Device, stream string, payload []byte) {
			messageCount.Add(1)
		},
	}
	app, err := New(config)
	require.NoError(t, err)
	tenant, err := app.LinkTenant("dummy")
	require.NoError(t, err)

	// wait until devices are populated
	require.Eventually(t, func() bool {
		devices, err := tenant.GetDevices()
		require.NoError(t, err)
		return len(devices) == 1
	}, 5*time.Second, 1*time.Second)

	// Check received messages
	messageChan <- message
	require.Eventually(t, func() bool { return messageCount.Load() == 1 }, 5*time.Second, 1*time.Second)

	// Check reconnect
	require.True(t, connCount.Load() == 1)
	closeChan <- struct{}{}
	// wait for the connection to be re-established
	require.Eventually(t, func() bool { return connCount.Load() == 2 }, 5*time.Second, 1*time.Second)

	// wait until devices are populated
	require.Eventually(t, func() bool {
		devices, err := tenant.GetDevices()
		require.NoError(t, err)
		return len(devices) == 1
	}, 5*time.Second, 1*time.Second)

	// Check received messages after reconnect
	messageChan <- message
	require.Eventually(t, func() bool { return messageCount.Load() == 2 }, 5*time.Second, 1*time.Second)

	_ = app.UnlinkTenant(tenant)
	app.Close()
}

func TestAppConnectionFailureThenRecovery(t *testing.T) {
	originalBackoff := reconnectBackoff
	originalDelay := reconnectDelay
	defer func() {
		reconnectBackoff = originalBackoff
		reconnectDelay = originalDelay
	}()
	reconnectBackoff = 5 * time.Millisecond
	reconnectDelay = 5 * time.Millisecond

	var attempts atomic.Int32
	server, _ := test.NewRPCServer(t, test.Config{ConnHandler: func() int {
		if attempts.Add(1) == 1 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}})
	defer server.Close()
	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	app, err := New(Config{
		ID:            "recovery-test-app",
		RegionalFQDN:  u.Host,
		GlobalFQDN:    u.Host,
		ReadStreamID:  "recovery-read-stream",
		WriteStreamID: "recovery-write-stream",
		ApiKey:        "test-key",
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		}},
	})
	require.NoError(t, err)
	app.startPubsubConnect()
	defer app.Close()

	require.Eventually(t, func() bool { return attempts.Load() >= 2 }, 3*time.Second, 5*time.Millisecond)
}

func TestAppShutdownInterruptsReconnectBackoff(t *testing.T) {
	originalBackoff := reconnectBackoff
	originalDelay := reconnectDelay
	defer func() {
		reconnectBackoff = originalBackoff
		reconnectDelay = originalDelay
	}()
	reconnectBackoff = time.Hour
	reconnectDelay = time.Hour

	var attempts atomic.Int32
	server, _ := test.NewRPCServer(t, test.Config{ConnHandler: func() int {
		attempts.Add(1)
		return http.StatusServiceUnavailable
	}})
	defer server.Close()
	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	app, err := New(Config{
		ID:            "shutdown-backoff-test-app",
		RegionalFQDN:  u.Host,
		GlobalFQDN:    u.Host,
		ReadStreamID:  "shutdown-read-stream",
		WriteStreamID: "shutdown-write-stream",
		ApiKey:        "test-key",
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		}},
	})
	require.NoError(t, err)
	app.startPubsubConnect()
	require.Eventually(t, func() bool { return attempts.Load() >= 1 }, time.Second, 5*time.Millisecond)

	closed := make(chan struct{})
	go func() {
		_ = app.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("App.Close did not interrupt reconnect backoff")
	}
}
