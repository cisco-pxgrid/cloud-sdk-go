package cloud

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub/test"
	"github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"
	"github.com/cisco-pxgrid/websocket"
	"github.com/stretchr/testify/require"
)

func TestReconnect(t *testing.T) {
	// Prepare test
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
