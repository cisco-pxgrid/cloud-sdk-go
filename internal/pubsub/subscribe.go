// Copyright (c) 2021, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"context"
	"encoding/base64"
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
		log.Logger.Errorf("Consume timeout. Using SubscriptionID=%s", id)
	} else {
		var err error
		id, err = c.createSubscription(stream)
		if err != nil {
			return "", fmt.Errorf("failed to create subscription for %s: %v", stream, err)
		}
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

	c.wg.Add(1)
	sub.wg.Add(1)
	go c.subscriber(sub)

	return id, nil
}

// unsubscribe unsubscribes from a DxHub Pubsub Stream
func (c *internalConnection) unsubscribe(stream string, deleteSub bool) error {
	c.subs.Lock()
	defer c.subs.Unlock()

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
	log.Logger.Debugf("Successfully unsubscribed from stream %s", stream)
	return nil
}

func (c *internalConnection) sendConsumeMessage(subscriptionId, consumeCtx string) (<-chan *rpc.Response, error) {
	req, err := rpc.NewConsumeRequest(subscriptionId, consumeCtx)
	if err != nil {
		return nil, err
	}
	respCh := make(chan *rpc.Response, 1) // we expect 1 response back
	err = c.sendMessage(req, func(resp *rpc.Response) {
		respCh <- resp
		close(respCh)
	})
	if err != nil {
		return nil, err
	}
	return respCh, err
}

type subscription struct {
	stream    string
	id        string
	callback  SubscriptionCallback
	ctx       context.Context
	ctxCancel context.CancelFunc
	wg        sync.WaitGroup
}

// subscriber goroutine is spawned for each subscription to a stream
func (c *internalConnection) subscriber(sub *subscription) {
	defer sub.wg.Done()
	defer c.wg.Done()
	log.Logger.Debugf("Starting subscriber thread for %s", sub.stream)

	consumeCtx := ""
loop:
	for {
		// send consume message for requesting data from the server
		respCh, err := c.sendConsumeMessage(sub.id, consumeCtx)
		if err != nil {
			log.Logger.Errorf("Failed to start consumption for stream %s: %v", sub.stream, err)
			sub.callback(err, "", nil, nil)
		} else {
			select {
			case resp := <-respCh:
				// received consume response from the processor
				if resp.Error.Code != 0 {
					log.Logger.Errorf("Consume error for stream %s: %v", sub.stream, resp.Error)
					sub.callback(fmt.Errorf("consume error: %v", resp.Error), resp.ID, nil, nil)
					break
				}
				res, err := resp.ConsumeResult()
				if err != nil {
					log.Logger.Errorf("Consume error for stream %s: %v", sub.stream, err)
					sub.callback(fmt.Errorf("consume error: %v", err), resp.ID, nil, nil)
					break
				}
				consumeCtx = res.ConsumeContext
				for stream, messages := range res.Messages {
					if stream != sub.stream {
						log.Logger.Errorf("Received consume message for stream %s, was expecting messages for stream %s", stream, sub.stream)
						continue
					}
					for _, m := range messages {
						payload, err := base64.StdEncoding.DecodeString(m.Payload)
						sub.callback(err, m.MsgID, m.Headers, payload)
					}
				}
			case <-time.After(consumeResponseTimeout):
				// WORKAROUND Consume timeout causes message drop. Workaround by reconnect
				log.Logger.Warnf("Consume timeout. Disconnecting")
				c.consumeTimeout = true
				// This requires a go routine otherwise the waitgroup blocks forever
				go c.disconnect()
				break loop
			case <-sub.ctx.Done():
				// user unsubscribed from the stream
				break loop
			}
		}
		select {
		case <-sub.ctx.Done():
			// user unsubscribed from the stream
			break loop
		case <-time.After(c.config.PollInterval):
		}
	}
	log.Logger.Debugf("Stopped subscriber thread for %s", sub.stream)
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
		Scheme: httpScheme,
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
		Scheme: httpScheme,
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
