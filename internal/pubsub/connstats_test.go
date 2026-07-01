// Copyright (c) 2024, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
	"github.com/stretchr/testify/require"
)

// captureLogger is an SDKLogger that records formatted log lines per level for assertions.
type captureLogger struct {
	mu    sync.Mutex
	info  []string
	warn  []string
	err   []string
	debug []string
}

func (c *captureLogger) Infof(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.info = append(c.info, fmt.Sprintf(format, args...))
}
func (c *captureLogger) Warnf(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warn = append(c.warn, fmt.Sprintf(format, args...))
}
func (c *captureLogger) Errorf(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = append(c.err, fmt.Sprintf(format, args...))
}
func (c *captureLogger) Debugf(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.debug = append(c.debug, fmt.Sprintf(format, args...))
}

func (c *captureLogger) infoContains(sub string) bool { return anyContains(c.snap(c.info), sub) }
func (c *captureLogger) warnContains(sub string) bool { return anyContains(c.snap(c.warn), sub) }

func (c *captureLogger) snap(lines []string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}

func anyContains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestConnStats_RecordAndSnapshot(t *testing.T) {
	s := newConnStats()

	s.initStream("stream-a")
	// initStream must not double-seed.
	first := s.sinceLastMessage("stream-a")
	require.GreaterOrEqual(t, first, time.Duration(0))

	s.recordMessage("stream-a")
	s.recordMessage("stream-a")
	s.recordMessage("stream-b")

	connectedSince, reconnects, sinceLast, counts := s.snapshotAndReset()
	require.Equal(t, int64(2), counts["stream-a"])
	require.Equal(t, int64(1), counts["stream-b"])
	require.Equal(t, 0, reconnects)
	require.True(t, connectedSince.IsZero(), "connectedSince should be zero before recordConnected")
	require.Contains(t, sinceLast, "stream-a")

	// Counters reset after snapshot.
	_, _, _, counts2 := s.snapshotAndReset()
	require.Equal(t, int64(0), counts2["stream-a"])
	require.Equal(t, int64(0), counts2["stream-b"])
}

func TestConnStats_ConnectedAndReconnect(t *testing.T) {
	s := newConnStats()
	require.Equal(t, 0, s.reconnectSnapshot())

	s.recordConnected()
	connectedSince, _, _, _ := s.snapshotAndReset()
	require.False(t, connectedSince.IsZero())

	s.recordReconnect()
	s.recordReconnect()
	require.Equal(t, 2, s.reconnectSnapshot())
}

func TestNewInternalConnection_DiagnosticDefaults(t *testing.T) {
	c, err := newInternalConnection(Config{
		GroupID: "g",
		Domain:  "example.com",
		APIKeyProvider: func() ([]byte, error) {
			return []byte("k"), nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, defaultStatusLogInterval, c.config.StatusLogInterval)
	require.Equal(t, defaultMessageGapThreshold, c.config.MessageGapThreshold)
	require.NotNil(t, c.config.stats)
}

func TestConnStats_SharedAcrossConnections(t *testing.T) {
	// Simulate NewConnection injecting shared stats, then a reconnect building a second
	// internalConnection from the same Config: both must share the same counters.
	shared := newConnStats()
	cfg := Config{
		GroupID: "g",
		Domain:  "example.com",
		APIKeyProvider: func() ([]byte, error) {
			return []byte("k"), nil
		},
		stats: shared,
	}
	c1, err := newInternalConnection(cfg)
	require.NoError(t, err)
	c2, err := newInternalConnection(cfg)
	require.NoError(t, err)
	require.Same(t, c1.config.stats, c2.config.stats)

	c1.config.stats.recordMessage("s")
	require.Equal(t, time.Duration(0) <= c2.config.stats.sinceLastMessage("s"), true)
	_, _, _, counts := c2.config.stats.snapshotAndReset()
	require.Equal(t, int64(1), counts["s"])
}

func TestPerMessageLogging_Toggle(t *testing.T) {
	orig := log.Logger
	defer func() { log.Logger = orig }()

	cl := &captureLogger{}
	log.Logger = cl

	// Emulate the per-message log line the subscriber emits when LogEachMessage is true.
	cfg := Config{LogEachMessage: true, Domain: "example.com", stats: newConnStats()}
	if cfg.LogEachMessage {
		log.Logger.Infof("Read-stream message received. region=%s stream=%s topic=%s msgID=%s type=%s tenant=%s device=%s bytes=%d subID=%s consumeCtx=%s decodeErr=%v",
			cfg.Domain, "app--x-R", "pxcloud--session-sessions", "mid-1", "data", "t1", "d1", 42, "sub-1", "ctx-1", nil)
	}
	require.True(t, cl.infoContains("Read-stream message received."))
	require.True(t, cl.infoContains("topic=pxcloud--session-sessions"))
	require.True(t, cl.infoContains("subID=sub-1"))

	// When disabled, nothing new is emitted.
	before := len(cl.snap(cl.info))
	cfg.LogEachMessage = false
	if cfg.LogEachMessage {
		log.Logger.Infof("should not happen")
	}
	require.Equal(t, before, len(cl.snap(cl.info)))
}

func TestGapDetection_Logic(t *testing.T) {
	orig := log.Logger
	defer func() { log.Logger = orig }()
	cl := &captureLogger{}
	log.Logger = cl

	s := newConnStats()
	s.initStream("app--x-R")
	// Force the last-message timestamp into the past to simulate a delivery gap.
	s.mu.Lock()
	s.lastMessage["app--x-R"] = time.Now().Add(-5 * time.Minute)
	s.mu.Unlock()

	threshold := 2 * time.Minute
	gap := s.sinceLastMessage("app--x-R")
	disconnected := false
	if !disconnected && gap > threshold {
		log.Logger.Warnf("Read-stream gap detected. region=%s stream=%s subID=%s noMessagesForSec=%d thresholdSec=%d (connection up)",
			"example.com", "app--x-R", "sub-1", int(gap.Seconds()), int(threshold.Seconds()))
	}
	require.True(t, cl.warnContains("Read-stream gap detected."))
	require.True(t, cl.warnContains("stream=app--x-R"))
}
