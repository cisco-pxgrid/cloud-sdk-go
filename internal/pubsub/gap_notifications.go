// Copyright (c) 2026, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"sync"
	"time"
)

// ReadStreamGapState identifies whether a confirmed read-stream gap started or recovered.
type ReadStreamGapState string

const (
	ReadStreamGapDetected  ReadStreamGapState = "detected"
	ReadStreamGapRecovered ReadStreamGapState = "recovered"
)

// ReadStreamGapReason identifies the SDK condition that confirmed the gap.
type ReadStreamGapReason string

const (
	ReadStreamGapReasonBrokerResponseTimeout  ReadStreamGapReason = "broker_response_timeout"
	ReadStreamGapReasonProcessingBackpressure ReadStreamGapReason = "processing_backpressure"
)

// ReadStreamGapEvent is the internal event forwarded to the public App callback.
type ReadStreamGapEvent struct {
	State          ReadStreamGapState
	Reason         ReadStreamGapReason
	Region         string
	Stream         string
	Duration       time.Duration
	ReconnectCount int64
	OccurredAt     time.Time
}

type readStreamGapLifecycle struct {
	active                 bool
	region                 string
	reason                 ReadStreamGapReason
	startedAt              time.Time
	connectionReady        bool
	brokerResponseAfterGap bool
	reconnectCount         int64
}

// ReadStreamGapTracker is shared by successive Connection instances for one region. This allows a
// gap detected by an old connection to recover after either the internal resubscribe path or the
// App-level connection rebuild path receives its first broker response.
//
// The type is exported only because package cloud wires it into the internal pubsub Config. The
// Go internal-package boundary prevents it from becoming customer API.
type ReadStreamGapTracker struct {
	mu             sync.Mutex
	handler        func(ReadStreamGapEvent)
	streams        map[string]*readStreamGapLifecycle
	reconnectCount int64
}

func NewReadStreamGapTracker(handler func(ReadStreamGapEvent)) *ReadStreamGapTracker {
	if handler == nil {
		return nil
	}
	return &ReadStreamGapTracker{
		handler: handler,
		streams: make(map[string]*readStreamGapLifecycle),
	}
}

// detect emits one edge-triggered notification for a confirmed timeout. Repeated timeout signals
// remain part of the same gap until a re-established subscription receives a broker response.
func (t *ReadStreamGapTracker) detect(region, stream string, reason ReadStreamGapReason, observedDuration time.Duration) {
	if t == nil {
		return
	}
	if observedDuration < 0 {
		observedDuration = 0
	}
	now := time.Now()
	t.mu.Lock()
	lifecycle := t.streams[stream]
	if lifecycle == nil {
		lifecycle = &readStreamGapLifecycle{}
		t.streams[stream] = lifecycle
	}
	if lifecycle.active {
		t.mu.Unlock()
		return
	}
	*lifecycle = readStreamGapLifecycle{
		active:    true,
		region:    region,
		reason:    reason,
		startedAt: now.Add(-observedDuration),
	}
	handler := t.handler
	event := ReadStreamGapEvent{
		State:          ReadStreamGapDetected,
		Reason:         reason,
		Region:         region,
		Stream:         stream,
		Duration:       observedDuration,
		ReconnectCount: t.reconnectCount,
		OccurredAt:     now,
	}
	t.mu.Unlock()
	handler(event)
}

// subscriptionStarted clears attempt-scoped evidence before a new subscriber begins polling.
func (t *ReadStreamGapTracker) subscriptionStarted(stream string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	lifecycle := t.streams[stream]
	if lifecycle == nil || !lifecycle.active {
		return
	}
	// Evidence from an attempt that did not become ready cannot recover a later attempt.
	lifecycle.connectionReady = false
	lifecycle.brokerResponseAfterGap = false
}

// connectionReady records that reconnect and subscription reuse/recreation completed. Recovery is
// emitted only after this point and after a subsequent broker response; either observation may
// arrive first because the subscriber starts as part of subscription creation.
func (t *ReadStreamGapTracker) connectionReady(stream string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	lifecycle := t.streams[stream]
	if lifecycle == nil || !lifecycle.active {
		t.mu.Unlock()
		return
	}
	if !lifecycle.connectionReady {
		t.reconnectCount++
		lifecycle.reconnectCount = t.reconnectCount
		lifecycle.connectionReady = true
	}
	event, handler, emit := t.recoveryEventLocked(stream, lifecycle, time.Now())
	t.mu.Unlock()
	if emit {
		handler(event)
	}
}

func (t *ReadStreamGapTracker) brokerResponse(stream string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	lifecycle := t.streams[stream]
	if lifecycle == nil || !lifecycle.active {
		t.mu.Unlock()
		return
	}
	lifecycle.brokerResponseAfterGap = true
	event, handler, emit := t.recoveryEventLocked(stream, lifecycle, time.Now())
	t.mu.Unlock()
	if emit {
		handler(event)
	}
}

func (t *ReadStreamGapTracker) recoveryEventLocked(stream string, lifecycle *readStreamGapLifecycle, now time.Time) (ReadStreamGapEvent, func(ReadStreamGapEvent), bool) {
	if !lifecycle.connectionReady || !lifecycle.brokerResponseAfterGap {
		return ReadStreamGapEvent{}, nil, false
	}
	event := ReadStreamGapEvent{
		State:          ReadStreamGapRecovered,
		Reason:         lifecycle.reason,
		Region:         lifecycle.region,
		Stream:         stream,
		Duration:       now.Sub(lifecycle.startedAt),
		ReconnectCount: lifecycle.reconnectCount,
		OccurredAt:     now,
	}
	lifecycle.active = false
	return event, t.handler, true
}
