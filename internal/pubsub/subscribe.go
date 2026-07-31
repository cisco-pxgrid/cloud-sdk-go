// Copyright (c) 2021, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sync"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"
	"github.com/cisco-pxgrid/cloud-sdk-go/log"
)

// SubscriptionCallback is the callback that's invoked when a message/error is received for the
// subscription request.
//
// err is set to a non-nil error in case of an error
// id represents the message id
// headers are the headers associated with the message
// payload contains the message payload
type SubscriptionCallback func(err error, id string, headers map[string]string, payload []byte)

var consumeResponseTimeout = 15 * time.Second
var resultProcessingTimeout = 60 * time.Second
var inactivityMessageLogDuration = 10 * time.Minute
var errConsumeTimeout = errors.New("consume timeout")

// sdkProcessingSlowWarnThreshold is how long the synchronous SDK subscription callback may run
// before the pub/sub layer logs a warning. The actual user DeviceMessageHandler is timed separately
// in app.go.
var sdkProcessingSlowWarnThreshold = 10 * time.Second

// sdkProcessingWatchInterval is how often the watchdog checks in-flight SDK processing and repeats
// the warning while it remains blocked.
var sdkProcessingWatchInterval = 15 * time.Second

// subscribe subscribes to a DxHub Pubsub Stream
func (c *internalConnection) subscribe(stream string, subscriptionID string, handler SubscriptionCallback) (string, error) {
	c.subs.Lock()
	defer c.subs.Unlock()

	var sub *subscription
	if _, ok := c.subs.table[stream]; ok {
		return "", fmt.Errorf("subscription for stream %s already exists", stream)
	}

	var id string
	if subscriptionID != "" {
		id = subscriptionID
		log.Logger.Infof("Reuse subscription ID=%s", id)
	} else {
		var err error
		id, err = c.createSubscription(stream)
		if err != nil {
			return "", fmt.Errorf("failed to create subscription for %s: %v", stream, err)
		}
		log.Logger.Infof("Created subscription. id=%s stream=%s", id, stream)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sub = &subscription{
		id:        id,
		stream:    stream,
		callback:  handler,
		ctx:       ctx,
		ctxCancel: cancel,
	}
	c.subs.table[stream] = sub

	// Start a fresh diagnostic lifecycle only after subscription creation/reuse succeeds. Process-
	// lifetime counters remain cumulative across reconnects.
	c.config.stats.beginStreamLifecycle(stream)

	c.wg.Add(1)
	sub.wg.Add(1)
	go c.subscriber(sub)

	return id, nil
}

func (c *internalConnection) unsubscribe(stream string) error {
	log.Logger.Debugf("Unsubscribing from DxHub Pubsub Stream %s", stream)
	c.subs.Lock()
	defer c.subs.Unlock()

	return c.unsubscribeWithoutLock(stream, true)
}

// unsubscribeWithoutLock unsubscribes from a DxHub Pubsub Stream
func (c *internalConnection) unsubscribeWithoutLock(stream string, deleteSub bool) error {
	sub, ok := c.subs.table[stream]
	if !ok {
		return fmt.Errorf("subscription for stream %s doesn't exist", stream)
	}
	if deleteSub {
		err := c.deleteSubscription(sub.id)
		if err != nil {
			return fmt.Errorf("failed to unsubscribe from stream %s: %v", stream, err)
		}
	}

	delete(c.subs.table, stream)
	sub.ctxCancel()
	sub.wg.Wait()
	if deleteSub {
		c.config.stats.endStreamLifecycle(stream)
	}
	log.Logger.Debugf("Successfully unsubscribed from stream %s", stream)
	return nil
}

// consumer send consume request, parse to get the result and put it in the result channel
// Note that there are implementations here to workaround the cloud's messaging behavior:
//  1. There is an issue in cloud. After no consume for 90s, the internal connection is dropped but the external websocket connection stays.
//     This SDK, the external websocket, cannot detect the situation. Sending consume request will not return any further messages.
//     The workaround here is to return a consume timeout when the result channel is blocked for 60 seconds (resultProcessingTimeout).
//  2. The cloud's messaging mechanism is Kafka, which is meant for distributed processing. Kafka guarantees at least once delivery.
//     If messages are not acknowledged, they will be redelivered.
//     During a callback, if the app takes too long to process, the messages maybe redelivered and gets into an infinite loop.
//     To avoid this, this "consumer" thread is used to acknowlegde current message by reading the next message.
//     But this may cause message loss because we are acking the message before the app processes it.
//     However, since app is treating this as a pubsub system, it will handle the message loss.
func (c *internalConnection) consumer(sub *subscription, resultCh chan *rpc.ConsumeResult) error {
	consumeCtx := ""
	respCh := make(chan *rpc.Response)

	inactivityMessageLogTime := time.Now().Add(inactivityMessageLogDuration)

	for {
		activity := false
		req, err := rpc.NewConsumeRequest(sub.id, consumeCtx)
		if err != nil {
			return err
		}

		err = c.sendMessage(req, func(resp *rpc.Response) {
			select {
			case respCh <- resp:
			case <-sub.ctx.Done():
				log.Logger.Debugf("Consumer stopped for stream %s", sub.stream)
			}
		})
		if err != nil {
			return err
		}
		select {
		case resp := <-respCh:
			if resp.Error.Code != 0 {
				return fmt.Errorf("consume error: %v", resp.Error)
			}

			res, err := resp.ConsumeResult()
			if err != nil {
				return err
			}
			messages, hasStream := res.Messages[sub.stream]
			// Record the broker observation before the unbuffered subscriber handoff. If the
			// subscriber is blocked in an app callback, diagnostics must still show that the broker
			// responded and whether that response contained messages.
			c.config.stats.recordBrokerResponse(sub.stream, res.ConsumeContext, len(messages))
			if hasStream && len(messages) > 0 {
				activity = true
			}

			select {
			case resultCh <- res:
				consumeCtx = res.ConsumeContext
			case <-time.After(resultProcessingTimeout):
				return errConsumeTimeout
			}
		case <-time.After(consumeResponseTimeout):
			return errConsumeTimeout
		case <-sub.ctx.Done():
			return nil
		}
		if !activity {
			if time.Now().After(inactivityMessageLogTime) {
				log.Logger.Infof("No message for %s for subscription %s", inactivityMessageLogDuration, sub.id)
				inactivityMessageLogTime = time.Now().Add(inactivityMessageLogDuration)
			}
			select {
			case <-time.After(c.config.PollInterval):
			case <-sub.ctx.Done():
				return nil
			}
		} else {
			inactivityMessageLogTime = time.Now().Add(inactivityMessageLogDuration)
		}
	}
}

type subscription struct {
	stream    string
	id        string
	callback  SubscriptionCallback
	ctx       context.Context
	ctxCancel context.CancelFunc
	wg        sync.WaitGroup
}

// subscriber goroutine is spawned for each subscription to a stream.
// It spawns another consumer goroutine to consume messages from the stream.
// See "consumer" function for more details on why this is necessary.
func (c *internalConnection) subscriber(sub *subscription) {
	defer sub.wg.Done()
	defer c.wg.Done()
	log.Logger.Debugf("Starting subscriber thread for %s", sub.stream)

	resultCh := make(chan *rpc.ConsumeResult)
	var err error
	go func() {
		err = c.consumer(sub, resultCh)
		close(resultCh)
	}()

	// This boundary times the complete SDK subscription callback. The application layer separately
	// times only the user DeviceMessageHandler so operators can attribute the delay.
	watch := &sdkProcessingWatch{}
	watchThreshold := sdkProcessingSlowWarnThreshold
	watchInterval := sdkProcessingWatchInterval
	watchdogDone := make(chan struct{})
	watchdogStopped := make(chan struct{})
	go func() {
		defer close(watchdogStopped)
		c.sdkProcessingWatchdog(sub, watch, watchdogDone, watchThreshold, watchInterval)
	}()
	defer func() {
		close(watchdogDone)
		<-watchdogStopped
	}()

	for res := range resultCh {
		for stream, messages := range res.Messages {
			if stream != sub.stream {
				log.Logger.Errorf("Received consume message for stream %s, was expecting messages for stream %s", stream, sub.stream)
				continue
			}
			for _, m := range messages {
				c.config.stats.recordDispatch(sub.stream)
				payload, decodeErr := base64.StdEncoding.DecodeString(m.Payload)
				if c.config.LogEachMessage {
					partition, offset := int64(-1), int64(-1)
					if p, o, ok := decodeConsumeOffset(res.ConsumeContext, sub.stream); ok {
						partition, offset = p, o
					}
					log.Logger.Infof("Read-stream message received. region=%s stream=%s topic=%s msgID=%s type=%s tenant=%s device=%s bytes=%d subID=%s partition=%d offset=%d consumeCtx=%s decodeErr=%v",
						c.config.Domain, sub.stream, m.Headers["stream"], m.MsgID, m.Headers["messageType"],
						m.Headers["tenant"], m.Headers["device"], len(payload), sub.id, partition, offset, res.ConsumeContext, decodeErr)
				}
				topic := m.Headers["stream"]
				watch.begin(m.MsgID, topic)
				c.config.stats.recordSDKProcessingStart(sub.stream, m.MsgID, topic)
				cbStart := time.Now()
				sub.callback(decodeErr, m.MsgID, m.Headers, payload)
				watch.end()
				c.config.stats.recordSDKProcessingComplete(sub.stream)
				if d := time.Since(cbStart); watchThreshold > 0 && d >= watchThreshold {
					log.Logger.Warnf("SDK read-stream processing returned after delay. region=%s stream=%s subID=%s msgID=%s topic=%s durationSec=%d",
						c.config.Domain, sub.stream, sub.id, m.MsgID, topic, int(d.Seconds()))
				}
			}
		}
	}
	if err != nil {
		if err == errConsumeTimeout {
			log.Logger.Warnf("Consume timeout. Disconnecting. region=%s stream=%s subID=%s sinceLastMsgSec=%d",
				c.config.Domain, sub.stream, sub.id, int(c.config.stats.sinceLastMessage(sub.stream).Seconds()))
			c.markConsumeTimeout()
			// This requires a go routine otherwise the waitgroup blocks forever
			go c.disconnect()
		} else {
			log.Logger.Errorf("Failed to consume messages for stream %s: %v", sub.stream, err)
			sub.callback(err, "", nil, nil)
		}
	}

	log.Logger.Debugf("Stopped subscriber thread for %s", sub.stream)
}

// sdkProcessingWatch tracks currently in-flight SDK subscription processing. The mutex is held
// only around the small begin/end/read operations, never during the callback itself.
type sdkProcessingWatch struct {
	mu          sync.Mutex
	start       time.Time
	lastWarning time.Time
	msgID       string
	topic       string
}

// begin records that a callback for msgID/topic has started.
func (w *sdkProcessingWatch) begin(msgID, topic string) {
	w.mu.Lock()
	w.start = time.Now()
	w.lastWarning = time.Time{}
	w.msgID = msgID
	w.topic = topic
	w.mu.Unlock()
}

// end clears the in-flight marker once the callback returns.
func (w *sdkProcessingWatch) end() {
	w.mu.Lock()
	w.start = time.Time{}
	w.lastWarning = time.Time{}
	w.mu.Unlock()
}

func (w *sdkProcessingWatch) warningDue(now time.Time, threshold, reminderInterval time.Duration) (elapsed time.Duration, msgID, topic string, initial, due bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.start.IsZero() || threshold <= 0 {
		return 0, "", "", false, false
	}
	elapsed = now.Sub(w.start)
	if elapsed < threshold {
		return 0, "", "", false, false
	}
	initial = w.lastWarning.IsZero()
	if !initial && (reminderInterval <= 0 || now.Sub(w.lastWarning) < reminderInterval) {
		return 0, "", "", false, false
	}
	w.lastWarning = now
	return elapsed, w.msgID, w.topic, initial, true
}

// sdkProcessingWatchdog reports SDK subscription processing that exceeds the fixed threshold. It
// repeats at the configured fixed reminder interval and does not change processing or recovery.
func (c *internalConnection) sdkProcessingWatchdog(sub *subscription, watch *sdkProcessingWatch, done <-chan struct{}, threshold, reminderInterval time.Duration) {
	checkInterval := time.Second
	if threshold > 0 && threshold < checkInterval {
		checkInterval = threshold
	}
	if reminderInterval > 0 && reminderInterval < checkInterval {
		checkInterval = reminderInterval
	}
	if checkInterval <= 0 {
		checkInterval = time.Second
	}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			if elapsed, msgID, topic, initial, due := watch.warningDue(now, threshold, reminderInterval); due {
				if initial {
					log.Logger.Warnf("SDK read-stream processing slow/blocked; subscriber cannot consume until it returns. region=%s stream=%s subID=%s msgID=%s topic=%s elapsedSec=%d",
						c.config.Domain, sub.stream, sub.id, msgID, topic, int(elapsed.Seconds()))
				} else {
					log.Logger.Warnf("SDK read-stream processing still blocked; subscriber cannot consume until it returns. region=%s stream=%s subID=%s msgID=%s topic=%s elapsedSec=%d",
						c.config.Domain, sub.stream, sub.id, msgID, topic, int(elapsed.Seconds()))
				}
			}
		}
	}
}

