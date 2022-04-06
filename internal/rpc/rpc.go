/*
 * Copyright (c) 2021, Cisco Systems, Inc.
 * All rights reserved.
 */

// Package rpc implements JSON RPC protocol used by DxHub PubSub
package rpc

import (
	"fmt"

	json "github.com/goccy/go-json"
	"github.com/google/uuid"
)

const (
	jsonRPCVersion      = "2.0"
	ResultStatusSuccess = "success"
)

// Method represents a jsonrpc method call
type Method string

const (
	MethodOpen    Method = "open"
	MethodClose   Method = "close"
	MethodConsume Method = "consume"
	MethodPublish Method = "publish"
)

// Request represents a jsonrpc request
type Request struct {
	Version string          `json:"jsonrpc"`
	ID      string          `json:"id,omitempty"`
	Method  Method          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type controlParams struct {
	ClientID string `json:"clientId"`
}

// ConsumeParams represents the params of a consume request
type ConsumeParams struct {
	SubscriptionID string `json:"subscriptionId"`
	ConsumeContext string `json:"consumeContext"`
}

// PublishParams represents the params of a publish request
type PublishParams struct {
	MsgID   string            `json:"msgId,omitempty"`
	Stream  string            `json:"stream"`
	Payload string            `json:"payload"`
	Headers map[string]string `json:"headers"`
}

// NewRequestFromBytes creates a new Request out of a payload in bytes
func NewRequestFromBytes(data []byte) (*Request, error) {
	var r Request
	err := json.Unmarshal(data, &r)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal request: %v", err)
	}

	return &r, nil
}

func newRequest(method Method, params interface{}) (*Request, error) {
	req := &Request{
		Version: jsonRPCVersion,
		Method:  method,
		ID:      uuid.New().String(),
	}
	paramsMarshalled, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	req.Params = paramsMarshalled

	return req, nil
}

// NewOpenRequest returns a new open request
func NewOpenRequest(clientId string) (*Request, error) {
	return newRequest(MethodOpen, controlParams{ClientID: clientId})
}

// NewCloseRequest returns a new close request
func NewCloseRequest(clientId string) (*Request, error) {
	return newRequest(MethodClose, controlParams{ClientID: clientId})
}

// NewConsumeRequest creates and returns a new consume request
func NewConsumeRequest(subID, consumeCtx string) (*Request, error) {
	c := ConsumeParams{
		SubscriptionID: subID,
		ConsumeContext: consumeCtx,
	}
	return newRequest(MethodConsume, c)
}

// NewPublishRequest returns a new publish request
func NewPublishRequest(stream string, headers map[string]string, payload string) (*Request, error) {
	p := PublishParams{
		MsgID:   uuid.NewString(),
		Stream:  stream,
		Payload: payload,
		Headers: headers,
	}

	return newRequest(MethodPublish, []PublishParams{p})
}

// ConsumeParams retrieves the params from a consume request
func (req *Request) ConsumeParams() (*ConsumeParams, error) {
	var params *ConsumeParams
	err := json.Unmarshal(req.Params, &params)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal consumer params: %v", err)
	}
	return params, nil
}

// PublishParams retrieves the params of a publish request
func (req *Request) PublishParams() ([]*PublishParams, error) {
	var params []*PublishParams
	err := json.Unmarshal(req.Params, &params)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal publish params: %v", err)
	}
	return params, nil
}

func (r *Request) Bytes() []byte {
	b, _ := json.Marshal(r)
	return b
}

func (r *Request) String() string {
	return string(r.Bytes())
}

// Response represents a jsonrpc response
type Response struct {
	Version string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   Error           `json:"error,omitempty"`
}

// Error represents a jsonrpc error
type Error struct {
	Code    int             `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// ControlResult represents the result of a control request
type ControlResult struct {
	Status string `json:"status"`
}

// ConsumeMessage represents a message received from the server in a consume response
type ConsumeMessage struct {
	MsgID   string            `json:"msgId"`
	Payload string            `json:"payload"`
	Headers map[string]string `json:"headers"`
}

// ConsumeResult represents the result of a consume request
type ConsumeResult struct {
	ConsumeContext string                      `json:"consumeContext"`
	SubscriptionID string                      `json:"subscriptionId"`
	Messages       map[string][]ConsumeMessage `json:"messages"`
}

// PublishResult represents the result of a publish request
type PublishResult struct {
	Status string `json:"status"`
	MsgID  string `json:"msgId"`
	Method Method `json:"method"`
}

// NewResponseFromBytes creates a new Response from supplied []byte
func NewResponseFromBytes(payload []byte) (*Response, error) {
	var res Response
	err := json.Unmarshal(payload, &res)
	if err != nil {
		return nil, err
	}

	return &res, nil
}

// NewControlResponse creates and returns a new control response
func NewControlResponse(id string, success bool, err Error) *Response {
	r := &Response{
		Version: jsonRPCVersion,
		ID:      id,
	}

	if success {
		res := ControlResult{
			Status: ResultStatusSuccess,
		}
		resBytes, _ := json.Marshal(res)
		r.Result = resBytes
	} else {
		r.Error = err
	}
	return r
}

// NewConsumeResponse creates and returns a new consume response
func NewConsumeResponse(id, consumeCtx, subID, stream string, msgs []ConsumeMessage) *Response {
	result := ConsumeResult{
		ConsumeContext: consumeCtx,
		SubscriptionID: subID,
		Messages: map[string][]ConsumeMessage{
			stream: msgs,
		},
	}

	b, _ := json.Marshal(&result)

	resp := &Response{
		Version: jsonRPCVersion,
		ID:      id,
		Result:  b,
	}

	return resp
}

// NewPublishResponse creates and returns a new publish response
func NewPublishResponse(id, msgID string, err error) *Response {
	if err != nil {
		return NewErrorResponse(id, err)
	}

	result := PublishResult{
		Method: MethodPublish,
		MsgID:  msgID,
		Status: ResultStatusSuccess,
	}
	b, _ := json.Marshal(&result)
	resp := &Response{
		Version: jsonRPCVersion,
		ID:      id,
		Result:  b,
	}
	return resp
}

// NewErrorResponse creates and returns a new Error response
func NewErrorResponse(id string, err error) *Response {
	return &Response{
		Version: jsonRPCVersion,
		ID:      id,
		Error: Error{
			Code:    -32099,
			Message: err.Error(),
		},
	}
}

// ControlResult retrives the result from a control response
func (resp *Response) ControlResult() (*ControlResult, error) {
	var result ControlResult
	err := json.Unmarshal(resp.Result, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal control result: %v", err)
	}
	if result.Status != ResultStatusSuccess {
		return nil, fmt.Errorf("received unknown result: %v", result.Status)
	}
	return &result, nil
}

// ConsumeResult retrieves the result from a consume response
func (resp *Response) ConsumeResult() (*ConsumeResult, error) {
	var result ConsumeResult
	err := json.Unmarshal(resp.Result, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal consume result: %v", err)
	}
	return &result, nil
}

// PublishResult retrives the result from a publish response
func (resp *Response) PublishResult() (*PublishResult, error) {
	var result PublishResult
	err := json.Unmarshal(resp.Result, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal publish result: %v", err)
	}
	return &result, nil
}

func (r *Response) Bytes() []byte {
	b, _ := json.Marshal(r)
	return b
}

func (r *Response) String() string {
	return string(r.Bytes())
}
