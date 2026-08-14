// Copyright (c) 2022, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
)

// Connection represents a connection to the DxHub PubSub server.
type Connection struct {
	config Config

	// mu protects the internal connection pointer and subscription registry from the diagnostic
	// status goroutine. Network operations use snapshots so logging never holds this lock while
	// waiting on I/O.
	mu                   sync.RWMutex
	lifecycleMu          sync.Mutex
	conn                 *internalConnection
	reconnecting         bool
	Error                chan error
	attemptConnectCancel context.CancelFunc
	ctxCancel            context.CancelFunc
	statusLoggerDone     chan struct{}
	subscriptions        map[string]subscriptionParams
}

type subscriptionParams struct {
	stream         string
	subscriptionID string
	handler        SubscriptionCallback
}

// NewConnection creates a new connection object based on the supplied configuration.
func NewConnection(config Config) (*Connection, error) {
	// Apply the diagnostic default here so the Connection-level status goroutine reads the resolved
	// value. newInternalConnection applies the same default to its own config copy.
	if config.StatusLogInterval == 0 {
		config.StatusLogInterval = defaultStatusLogInterval
	}
	// Create the shared diagnostic stats up-front and keep it in c.config so that every
	// internalConnection built from this config (including reconnects) shares the same
	// counters and last-message timestamps.
	if config.stats == nil {
		config.stats = newConnStats()
	}
	conn, err := newInternalConnection(config)
	if err != nil {
		return nil, err
	}
	c := &Connection{
		config:        config,
		conn:          conn,
		Error:         make(chan error, 1),
		subscriptions: map[string]subscriptionParams{},
	}
	return c, nil
}

func (c *Connection) String() string {
	return fmt.Sprintf("Conn[ID: %s, Domain: %s]", c.config.GroupID, c.config.Domain)
}

// Connect establishes a connection to the DxHub PubSub server.
func (c *Connection) Connect(connectCtx context.Context) error {
	// Serialize lifecycle completion, while allowing Disconnect to cancel a slow network attempt
	// before it waits for this operation to finish.
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	attemptCtx, attemptCancel := context.WithCancel(connectCtx)
	c.mu.Lock()
	c.attemptConnectCancel = attemptCancel
	conn := c.conn
	c.mu.Unlock()

	err := conn.connect(attemptCtx)
	attemptErr := attemptCtx.Err()
	attemptCancel()
	c.mu.Lock()
	c.attemptConnectCancel = nil
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if attemptErr != nil {
		conn.disconnect()
		return attemptErr
	}
	ctx, cancel := context.WithCancel(context.Background())
	statusLoggerDone := make(chan struct{})
	c.mu.Lock()
	c.ctxCancel = cancel
	c.statusLoggerDone = statusLoggerDone
	c.mu.Unlock()
	go c.errorHandler(ctx)
	go func() {
		defer close(statusLoggerDone)
		c.statusLogger(ctx)
	}()
	return nil
}

// Disconnect disconnects the connection to the DxHub PubSub server.
func (c *Connection) Disconnect() {
	// Cancel first so a slow in-flight connect can return, then serialize the final disconnect with
	// Connect to prevent a successful attempt from publishing lifecycle state afterward.
	c.mu.RLock()
	attemptCancel := c.attemptConnectCancel
	c.mu.RUnlock()
	if attemptCancel != nil {
		attemptCancel()
	}

	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	cancel := c.ctxCancel
	c.ctxCancel = nil
	statusLoggerDone := c.statusLoggerDone
	c.statusLoggerDone = nil
	conn := c.conn
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// The status logger owns the final diagnostic snapshot. Join it before disconnect tears down
	// subscriptions and their active stream evidence, and before reporting lifecycle completion.
	if statusLoggerDone != nil {
		<-statusLoggerDone
	}
	if conn != nil {
		conn.disconnect()
	}
}

// IsDisconnected returns true if c is disconnected from the server.
func (c *Connection) IsDisconnected() bool {
	conn := c.connectionSnapshot()
	return conn == nil || conn.isDisconnected()
}

