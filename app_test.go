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

func init() {
	// Speed up for testing

}

func TestReconnect(t *testing.T) {
	reconnectBackoff = 1 * time.Second
	reconnectDelay = 1 * time.Second

	var connCount atomic.Uint32
	var consumeCount int
	s, _ := test.NewRPCServer(t, test.Config{
		ConnHandler: func() int {
			connCount.Add(1)
			return http.StatusOK
		},
		ConsumeHandler: func(conn *websocket.Conn, req *rpc.Request) *rpc.Response {
			consumeCount++
			if consumeCount == 1 {
				result := `{
					"consumeContext": "ctx1",
					"subscriptionId": "sub1",
					"messages": {
						"readStream":[
							{
								"msgId": "msg1",
								"headers": {
									"messageType": "data",
									"tenant": "tenant1",
									"device": "device1",
									"key1": "val2"
								},
								"payload": "VGhpcyBpcyBhIHRlc3Q="
							}
						]
					}
				}`
				return &rpc.Response{
					Version: "1.0",
					ID:      req.ID,
					Result:  json.RawMessage(result),
				}
			} else if consumeCount == 2 {
				// close the connection to trigger reconnect
				conn.Close(websocket.StatusNormalClosure, "")
				return nil
			} else {
				result := `{
					"consumeContext": "ctx1",
					"subscriptionId": "readStream",
					"messages": {}
				}`
				return &rpc.Response{
					Version: "1.0",
					ID:      req.ID,
					Result:  json.RawMessage(result),
				}
			}
		},
	})
	defer s.Close()
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
	}
	app, err := New(config)
	require.NoError(t, err)
	tenant, err := app.LinkTenant("otp_string")
	require.NoError(t, err)
	// wait for the connection to be established
	require.Eventually(t, func() bool { return connCount.Load() == 2 }, 5*time.Second, 1*time.Second)
	_ = app.UnlinkTenant(tenant)
	app.Close()
}
