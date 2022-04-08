// Copyright (c) 2022, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"context"
	"fmt"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
)

// Connection represents a connection to the DxHub PubSub server.
// WORKAROUND This is a wrapper for original Connection
// The original Connection is now renamed to internalConnection
// This reconnects in case of the server message drop issue.
// When server issue is fixed, InternalConnection will be reverted back to Connection.
type Connection struct {
	config        Config
	conn          *internalConnection
	Error         chan error
	ctx           context.Context
	ctxCancel     context.CancelFunc
	subscriptions map[string]subscriptionParams
}

type subscriptionParams struct {
	stream         string
	subscriptionID string
	handler        SubscriptionCallback
}

// NewConnection creates a new connection object based on the supplied configuration.
func NewConnection(config Config) (*Connection, error) {
	conn, err := NewInternalConnection(config)
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
	if err := c.conn.Connect(connectCtx); err != nil {
		return err
	}
	c.ctx, c.ctxCancel = context.WithCancel(context.Background())
	go c.errorHandler()
	return nil
}

// Disconnect disconnects the connection to the DxHub PubSub server.
func (c *Connection) Disconnect() {
	c.ctxCancel()
	if c.conn != nil {
		c.conn.Disconnect()
	}
}

// IsDisconnected returns true if c is disconnected from the server.
func (c *Connection) IsDisconnected() bool {
	return c.conn.IsDisconnected()
}

// Subscribe subscribes to a DxHub Pubsub Stream
func (c *Connection) Subscribe(stream string, handler SubscriptionCallback) error {
	subscriptionID, err := c.conn.Subscribe(stream, "", handler)
	if err != nil {
		return err
	}
	sub := subscriptionParams{
		stream:         stream,
		subscriptionID: subscriptionID,
		handler:        handler,
	}
	c.subscriptions[stream] = sub
	return nil
}

// Unsubscribe unsubscribes from a DxHub Pubsub Stream
func (c *Connection) Unsubscribe(stream string) error {
	if err := c.conn.Unsubscribe(stream); err != nil {
		return err
	}
	delete(c.subscriptions, stream)
	return nil
}

// Publish publishes a message to the stream asynchronously.
func (c *Connection) Publish(ctx context.Context, stream string, headers map[string]string, payload []byte) (*PublishResult, error) {
	return c.conn.Publish(ctx, stream, headers, payload)
}

// PublishAsync publishes a message to the stream asynchronously.
// Response can be monitored on the supplied channel. The cancel function must be invoked before closing the channel.
func (c *Connection) PublishAsync(stream string, headers map[string]string, payload []byte, result chan *PublishResult) (msgID string, cancel func(), err error) {
	return c.conn.PublishAsync(stream, headers, payload, result)
}

// errorHandler waits for error and puts it in the error channel
// If there is message drop, ConsumeTimeout will be true,
// it reconnects and resubscribes
func (c *Connection) errorHandler() {
	var err error
	defer func() {
		// Always push the err, even if it is nil
		c.Error <- err
	}()
	for {
		select {
		case err = <-c.conn.Error:
			if !c.conn.ConsumeTimeout {
				return
			}
			log.Logger.Warnf("Consume timeout. Reconnecting")
			// Create new connection and subscribe with existing subscription ID
			c.conn, err = NewInternalConnection(c.config)
			if err != nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err = c.conn.Connect(ctx); err != nil {
				return
			}
			for _, sub := range c.subscriptions {
				_, err = c.conn.Subscribe(sub.stream, sub.subscriptionID, sub.handler)
				if err != nil {
					return
				}
			}
		case <-c.ctx.Done():
			return
		}
	}
}