// Subscribe subscribes to a DxHub Pubsub Stream
func (c *Connection) Subscribe(stream string, handler SubscriptionCallback) error {
	connection := c.connectionSnapshot()
	subscriptionID, err := connection.subscribe(stream, "", handler)
	if err != nil {
		return err
	}
	sub := subscriptionParams{
		stream:         stream,
		subscriptionID: subscriptionID,
		handler:        handler,
	}
	c.mu.Lock()
	c.subscriptions[stream] = sub
	c.mu.Unlock()
	if c.config.GapTracker != nil {
		c.config.GapTracker.connectionReady(stream)
	}
	return nil
}

// Unsubscribe unsubscribes from a DxHub Pubsub Stream
func (c *Connection) Unsubscribe(stream string) error {
	if err := c.connectionSnapshot().unsubscribe(stream); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.subscriptions, stream)
	c.mu.Unlock()
	return nil
}

// Publish publishes a message to the stream asynchronously.
func (c *Connection) Publish(ctx context.Context, stream string, headers map[string]string, payload []byte) (*PublishResult, error) {
	return c.connectionSnapshot().Publish(ctx, stream, headers, payload)
}

// PublishAsync publishes a message to the stream asynchronously.
// Response can be monitored on the supplied channel. The cancel function must be invoked before closing the channel.
func (c *Connection) PublishAsync(stream string, headers map[string]string, payload []byte, result chan *PublishResult) (msgID string, cancel func(), err error) {
	return c.connectionSnapshot().PublishAsync(stream, headers, payload, result)
}

// errorHandler waits for error and puts it in the error channel.
// If there is message drop, ConsumeTimeout will be true, it reconnects and resubscribes.
func (c *Connection) errorHandler(ctx context.Context) {
	var err error
	defer func() {
		// Always push the err, even if it is nil
		c.Error <- err
	}()
	for {
		conn := c.connectionSnapshot()
		select {
		case err = <-conn.Error:
			if !conn.hasConsumeTimeout() {
				return
			}
			c.setReconnecting(true)
			reconnectStart := time.Now()
			log.Logger.Warnf("Consume timeout. Reconnecting. region=%s groupId=%s", c.config.Domain, c.config.GroupID)
			// Create new connection and subscribe with existing subscription ID
			newConn, connectionErr := newInternalConnection(c.config)
			if connectionErr != nil {
				c.setReconnecting(false)
				err = connectionErr
				return
			}
			c.setConnection(newConn)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err = newConn.connect(ctx)
			cancel()
			if err != nil {
				c.setReconnecting(false)
				return
			}
			log.Logger.Infof("Reconnected to PubSub server. region=%s groupId=%s", c.config.Domain, c.config.GroupID)
			subscriptions := c.subscriptionSnapshot()
			for _, sub := range subscriptions {
				_, err = newConn.subscribe(sub.stream, sub.subscriptionID, sub.handler)
				if err != nil {
					c.setReconnecting(false)
					return
				}
				log.Logger.Infof("Resubscribed after reconnect. region=%s stream=%s subID=%s",
					c.config.Domain, sub.stream, sub.subscriptionID)
			}
			c.config.stats.recordReconnect()
			log.Logger.Infof("Reconnect complete. region=%s groupId=%s streams=%d durationMs=%d reconnectCount=%d",
				c.config.Domain, c.config.GroupID, len(subscriptions),
				time.Since(reconnectStart).Milliseconds(), c.config.stats.reconnectSnapshot())
			c.setReconnecting(false)
			if c.config.GapTracker != nil {
				for _, sub := range subscriptions {
					c.config.GapTracker.connectionReady(sub.stream)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Connection) connectionSnapshot() *internalConnection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn
}

func (c *Connection) setConnection(conn *internalConnection) {
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
}

func (c *Connection) setReconnecting(reconnecting bool) {
	c.mu.Lock()
	c.reconnecting = reconnecting
	c.mu.Unlock()
}

func (c *Connection) diagnosticConnectionState() (disconnected, reconnecting bool) {
	c.mu.RLock()
	conn := c.conn
	reconnecting = c.reconnecting
	c.mu.RUnlock()
	return conn == nil || conn.isDisconnected(), reconnecting
}

func (c *Connection) subscriptionSnapshot() []subscriptionParams {
	c.mu.RLock()
	defer c.mu.RUnlock()
	subscriptions := make([]subscriptionParams, 0, len(c.subscriptions))
	for _, sub := range c.subscriptions {
		subscriptions = append(subscriptions, sub)
	}
	return subscriptions
}
