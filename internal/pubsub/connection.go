// Copyright (c) 2021, Cisco Systems, Inc.
// All rights reserved.

// Package pubsub implements functionality to interact with DxHub using PubSub semantics.
//
// # Examples
//
// Following examples explain how to use the pubsub package at the high level.
//
// Connection
//
//	config := &pubsub.Config{App: "test-app", Domain: "test-domain", APIKeyProvider: func() string { return "APIKey"}}
//	conn, err := pubsub.NewConnection(config)
//	err = conn.Connect(context.Background())
//	go func() {
//	    err = <- conn.Error  // Monitor connection for any errors
//	}
//	...
//	conn.Disconnect()        // Disconnect from the server
//
// Subscribe
//
//	conn.Subscribe("stream", func(err error, id string, headers map[string]string, payload []byte){
//	    fmt.Printf("Received new message: %s, %v, %s", id, headers, payload)
//	})
//
// Publish
//
//	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
//	headers := map[string]string{
//	    "key1": "value1",
//	    "key2": "value2",
//	}
//	payload := []byte("message payload")
//	resp, err := conn.Publish(ctx, "stream", headers, payload)
//	fmt.Printf("Message publish result: %v", resp)
//
// PublishAsync
//
//	headers := map[string]string{
//	    "key1": "value1",
//	    "key2": "value2",
//	}
//	payload := []byte("message payload")
//	respCh := make(chan *PublishResult)
//	id, cancel, err := conn.PublishAsync("stream", headers, payload, respCh)
//	defer cancel()
//	resp := <- respCh
//	fmt.Printf("Message publish result: %v", resp)
package pubsub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	rpc "github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"
	"github.com/google/uuid"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"github.com/cisco-pxgrid/websocket"
	"github.com/go-resty/resty/v2"
)

var (
	defaultTimeout      = 15 * time.Second
	pingPeriod          = 55 * time.Second
	pongWait            = 60 * time.Second
	defaultPollInterval = 1 * time.Second
	handlersExpiration  = 3 * time.Minute
	WebSocketScheme     = "wss"
	HttpScheme          = "https"
	apiPaths            = struct {
		subscriptions string
		pubsub        string
	}{
		subscriptions: "/api/dxhub/v1/registry/subscriptions",
		pubsub:        "/api/v2/pubsub",
	}
	maxMessageSize int64 = 51 * 1024 * 1024 // 51mb. DxHub max message size is 50mb. An extra mb as a buffer.

)

const (
	headerStrApiKey    = "X-Api-Key"
	headerStrAuthToken = "X-Auth-Token"
)

// Config represents configuration required to open the pubsub connection.
type Config struct {
	// GroupID specifies the group ID of the PubSub client
	GroupID string

	// Domain should be set to the cloud domain of the region where Application wants to connect to.
	Domain string

	// APIKeyProvider returns the API Key for the Application. Either APIKeyProvider or the
	// AuthTokenProvider must be set in the config. APIKeyProvider takes precedence over the
	// AuthTokenProvider.
	APIKeyProvider func() ([]byte, error)

	// AuthTokenProvider returns the generated Auth Token for the Application. Either APIKeyProvider
	// or the AuthTokenProvider must be set in the config.
	AuthTokenProvider func() ([]byte, error)

	// PollInterval defines the interval between consecutive read requests to the server.
	// Default is 1 second.
	PollInterval time.Duration

	Transport *http.Transport
}

// internalConnection represents a connection to the DxHub PubSub server.
type internalConnection struct {
	// Error channel should be monitored by the user for receiving error notification from the
	// connection. If an error is sent to this channel, then the connection is already closed. Error
	// returned from the channel shall describe the reason of connection closure. A nil value
	// indicates normal closure.
	Error      chan error
	id         string
	mu         sync.Mutex       // lock to protect the connection itself
	config     Config           // config received from the user
	restClient *resty.Client    // resty HTTP client
	ws         *websocket.Conn  // websocket connection
	wg         sync.WaitGroup   // waitgroup to ensure all spawned goroutines exit
	closed     chan struct{}    // channel to notify goroutine about connection closure
	readerCh   chan []byte      // channel where read messages are sent to for processing
	writerCh   chan *msgRequest // channel where messages are sent to for publishing
	closeOnce  sync.Once        // to make sure the connection closure procedure is performed only once
	authHeader struct {         // auth header
		key      string                 // string to use for auth header, either "X-API-KEY" or "X-AUTH-TOKEN"
		provider func() ([]byte, error) // auth header provider function
	}
	subs struct { // subscriptions
		table      map[string]*subscription // map of subscriptions indexed by stream name
		sync.Mutex                          // lock to protect the table
	}
	msgHandlers *handlerMap

	// consumeTimeout to signify there was a consume timeout within subscriber
	consumeTimeout bool
}

