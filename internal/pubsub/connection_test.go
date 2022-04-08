package pubsub

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_E2E(t *testing.T) {
	s := test.NewRPCServer(t, test.Config{
		PubSubPath:        apiPaths.pubsub,
		SubscriptionsPath: apiPaths.subscriptions,
	})
	defer s.Close()

	u, _ := url.Parse(s.URL)

	c, err := NewInternalConnection(Config{
		GroupID: "test-client",
		Domain:  u.Host,
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NotNil(t, c)

	c.restClient.SetTLSClientConfig(&tls.Config{
		InsecureSkipVerify: true, // no verification for test server
	})

	err = c.Connect(context.Background())
	require.NoError(t, err)

	// 5 subscriptions
	numSubs := 5
	receivedMsgs := make(map[string]int, numSubs)
	receivedMu := sync.Mutex{}
	for i := 0; i < numSubs; i++ {
		stream := fmt.Sprintf("test-stream-%d", i)
		receivedMu.Lock()
		receivedMsgs[stream] = 0
		receivedMu.Unlock()
		_, err = c.Subscribe(stream, "",
			func(e error, id string, _ map[string]string, payload []byte) {
				t.Logf("Received message: %s, payload: %s", id, payload)
				receivedMu.Lock()
				receivedMsgs[stream]++
				receivedMu.Unlock()
				assert.NoError(t, e)
			})
		assert.NoError(t, err)
	}

	// publish to 5 streams simultaneously
	var wg sync.WaitGroup
	numMessages := 100
	for i := 0; i < numSubs; i++ {
		stream := fmt.Sprintf("test-stream-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < numMessages; i++ {
				payload := []byte("This is a test message on " + stream + ": " + strconv.Itoa(i))
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				r, err := c.Publish(ctx, stream, nil, payload)
				cancel()
				assert.NoError(t, err)
				t.Logf("Published message: %s to stream: %s, payload: %s", r.ID, stream, payload)
			}
		}()
	}
	wg.Wait()

	// wait for sever to respond to all consume requests
	time.Sleep(1 * time.Second)

	// verify
	for i := 0; i < numSubs; i++ {
		stream := fmt.Sprintf("test-stream-%d", i)
		receivedMu.Lock()
		assert.Equal(t, numMessages, receivedMsgs[stream], "Did not receive all the mssages from stream", i)
		receivedMu.Unlock()
	}

	c.Disconnect()
	assert.Equal(t, true, c.IsDisconnected(), "Connection is still connected")

	t.Logf("subs table: %#v", c.subs.table)
	assert.Zero(t, len(c.subs.table))

	select {
	case err = <-c.Error:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatalf("Timed out waiting for the connection to close")
	}
}

func Test_ConnectionError(t *testing.T) {
	s := test.NewRPCServer(t, test.Config{
		PubSubPath:        apiPaths.pubsub,
		SubscriptionsPath: apiPaths.subscriptions,
		RejectConn:        true,
	})
	defer s.Close()

	u, _ := url.Parse(s.URL)

	c, err := NewInternalConnection(Config{
		GroupID: "test-client",
		Domain:  u.Host,
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NotNil(t, c)

	c.restClient.SetTLSClientConfig(&tls.Config{
		InsecureSkipVerify: true, // no verification for test server
	})

	err = c.Connect(context.Background())
	assert.Error(t, err, "Did not receive expected error")
}

func Test_ConnectionMissingAppName(t *testing.T) {
	c, err := NewInternalConnection(Config{
		Domain: "example.com", // doesn't matter for this case
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
	})
	assert.Error(t, err, "Did not receive expected error")
	assert.Equal(t, "Config must contain GroupID", err.Error())
	assert.Nil(t, c, "Connection is not nil")
}

func Test_ConnectionMissingDomain(t *testing.T) {
	c, err := NewInternalConnection(Config{
		GroupID: "test-app",
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
	})
	assert.Error(t, err, "Did not receive expected error")
	assert.Equal(t, "Config must contain Domain", err.Error())
	assert.Nil(t, c, "Connection is not nil")
}

func Test_ConnectionMissingAuthProvider(t *testing.T) {
	c, err := NewInternalConnection(Config{
		GroupID: "test-app",
		Domain:  "example.com", // doesn't matter for this case
	})
	assert.Error(t, err, "Did not receive expected error")
	assert.Equal(t, "Config must contain either APIKeyProvider or AuthTokenProvider", err.Error())
	assert.Nil(t, c, "Connection is not nil")
}

func Test_AuthProviders(t *testing.T) {
	t.Run("ApiKeyTest", func(t *testing.T) {
		c, err := NewInternalConnection(Config{
			GroupID: "test-app",
			Domain:  "example.com", // doesn't matter for this case
			APIKeyProvider: func() ([]byte, error) {
				return []byte("xyz"), nil
			},
		})

		require.NoError(t, err)
		require.NotNil(t, c)

		require.Equal(t, "X-Api-Key", c.authHeader.key)
		authHeader, _ := c.authHeader.provider()
		require.Equal(t, []byte("xyz"), authHeader)
	})

	t.Run("AuthTokenTest", func(t *testing.T) {
		c, err := NewInternalConnection(Config{
			GroupID: "test-app",
			Domain:  "example.com", // doesn't matter for this case
			AuthTokenProvider: func() ([]byte, error) {
				return []byte("abc"), nil
			},
		})
		require.NoError(t, err)
		require.NotNil(t, c)

		require.Equal(t, "X-Auth-Token", c.authHeader.key)
		authHeader, _ := c.authHeader.provider()
		require.Equal(t, []byte("abc"), authHeader)
	})
}

func Test_ConnectAlreadyConnected(t *testing.T) {
	s := test.NewRPCServer(t, test.Config{
		PubSubPath:        apiPaths.pubsub,
		SubscriptionsPath: apiPaths.subscriptions,
	})
	defer s.Close()

	u, _ := url.Parse(s.URL)

	c, err := NewInternalConnection(Config{
		GroupID: "test-client",
		Domain:  u.Host,
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NotNil(t, c)

	c.restClient.SetTLSClientConfig(&tls.Config{
		InsecureSkipVerify: true, // no verification for test server
	})

	err = c.Connect(context.Background())
	require.NoError(t, err)
	defer c.Disconnect()

	err = c.Connect(context.Background())
	require.Error(t, err, "Did not receive expected error")
}

func Test_ConnectAuthTokenError(t *testing.T) {
	c, err := NewInternalConnection(Config{
		GroupID: "test-client",
		Domain:  "example.com",
		APIKeyProvider: func() ([]byte, error) {
			return nil, fmt.Errorf("Error")
		},
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NotNil(t, c)

	err = c.Connect(context.Background())
	require.Error(t, err, "Did not receive expected error")
}

func Test_ConsumeError(t *testing.T) {
	s := test.NewRPCServer(t, test.Config{
		PubSubPath:        apiPaths.pubsub,
		SubscriptionsPath: apiPaths.subscriptions,
		ConsumeError:      true,
	})
	defer s.Close()

	u, _ := url.Parse(s.URL)

	c, err := NewInternalConnection(Config{
		GroupID: "test-client",
		Domain:  u.Host,
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, c)

	c.restClient.SetTLSClientConfig(&tls.Config{
		InsecureSkipVerify: true, // no verification for test server
	})

	err = c.Connect(context.Background())
	require.NoError(t, err)

	count := 0
	_, err = c.Subscribe("test-stream", "",
		func(e error, _ string, _ map[string]string, _ []byte) {
			t.Logf("Got error: %v", e)
			assert.Error(t, e)
			if e == nil {
				count++
			}
		})
	assert.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, err = c.Publish(ctx, "test-stream", nil, []byte("test payload"))
	assert.NoError(t, err)
	time.Sleep(2 * time.Second)

	c.Disconnect()

	assert.True(t, c.IsDisconnected())
	assert.Zero(t, len(c.subs.table))
	assert.Zero(t, count)
}

func Test_PublishError1(t *testing.T) {
	c, err := NewInternalConnection(Config{
		GroupID: "test-client",
		Domain:  "example.com",
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, err = c.Publish(ctx, "test-stream", nil, []byte("test payload"))
	assert.Error(t, err, "Did not receive expected error")
}

func Test_PublishError2(t *testing.T) {
	s := test.NewRPCServer(t, test.Config{
		PubSubPath:        apiPaths.pubsub,
		SubscriptionsPath: apiPaths.subscriptions,
		PublishError:      true,
	})
	defer s.Close()

	u, _ := url.Parse(s.URL)

	c, err := NewInternalConnection(Config{
		GroupID: "test-client",
		Domain:  u.Host,
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, c)

	c.restClient.SetTLSClientConfig(&tls.Config{
		InsecureSkipVerify: true, // no verification for test server
	})

	err = c.Connect(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := c.Publish(ctx, "test-stream", nil, []byte("test payload"))
	require.NoError(t, err)
	require.Error(t, r.Error)

	c.Disconnect()

	assert.True(t, c.IsDisconnected())
	assert.Zero(t, len(c.subs.table))
}

func Test_PublishAsync(t *testing.T) {
	s := test.NewRPCServer(t, test.Config{
		PubSubPath:        apiPaths.pubsub,
		SubscriptionsPath: apiPaths.subscriptions,
	})
	defer s.Close()

	u, _ := url.Parse(s.URL)

	c, err := NewInternalConnection(Config{
		GroupID: "test-client",
		Domain:  u.Host,
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, c)

	c.restClient.SetTLSClientConfig(&tls.Config{
		InsecureSkipVerify: true, // no verification for test server
	})

	err = c.Connect(context.Background())
	require.NoError(t, err)

	count := 0
	_, err = c.Subscribe("test-stream", "",
		func(e error, id string, _ map[string]string, payload []byte) {
			assert.NoError(t, e)
			t.Logf("Received message %s: %s", id, payload)
			count++
		})
	assert.NoError(t, err)

	ack := make(chan *PublishResult)
	id, cancel, err := c.PublishAsync("test-stream", nil, []byte("test payload"), ack)
	require.NoError(t, err)

	select {
	case r := <-ack:
		require.Equal(t, id, r.ID)
		require.NoError(t, r.Error)
	case <-time.After(time.Second):
		assert.FailNow(t, "timed out")
	}
	cancel()

	c.Disconnect()

	assert.True(t, c.IsDisconnected())
	assert.Zero(t, len(c.subs.table))
	assert.Equal(t, 1, count)
}

func Test_PublishAsyncCanceled(t *testing.T) {
	s := test.NewRPCServer(t, test.Config{
		PubSubPath:        apiPaths.pubsub,
		SubscriptionsPath: apiPaths.subscriptions,
	})
	defer s.Close()

	u, _ := url.Parse(s.URL)

	c, err := NewInternalConnection(Config{
		GroupID: "test-client",
		Domain:  u.Host,
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, c)

	c.restClient.SetTLSClientConfig(&tls.Config{
		InsecureSkipVerify: true, // no verification for test server
	})

	err = c.Connect(context.Background())
	require.NoError(t, err)

	count := 0
	_, err = c.Subscribe("test-stream", "",
		func(e error, id string, _ map[string]string, payload []byte) {
			assert.NoError(t, e)
			t.Logf("Received message %s: %s", id, payload)
			count++
		})
	assert.NoError(t, err)

	ack := make(chan *PublishResult)
	_, cancel, err := c.PublishAsync("test-stream", nil, []byte("test payload"), ack)
	require.NoError(t, err)
	cancel()

	select {
	case <-ack:
		assert.Fail(t, "did not expect ack")
	case <-time.After(time.Second):
	}

	c.Disconnect()

	assert.True(t, c.IsDisconnected())
	assert.Zero(t, len(c.subs.table))
	assert.Equal(t, 1, count)
}

func Test_ConsumeTimeout(t *testing.T) {
	s := test.NewRPCServer(t, test.Config{
		PubSubPath:        apiPaths.pubsub,
		SubscriptionsPath: apiPaths.subscriptions,
		ConsumeDrop:       true,
	})
	defer s.Close()

	// Change to shorter timeout
	consumeResponseTimeout = 2 * time.Second

	u, _ := url.Parse(s.URL)

	c, err := NewInternalConnection(Config{
		GroupID: "test-client",
		Domain:  u.Host,
		APIKeyProvider: func() ([]byte, error) {
			return []byte("xyz"), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, c)

	c.restClient.SetTLSClientConfig(&tls.Config{
		InsecureSkipVerify: true, // no verification for test server
	})

	err = c.Connect(context.Background())
	require.NoError(t, err)

	_, err = c.Subscribe("test-stream", "",
		func(_ error, _ string, _ map[string]string, _ []byte) {
			require.Fail(t, "Unexpected message")
		})
	assert.NoError(t, err)

	select {
	case <-c.Error:
		require.True(t, c.ConsumeTimeout)
	case <-time.After(5 * time.Second):
		require.Fail(t, "Error expected")
	}
	c.Disconnect()
}
