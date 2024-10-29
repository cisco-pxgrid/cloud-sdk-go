// Copyright (c) 2021, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"context"
	"fmt"

	"github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"
	"github.com/cisco-pxgrid/cloud-sdk-go/log"
)

func (c *internalConnection) sendOpenMessage() error {
	req, err := rpc.NewOpenRequest(c.id)
	if err != nil {
		return err
	}
	return c.sendControlMessage(req)
}

func (c *internalConnection) sendCloseMessage() error {
	req, err := rpc.NewCloseRequest(c.id)
	if err != nil {
		return err
	}
	return c.sendControlMessage(req)
}

func (c *internalConnection) sendControlMessage(req *rpc.Request) error {
	log.Logger.Debugf("Sending control message: %v", req)
	respCh := make(chan *rpc.Response, 1) // we expect 1 response back
	err := c.sendMessage(req, func(resp *rpc.Response) {
		log.Logger.Debugf("Received control message response: %v", resp)
		respCh <- resp
		close(respCh)
	})
	if err != nil {
		return fmt.Errorf("failed to send request %v: %v", req, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	select {
	case resp := <-respCh:
		_, err := resp.ControlResult()
		if err != nil {
			return fmt.Errorf("received unexpected response: %v, %v", resp, err)
		}
	case <-ctx.Done():
		return fmt.Errorf("sending request %v: timed out", req)
	}
	return nil
}

func (c *internalConnection) sendMessage(req *rpc.Request, handler func(resp *rpc.Response)) error {
	select {
	case c.writerCh <- &msgRequest{req: req, handler: handler}:
		return nil
	default:
		return fmt.Errorf("writer is busy")
	}
}
