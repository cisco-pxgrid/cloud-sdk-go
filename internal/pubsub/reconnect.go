// Copyright (c) 2022, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
)

// consumeCtxStreams mirrors the decoded shape of the base64 consumeContext cursor:
// {"streams":[{"stream":"...","partition":N,"offset":N}]}.
type consumeCtxStreams struct {
	Streams []struct {
		Stream    string `json:"stream"`
		Partition int64  `json:"partition"`
		Offset    int64  `json:"offset"`
	} `json:"streams"`
}

// decodeConsumeOffset decodes the base64 consumeContext and returns the partition and offset
// for the given stream. ok is false if the context is empty, undecodable, or has no entry for
// the stream (e.g. before the first message, when the cursor is {"streams":[]}).
func decodeConsumeOffset(consumeCtx, stream string) (partition, offset int64, ok bool) {
	if consumeCtx == "" {
		return 0, 0, false
	}
	raw, err := base64.StdEncoding.DecodeString(consumeCtx)
	if err != nil {
		return 0, 0, false
	}
	var cc consumeCtxStreams
	if err := json.Unmarshal(raw, &cc); err != nil {
		return 0, 0, false
	}
	for _, s := range cc.Streams {
		if s.Stream == stream {
			return s.Partition, s.Offset, true
		}
	}
	return 0, 0, false
}

// Connection represents a connection to the DxHub PubSub server.
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
	// Apply diagnostic defaults here so that Connection-level goroutines (statusLogger) read the
	// resolved values. newInternalConnection applies the same defaults to its own copy, but the
	// Connection keeps this config for status/gap logging.
	if config.StatusLogInterval == 0 {
		config.StatusLogInterval = defaultStatusLogInterval
	}
	if config.MessageGapThreshold == 0 {
		config.MessageGapThreshold = defaultMessageGapThreshold
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
	if err := c.conn.connect(connectCtx); err != nil {
		return err
	}
	c.ctx, c.ctxCancel = context.WithCancel(context.Background())
	go c.errorHandler()
	go c.statusLogger()
	return nil
}

// Disconnect disconnects the connection to the DxHub PubSub server.
func (c *Connection) Disconnect() {
	if c.ctx != nil {
		c.ctxCancel()
	}
	if c.conn != nil {
		c.conn.disconnect()
	}
}

// IsDisconnected returns true if c is disconnected from the server.
func (c *Connection) IsDisconnected() bool {
	return c.conn.isDisconnected()
}

// Subscribe subscribes to a DxHub Pubsub Stream
func (c *Connection) Subscribe(stream string, handler SubscriptionCallback) error {
	subscriptionID, err := c.conn.subscribe(stream, "", handler)
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
	if err := c.conn.unsubscribe(stream); err != nil {
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

// errorHandler waits for error and puts it in the error channel.
// If there is message drop, ConsumeTimeout will be true, it reconnects and resubscribes.
func (c *Connection) errorHandler() {
	var err error
	defer func() {
		// Always push the err, even if it is nil
		c.Error <- err
	}()
	for {
		select {
		case err = <-c.conn.Error:
			if !c.conn.consumeTimeout {
				return
			}
			reconnectStart := time.Now()
			log.Logger.Warnf("Consume timeout. Reconnecting. region=%s groupId=%s", c.config.Domain, c.config.GroupID)
			// Create new connection and subscribe with existing subscription ID
			c.conn, err = newInternalConnection(c.config)
			if err != nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err = c.conn.connect(ctx); err != nil {
				return
			}
			log.Logger.Infof("Reconnected to PubSub server. region=%s groupId=%s", c.config.Domain, c.config.GroupID)
			for _, sub := range c.subscriptions {
				_, err = c.conn.subscribe(sub.stream, sub.subscriptionID, sub.handler)
				if err != nil {
					return
				}
				log.Logger.Infof("Resubscribed after reconnect. region=%s stream=%s subID=%s",
					c.config.Domain, sub.stream, sub.subscriptionID)
			}
			c.config.stats.recordReconnect()
			log.Logger.Infof("Reconnect complete. region=%s groupId=%s streams=%d durationMs=%d reconnectCount=%d",
				c.config.Domain, c.config.GroupID, len(c.subscriptions),
				time.Since(reconnectStart).Milliseconds(), c.config.stats.reconnectSnapshot())
		case <-c.ctx.Done():
			return
		}
	}
}

// statusLogger periodically logs the read-stream connection status, a per-stream summary
// of messages received during the interval, and a WARN for any subscribed stream that has
// received no messages beyond MessageGapThreshold while the connection is up.
func (c *Connection) statusLogger() {
	interval := c.config.StatusLogInterval
	if interval <= 0 {
		interval = defaultStatusLogInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			disconnected := c.IsDisconnected()
			connectedSince, reconnects, sinceLast, counts, consumeIters, lastConsumeCtx := c.config.stats.snapshotAndReset()

			// Build a stable list of subscribed streams with their subscription IDs.
			var streams string
			for stream, sub := range c.subscriptions {
				received := counts[stream]
				iters := consumeIters[stream]
				consumeCtx := lastConsumeCtx[stream]
				partition, offset := int64(-1), int64(-1)
				if p, o, ok := decodeConsumeOffset(consumeCtx, stream); ok {
					partition, offset = p, o
				}
				log.Logger.Infof("Read-stream summary. region=%s stream=%s subID=%s received=%d consumeIters=%d intervalSec=%d disconnected=%t partition=%d offset=%d lastConsumeCtx=%s",
					c.config.Domain, stream, sub.subscriptionID, received, iters, int(interval.Seconds()), disconnected, partition, offset, consumeCtx)
				streams += stream + " "

				// Gap detection: connection is up but no messages for longer than the threshold.
				if !disconnected {
					if gap, ok := sinceLast[stream]; ok && gap > c.config.MessageGapThreshold {
						log.Logger.Warnf("Read-stream gap detected. region=%s stream=%s subID=%s noMessagesForSec=%d thresholdSec=%d consumeItersInterval=%d partition=%d offset=%d lastConsumeCtx=%s (connection up)",
							c.config.Domain, stream, sub.subscriptionID,
							int(gap.Seconds()), int(c.config.MessageGapThreshold.Seconds()), iters, partition, offset, consumeCtx)
					}
				}
			}

			log.Logger.Infof("Read-stream status. region=%s groupId=%s disconnected=%t connectedForSec=%d reconnectCount=%d streams=[%s]",
				c.config.Domain, c.config.GroupID, disconnected,
				int(time.Since(connectedSince).Seconds()), reconnects, streams)
		case <-c.ctx.Done():
			return
		}
	}
}
