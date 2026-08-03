// Copyright (c) 2022, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
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
	var raw []byte
	var err error
	// Standard padded Base64 is the format currently emitted by the broker. Accept raw and
	// URL-safe variants defensively because this decoding only enriches diagnostics; the opaque
	// consume context passed back to the broker is never modified.
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		raw, err = encoding.DecodeString(consumeCtx)
		if err == nil {
			break
		}
	}
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
	config Config

	// mu protects the internal connection pointer and subscription registry from the diagnostic
	// status goroutine. Network operations use snapshots so logging never holds this lock while
	// waiting on I/O.
	mu           sync.RWMutex
	conn         *internalConnection
	reconnecting bool
	Error        chan error
	ctxCancel    context.CancelFunc
	// lifecycleGeneration lets a concurrent Disconnect win over an in-flight Connect without
	// holding mu across network I/O. It does not change the established reconnect policy.
	lifecycleGeneration uint64
	subscriptions       map[string]subscriptionParams
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
	generation := c.lifecycleGenerationSnapshot()
	conn := c.connectionSnapshot()
	if err := conn.connect(connectCtx); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	if !c.activateLifecycle(generation, cancel) {
		cancel()
		conn.disconnect()
		return context.Canceled
	}
	go c.errorHandler(ctx)
	go c.statusLogger(ctx)
	return nil
}

// Disconnect disconnects the connection to the DxHub PubSub server.
func (c *Connection) Disconnect() {
	cancel := c.cancelLifecycle()
	if cancel != nil {
		cancel()
	}
	if conn := c.connectionSnapshot(); conn != nil {
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
		case <-ctx.Done():
			return
		}
	}
}

func (c *Connection) lifecycleGenerationSnapshot() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lifecycleGeneration
}

// activateLifecycle publishes the cancel function only if Disconnect has not occurred while
// the network connection was being established.
func (c *Connection) activateLifecycle(generation uint64, cancel context.CancelFunc) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lifecycleGeneration != generation {
		return false
	}
	c.ctxCancel = cancel
	return true
}

