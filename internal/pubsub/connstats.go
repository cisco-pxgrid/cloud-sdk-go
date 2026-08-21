// Copyright (c) 2026, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"sync"
	"time"
)

// streamStats contains cumulative counters and last-known timestamps for one stream. Counters are
// never reset by readers: each observer can safely calculate its own interval deltas.
type streamStats struct {
	active               bool
	subscribedAt         time.Time
	lastBrokerResponseAt time.Time
	lastMessageAt        time.Time
	lastCursorChangeAt   time.Time
	lastSDKProcessingAt  time.Time
	brokerResponses      int64
	brokerMessages       int64
	dispatchStarts       int64
	sdkProcessingStarts  int64
	sdkProcessingEnds    int64
	cursorChanges        int64
	lastConsumeCtx       string
	sdkProcessingActive  bool
	sdkProcessingMsgID   string
	sdkProcessingTopic   string
}

// streamStatsSnapshot is an immutable point-in-time copy returned to diagnostic observers.
type streamStatsSnapshot streamStats

type connStatsSnapshot struct {
	connectedSince time.Time
	reconnectCount int
	streams        map[string]streamStatsSnapshot
}

// connStats tracks read-stream delivery and connection-status metrics for diagnostic logging. A
// single instance is shared across reconnects via Config.stats.
type connStats struct {
	mu             sync.Mutex
	connectedSince time.Time
	reconnectCount int
	streams        map[string]*streamStats
}

func newConnStats() *connStats {
	return &connStats{streams: make(map[string]*streamStats)}
}

// getOrCreateStreamLocked requires s.mu to be held by the caller.
func (s *connStats) getOrCreateStreamLocked(stream string) *streamStats {
	stats, ok := s.streams[stream]
	if !ok {
		stats = &streamStats{}
		s.streams[stream] = stats
	}
	return stats
}

// recordConnected marks the time the underlying connection was established.
func (s *connStats) recordConnected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connectedSince = time.Now()
}

func (s *connStats) recordReconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconnectCount++
}

// recordBrokerResponse records a decoded broker response before it is handed to the subscriber.
// This remains truthful even when subscriber dispatch or an application callback blocks.
func (s *connStats) recordBrokerResponse(stream, consumeCtx string, messageCount int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := s.getOrCreateStreamLocked(stream)
	now := time.Now()
	stats.brokerResponses++
	stats.lastBrokerResponseAt = now
	if messageCount > 0 {
		stats.brokerMessages += int64(messageCount)
		stats.lastMessageAt = now
	}
	if consumeCtx != "" && consumeCtx != stats.lastConsumeCtx {
		stats.cursorChanges++
		stats.lastCursorChangeAt = now
	}
	stats.lastConsumeCtx = consumeCtx
}

func (s *connStats) recordDispatch(stream string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getOrCreateStreamLocked(stream).dispatchStarts++
}

func (s *connStats) recordSDKProcessingStart(stream, msgID, topic string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := s.getOrCreateStreamLocked(stream)
	stats.sdkProcessingStarts++
	stats.sdkProcessingActive = true
	stats.lastSDKProcessingAt = time.Now()
	stats.sdkProcessingMsgID = msgID
	stats.sdkProcessingTopic = topic
}

func (s *connStats) recordSDKProcessingComplete(stream string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := s.getOrCreateStreamLocked(stream)
	stats.sdkProcessingEnds++
	stats.sdkProcessingActive = false
}

// beginStreamLifecycle starts all age baselines at the successful subscription time. Cumulative
// counters survive subscribe and reconnect boundaries, while current processing identity is reset.
func (s *connStats) beginStreamLifecycle(stream string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := s.getOrCreateStreamLocked(stream)
	now := time.Now()
	stats.active = true
	stats.subscribedAt = now
	stats.lastBrokerResponseAt = now
	stats.lastMessageAt = now
	stats.lastCursorChangeAt = now
	stats.lastSDKProcessingAt = now
	stats.lastConsumeCtx = ""
	stats.sdkProcessingActive = false
	stats.sdkProcessingMsgID = ""
	stats.sdkProcessingTopic = ""
}

// endStreamLifecycle marks the stream inactive while preserving its last evidence for diagnostics.
func (s *connStats) endStreamLifecycle(stream string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats, ok := s.streams[stream]
	if !ok {
		return
	}
	stats.active = false
	stats.sdkProcessingActive = false
}

func (s *connStats) reconnectSnapshot() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconnectCount
}

func (s *connStats) sinceLastMessage(stream string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats, ok := s.streams[stream]
	if !ok || !stats.active {
		return 0
	}
	return time.Since(stats.lastMessageAt)
}

func (s *connStats) sinceLastBrokerResponse(stream string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats, ok := s.streams[stream]
	if !ok || !stats.active || stats.lastBrokerResponseAt.IsZero() {
		return 0
	}
	return time.Since(stats.lastBrokerResponseAt)
}

// snapshot returns a non-destructive point-in-time copy. Cumulative source counters remain
// available to every diagnostic observer.
func (s *connStats) snapshot() connStatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := connStatsSnapshot{
		connectedSince: s.connectedSince,
		reconnectCount: s.reconnectCount,
		streams:        make(map[string]streamStatsSnapshot, len(s.streams)),
	}
	for stream, stats := range s.streams {
		snapshot.streams[stream] = streamStatsSnapshot(*stats)
	}
	return snapshot
}
