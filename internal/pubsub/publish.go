// Copyright (c) 2021, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"

	"github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"
	"github.com/cisco-pxgrid/cloud-sdk-go/log"
)

type msgRequest struct {
	req     *rpc.Request
	handler func(*rpc.Response)
}

// PublishResult represents the result of a publish request from the server
type PublishResult struct {
	ID    string // Message ID
	Error error  // Error shall be non-nil in case of an error
}

type pubResultAck struct {
	ch chan *PublishResult
	sync.Mutex
}

func (p *PublishResult) String() string {
	return fmt.Sprintf("PublishResult[ID: %s, Error: %v]", p.ID, p.Error)
}

func (c *internalConnection) sendPublishMessage(stream string, headers map[string]string, payload string, ack *pubResultAck) (string, error) {
	// Create a new request for publishing the message
	req, err := rpc.NewPublishRequest(stream, headers, payload)
	if err != nil {
		log.Logger.Errorf("Failed to create message for publish: %v", err)
		return "", err
	}

	var handler func(*rpc.Response) = nil
	ack.Lock()
	if ack.ch != nil {
		// define the handler
		handler = func(resp *rpc.Response) {
			pr := &PublishResult{ID: resp.ID}
			// this lock gets activated when handler is invoked, this is acquired in a different
			// context than the one above
			ack.Lock()
			defer ack.Unlock()

			// Have to perform channel check here again because by the time the response is received
			// the user might've given up and set the ack channel to nil
			if ack.ch != nil {
				// Create PublishResult based on the response
				if resp.Error.Code == 0 {
					rpcResult, rpcErr := resp.PublishResult()
					if rpcErr != nil {
						pr.Error = rpcErr
					} else if rpcResult.Status != rpc.ResultStatusSuccess {
						pr.Error = fmt.Errorf(rpcResult.Status)
					} else {
						pr.Error = nil
					}
				} else {
					pr.Error = fmt.Errorf("%d: %s", resp.Error.Code, resp.Error.Message)
				}

				// Send PublishResult back to the user
				select {
				case ack.ch <- pr:
				default:
				}
			}
		}
	}
	ack.Unlock()

	// Send the message over the network
	err = c.sendMessage(req, handler)
	if err != nil {
		return "", err
	}
	return req.ID, nil
}

// Publish publishes a message to the stream asynchronously.
func (c *internalConnection) Publish(ctx context.Context, stream string, headers map[string]string, payload []byte) (*PublishResult, error) {
	ack := &pubResultAck{
		ch: make(chan *PublishResult),
	}
	id, err := c.sendPublishMessage(stream, headers, base64.StdEncoding.EncodeToString(payload), ack)
	if err != nil {
		return nil, fmt.Errorf("publish failure: %v", err)
	}

	select {
	case r := <-ack.ch:
		return r, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out waiting for publish response for message %s", id)
	}
}

// PublishAsync publishes a message to the stream asynchronously.
// Response can be monitored on the supplied channel. The cancel function must be invoked before closing the channel.
func (c *internalConnection) PublishAsync(stream string, headers map[string]string, payload []byte, result chan *PublishResult) (msgID string, cancel func(), err error) {
	ack := &pubResultAck{
		ch: result,
	}
	id, err := c.sendPublishMessage(stream, headers, base64.StdEncoding.EncodeToString(payload), ack)
	if err != nil {
		return "", nil, fmt.Errorf("publish failure: %v", err)
	}

	cancel = func() {
		ack.Lock()
		defer ack.Unlock()

		ack.ch = nil
	}

	return id, cancel, nil
}