// newInternalConnection creates a new connection object based on the supplied configuration.
func newInternalConnection(config Config) (*internalConnection, error) {
	if config.GroupID == "" {
		return nil, fmt.Errorf("Config must contain GroupID")
	}
	if config.Domain == "" {
		return nil, fmt.Errorf("Config must contain Domain")
	}
	if config.PollInterval == 0 {
		config.PollInterval = defaultPollInterval
	}

	httpClient := resty.New()
	if config.Transport != nil {
		httpClient.SetTransport(config.Transport)
	}
	c := &internalConnection{
		config:      config,
		id:          uuid.New().String(),
		restClient:  httpClient,
		closed:      make(chan struct{}),
		Error:       make(chan error, 1),        // buffer of 1 to make sure that error is not lost
		readerCh:    make(chan []byte, 64),      // buffer of 64 helps with latency and provides a buffer to catch up during processing
		writerCh:    make(chan *msgRequest, 64), // buffer of 64 helps with latency and provides a buffer to catch up during processing
		msgHandlers: NewHandlerMap(handlersExpiration),
	}
	c.subs.table = make(map[string]*subscription)

	if config.APIKeyProvider != nil {
		c.authHeader.key = headerStrApiKey
		c.authHeader.provider = config.APIKeyProvider
	} else if config.AuthTokenProvider != nil {
		c.authHeader.key = headerStrAuthToken
		c.authHeader.provider = config.AuthTokenProvider
	} else {
		return nil, fmt.Errorf("Config must contain either APIKeyProvider or AuthTokenProvider")
	}

	return c, nil
}

func (c *internalConnection) String() string {
	return fmt.Sprintf("Conn[ID: %s, Domain: %s]", c.config.GroupID, c.config.Domain)
}

// connect establishes a connection to the DxHub PubSub server.
func (c *internalConnection) connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ws != nil {
		return fmt.Errorf("already connected")
	}
	authToken, err := c.authHeader.provider()
	if err != nil {
		return fmt.Errorf("failed to get auth token: %v", err)
	}
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			c.authHeader.key: []string{string(authToken)},
		},
		HTTPClient: c.restClient.GetClient(),
	}
	var resp *http.Response
	brokerSubURL := &url.URL{
		Host:   c.config.Domain,
		Scheme: WebSocketScheme,
		Path:   apiPaths.pubsub,
	}
	c.ws, resp, err = websocket.Dial(ctx, brokerSubURL.String(), opts)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("failed to connect: %v, HTTP Response: %+v", err, resp)
		}
		return fmt.Errorf("failed to connect: %v", err)
	}
	log.Logger.Infof("Connected to PubSub server. url=%s groupId=%s", brokerSubURL.String(), c.config.GroupID)
	c.ws.SetReadLimit(maxMessageSize)

	c.wg.Add(1)
	go c.processor()

	c.wg.Add(1)
	go c.reader()

	c.wg.Add(1)
	go c.writer()

	err = c.sendOpenMessage()
	if err != nil {
		c.closeNotify(c.checkWSError(err))
		return fmt.Errorf("failed to send open message: %v", err)
	}

	return nil
}

// ping pings the WebSocket server and waits for Pong
func (c *internalConnection) ping() {
	c.wg.Add(1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pongWait)
		log.Logger.Debugf("Pinging server %v", c)
		err := c.ws.Ping(ctx)
		cancel()
		if err != nil {
			// deferring the close here to make sure that wg gets marked Done before close
			defer c.closeNotify(c.checkWSError(err))
		}
		c.wg.Done()
	}()
}