// cancelLifecycle invalidates an in-flight Connect and returns the active cancel function. The
// function is invoked by the caller after releasing mu so cancellation cannot run under the lock.
func (c *Connection) cancelLifecycle() context.CancelFunc {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lifecycleGeneration++
	cancel := c.ctxCancel
	c.ctxCancel = nil
	return cancel
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

type streamDiagnosticState string

const (
	streamStateAwaitingActivity     streamDiagnosticState = "awaiting_activity"
	streamStateHealthyDelivery      streamDiagnosticState = "healthy_delivery"
	streamStateBrokerQuiet          streamDiagnosticState = "broker_quiet"
	streamStateConsumerStalled      streamDiagnosticState = "consumer_stalled"
	streamStateSDKProcessingBlocked streamDiagnosticState = "sdk_processing_blocked"
	streamStateDisconnected         streamDiagnosticState = "disconnected"
	streamStateReconnecting         streamDiagnosticState = "reconnecting"
)

type streamStatsDelta struct {
	brokerResponses     int64
	brokerMessages      int64
	dispatchStarts      int64
	sdkProcessingStarts int64
	sdkProcessingEnds   int64
	cursorChanges       int64
}

func nonNegativeDelta(current, previous int64) int64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func diagnosticDelta(current, previous streamStatsSnapshot) streamStatsDelta {
	return streamStatsDelta{
		brokerResponses:     nonNegativeDelta(current.brokerResponses, previous.brokerResponses),
		brokerMessages:      nonNegativeDelta(current.brokerMessages, previous.brokerMessages),
		dispatchStarts:      nonNegativeDelta(current.dispatchStarts, previous.dispatchStarts),
		sdkProcessingStarts: nonNegativeDelta(current.sdkProcessingStarts, previous.sdkProcessingStarts),
		sdkProcessingEnds:   nonNegativeDelta(current.sdkProcessingEnds, previous.sdkProcessingEnds),
		cursorChanges:       nonNegativeDelta(current.cursorChanges, previous.cursorChanges),
	}
}

func elapsedSince(now, timestamp time.Time) time.Duration {
	if timestamp.IsZero() || timestamp.After(now) {
		return 0
	}
	return now.Sub(timestamp)
}

// classifyStream is deliberately diagnostic-only. It classifies the evidence already observed by
// the SDK and never initiates reconnect or changes delivery behavior. Without an explicit
// expected-traffic policy, continuing empty broker responses are the valid broker_quiet state.
func classifyStream(now time.Time, disconnected, reconnecting bool, current streamStatsSnapshot, delta streamStatsDelta, gapThreshold, sdkProcessingThreshold time.Duration) streamDiagnosticState {
	if sdkProcessingThreshold > 0 && current.sdkProcessingActive && elapsedSince(now, current.lastSDKProcessingAt) >= sdkProcessingThreshold {
		return streamStateSDKProcessingBlocked
	}
	if reconnecting {
		return streamStateReconnecting
	}
	if disconnected {
		return streamStateDisconnected
	}

	responseBaseline := current.lastBrokerResponseAt
	if responseBaseline.IsZero() {
		responseBaseline = current.subscribedAt
	}
	if gapThreshold > 0 && elapsedSince(now, responseBaseline) >= gapThreshold {
		return streamStateConsumerStalled
	}
	if delta.brokerMessages > 0 || delta.cursorChanges > 0 {
		return streamStateHealthyDelivery
	}
	if delta.brokerResponses > 0 {
		return streamStateBrokerQuiet
	}
	return streamStateAwaitingActivity
}

func diagnosticStateChanged(previous, current streamDiagnosticState, previousExists bool) bool {
	return !previousExists || previous != current
}

func diagnosticStateWarns(state streamDiagnosticState) bool {
	return state == streamStateConsumerStalled
}

// statusLogger periodically emits non-destructive interval deltas, cumulative totals, and an
// evidence-based state for each read stream. State changes are logged once; a quiet broker is INFO
// only, while a consumer stall is WARN. This function does not alter connection behavior.
func (c *Connection) statusLogger(ctx context.Context) {
	interval := c.config.StatusLogInterval
	if interval <= 0 {
		interval = defaultStatusLogInterval
	}
	previous := c.config.stats.snapshot().streams
	previousStates := make(map[string]streamDiagnosticState)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			disconnected, reconnecting := c.diagnosticConnectionState()
			snapshot := c.config.stats.snapshot()
			subscriptions := c.subscriptionSnapshot()

			var streams string
			for _, sub := range subscriptions {
				stream := sub.stream
				current := snapshot.streams[stream]
				if !current.active {
					continue
				}
				delta := diagnosticDelta(current, previous[stream])
				state := classifyStream(now, disconnected, reconnecting, current, delta, c.config.MessageGapThreshold, sdkProcessingSlowWarnThreshold)
				consumeCtx := current.lastConsumeCtx
				partition, offset := int64(-1), int64(-1)
				if p, o, ok := decodeConsumeOffset(consumeCtx, stream); ok {
					partition, offset = p, o
				}
				messageBaseline := current.lastMessageAt
				if messageBaseline.IsZero() {
					messageBaseline = current.subscribedAt
				}
				responseBaseline := current.lastBrokerResponseAt
				if responseBaseline.IsZero() {
					responseBaseline = current.subscribedAt
				}
				cursorBaseline := current.lastCursorChangeAt
				if cursorBaseline.IsZero() {
					cursorBaseline = current.subscribedAt
				}
				sdkProcessingFor := time.Duration(0)
				if current.sdkProcessingActive {
					sdkProcessingFor = elapsedSince(now, current.lastSDKProcessingAt)
				}
				log.Logger.Infof("Read-stream summary. region=%s stream=%s subID=%s state=%s received=%d receivedTotal=%d consumeIters=%d consumeItersTotal=%d dispatched=%d dispatchedTotal=%d sdkProcessingStarted=%d sdkProcessingStartedTotal=%d sdkProcessingCompleted=%d sdkProcessingCompletedTotal=%d sdkProcessingActive=%t sdkProcessingForSec=%d sdkProcessingMsgID=%s sdkProcessingTopic=%s cursorChanges=%d cursorChangesTotal=%d noCursorChangeForSec=%d noBrokerResponseForSec=%d noMessagesForSec=%d intervalSec=%d disconnected=%t reconnecting=%t partition=%d offset=%d lastConsumeCtx=%s",
					c.config.Domain, stream, sub.subscriptionID, state,
					delta.brokerMessages, current.brokerMessages, delta.brokerResponses, current.brokerResponses,
					delta.dispatchStarts, current.dispatchStarts, delta.sdkProcessingStarts, current.sdkProcessingStarts,
					delta.sdkProcessingEnds, current.sdkProcessingEnds,
					current.sdkProcessingActive, int(sdkProcessingFor.Seconds()), current.sdkProcessingMsgID, current.sdkProcessingTopic,
					delta.cursorChanges, current.cursorChanges, int(elapsedSince(now, cursorBaseline).Seconds()),
					int(elapsedSince(now, responseBaseline).Seconds()), int(elapsedSince(now, messageBaseline).Seconds()),
					int(interval.Seconds()), disconnected, reconnecting, partition, offset, consumeCtx)
				streams += stream + " "

				if prior, exists := previousStates[stream]; diagnosticStateChanged(prior, state, exists) {
					format := "Read-stream diagnostic state changed. region=%s stream=%s subID=%s previous=%s state=%s consumeIters=%d received=%d noBrokerResponseForSec=%d sdkProcessingActive=%t sdkProcessingForSec=%d sdkProcessingMsgID=%s sdkProcessingTopic=%s disconnected=%t reconnecting=%t partition=%d offset=%d"
					args := []interface{}{c.config.Domain, stream, sub.subscriptionID, prior, state,
						delta.brokerResponses, delta.brokerMessages, int(elapsedSince(now, responseBaseline).Seconds()),
						current.sdkProcessingActive, int(sdkProcessingFor.Seconds()), current.sdkProcessingMsgID, current.sdkProcessingTopic,
						disconnected, reconnecting, partition, offset}
					if diagnosticStateWarns(state) {
						log.Logger.Warnf(format, args...)
					} else {
						log.Logger.Infof(format, args...)
					}
				}
				previousStates[stream] = state
			}
			previous = snapshot.streams

			connectedFor := 0
			if !snapshot.connectedSince.IsZero() {
				connectedFor = int(now.Sub(snapshot.connectedSince).Seconds())
			}
			log.Logger.Infof("Read-stream status. region=%s groupId=%s disconnected=%t connectedForSec=%d reconnectCount=%d streams=[%s]",
				c.config.Domain, c.config.GroupID, disconnected, connectedFor, snapshot.reconnectCount, streams)
		case <-ctx.Done():
			return
		}
	}
}