type subscriptionReq struct {
	GroupID string   `json:"groupId"`
	Streams []string `json:"streams"`
}

type subscriptionResp struct {
	ID string `json:"_id"`
}

func (c *internalConnection) createSubscription(stream string) (string, error) {
	subReq := subscriptionReq{
		GroupID: c.config.GroupID,
		Streams: []string{stream},
	}
	subResp := subscriptionResp{}
	u := url.URL{
		Scheme: HttpScheme,
		Host:   c.config.Domain,
		Path:   apiPaths.subscriptions,
	}
	authValue, err := c.authHeader.provider()
	if err != nil {
		log.Logger.Errorf("Failed to obtain auth header: %v", err)
		return "", err
	}
	resp, err := c.restClient.R().
		SetHeader(c.authHeader.key, string(authValue)).
		SetBody(subReq).
		SetResult(&subResp).
		Post(u.String())
	if err != nil {
		return "", fmt.Errorf("failed to create subscription for %s: %v", stream, err)
	}

	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		log.Logger.Errorf("Received unexpected response '%s' while creating the subscription", resp.Status())
		return "", fmt.Errorf("received unexpected response '%s' while creating the subscription", resp.Status())
	}

	log.Logger.Debugf("Subscription created: %+v", subResp)
	if subResp.ID == "" {
		return "", fmt.Errorf("received empty subscriptions ID")
	}

	return subResp.ID, nil
}

func (c *internalConnection) deleteSubscription(id string) error {
	log.Logger.Debugf("Deleting subscription '%s'", id)
	u := url.URL{
		Scheme: HttpScheme,
		Host:   c.config.Domain,
		Path:   path.Join(apiPaths.subscriptions, id),
	}

	token, err := c.authHeader.provider()
	if err != nil {
		return fmt.Errorf("failed to obtain auth header for subscription %s deletion: %v", id, err)
	}
	resp, err := c.restClient.R().
		SetHeader(c.authHeader.key, string(token)).
		Delete(u.String())
	if err != nil || (resp.StatusCode() < 200 && resp.StatusCode() >= 300) {
		return fmt.Errorf("failed to delete subscription: %v", err)
	}

	return nil
}