func (c *internalConnection) writer() {
loop:
	for {
		select {
		case <-c.closed:
			break loop
		case msg := <-c.writerCh:
			ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
			defer cancel()

			if msg.handler != nil {
				c.msgHandlers.Set(msg.req.ID, msg.handler)
			}
			err := c.ws.Write(ctx, websocket.MessageText, msg.req.Bytes())
			if err != nil {
				log.Logger.Errorf("Failed to write message %s: %v", msg.req, err)

				defer c.closeNotify(c.checkWSError(err))
				break loop
			}
		}
	}
	log.Logger.Debugf("writer shutdown complete")
	c.wg.Done()
}

// processor goroutine processes the incoming messages from the WebSocket connection and sends ping
// messages when required
func (c *internalConnection) processor() {
loop:
	for {
		select {
		case <-c.closed:
			break loop
		case msg := <-c.readerCh:
			resp, err := rpc.NewResponseFromBytes(msg)
			if err != nil {
				log.Logger.Errorf("Received unknown message: %s", msg)
				continue
			}
			handler := c.msgHandlers.GetAndDelete(resp.ID)
			if handler != nil {
				handler(resp)
			}
		case <-time.After(pingPeriod):
			c.ping()
		}
	}
	log.Logger.Debugf("processor shutdown complete")
	c.wg.Done()
}

// reader reads messages from the WebSocket connection and passes it to processor
func (c *internalConnection) reader() {
	for {
		msg, err := c.read(context.Background())
		if err != nil {
			// deferring the close here to make sure that wg gets marked Done before close
			defer c.closeNotify(c.checkWSError(err))
			break
		}

		select {
		case c.readerCh <- msg:
		default:
			// processor cannot take anymore messages because it's blocked downstream
			log.Logger.Errorf("Failed to submit message %s: processor is blocked", msg)
		}
	}
	log.Logger.Debugf("reader goroutine shutdown complete")
	c.wg.Done()
}

// disconnect disconnects the connection to the DxHub PubSub server.
func (c *internalConnection) disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ws == nil {
		log.Logger.Debugf("Connection is not opened")
		return
	}
	if c.isClosed() {
		return
	}

	err := c.sendCloseMessage()
	if err != nil {
		log.Logger.Errorf("failed to send close message: %v", err)
	}
	log.Logger.Debugf("connection closing")
	err = c.ws.Close(websocket.StatusNormalClosure, websocket.StatusNormalClosure.String())

	// wait for all goroutines to finish
	log.Logger.Debugf("waiting for all goroutines to finish")
	c.wg.Wait()

	if err != nil {
		log.Logger.Infof("PubSub connection closed with error: %v", err)
	} else {
		log.Logger.Infof("PubSub connection closed")
	}
}

// closeNotify notifies other goroutines about the connection closure
func (c *internalConnection) closeNotify(err error) {
	c.closeOnce.Do(func() {
		close(c.closed)

		c.subs.Lock()
		// WORKAROUND To decide if subscription needs to be deleted
		// c.unsubscribe still needs to be called to free up other resource
		deleteSub := true
		if c.consumeTimeout {
			log.Logger.Infof("Consume timeout. Not deleting subscription as reconnect will reuse")
			deleteSub = false
		}
		for stream := range c.subs.table {
			log.Logger.Debugf("unsubscribing from %s", stream)
			e := c.unsubscribeWithoutLock(stream, deleteSub)
			if e != nil {
				log.Logger.Errorf("failed to unsubscribe from stream %s: %v", stream, e)
			}
		}
		c.subs.Unlock()

		c.wg.Wait()
		c.Error <- err
		close(c.Error)
	})
}

// isDisconnected returns true if c is disconnected from the server.
func (c *internalConnection) isDisconnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.isClosed()
}

func (c *internalConnection) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *internalConnection) checkWSError(err error) error {
	closeStatus := websocket.CloseStatus(err)
	if closeStatus == websocket.StatusNormalClosure || closeStatus == websocket.StatusGoingAway {
		log.Logger.Infof("PubSub connection closed by server: %v", closeStatus)
		return nil
	} else if closeStatus != -1 {
		log.Logger.Errorf("PubSub connection failure: %v", closeStatus)
		return err
	} else if errors.Is(err, io.EOF) {
		log.Logger.Infof("PubSub connection closed by server: EOF")
		return nil
	} else {
		log.Logger.Errorf("Unexpected PubSub connection failure: %v", err)
		return err
	}
}

func (c *internalConnection) read(ctx context.Context) ([]byte, error) {
	_, p, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}

	return p, err
}
