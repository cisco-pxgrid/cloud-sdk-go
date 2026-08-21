// Copyright (c) 2026, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

func decodeConsumeOffset(consumeCtx, stream string) (partition, offset int64, ok bool) {
	if consumeCtx == "" {
		return 0, 0, false
	}
	var raw []byte
	var err error
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
	var cursor consumeCtxStreams
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return 0, 0, false
	}
	for _, cursorStream := range cursor.Streams {
		if cursorStream.Stream == stream {
			return cursorStream.Partition, cursorStream.Offset, true
		}
	}
	return 0, 0, false
}

type streamDiagnosticState string

const (
	streamStateAwaitingActivity     streamDiagnosticState = "awaiting_activity"
	streamStateHealthyDelivery      streamDiagnosticState = "healthy_delivery"
	streamStateBrokerQuiet          streamDiagnosticState = "broker_quiet"
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

// classifyStream is diagnostic-only. Missing broker responses are handled by the established
// consume timeout; this classifier focuses on delivery progress and blocked SDK processing.
func classifyStream(now time.Time, disconnected, reconnecting bool, current streamStatsSnapshot, delta streamStatsDelta, sdkProcessingThreshold time.Duration) streamDiagnosticState {
	if sdkProcessingThreshold > 0 && current.sdkProcessingActive && elapsedSince(now, current.lastSDKProcessingAt) >= sdkProcessingThreshold {
		return streamStateSDKProcessingBlocked
	}
	if reconnecting {
		return streamStateReconnecting
	}
	if disconnected {
		return streamStateDisconnected
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

// statusLogger periodically emits non-destructive interval deltas, cumulative totals, and an
// evidence-based state for each read stream. It emits one final snapshot before stopping.
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
		case now := <-ticker.C:
			previous = c.logReadStreamStatus(now, interval, previous, previousStates)
		case <-ctx.Done():
			c.logReadStreamStatus(time.Now(), interval, previous, previousStates)
			log.Logger.Infof("Read-stream status logger stopped. region=%s groupId=%s reason=%v",
				c.config.Domain, c.config.GroupID, ctx.Err())
			return
		}
	}
}

func (c *Connection) logReadStreamStatus(now time.Time, interval time.Duration, previous map[string]streamStatsSnapshot, previousStates map[string]streamDiagnosticState) map[string]streamStatsSnapshot {
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
		state := classifyStream(now, disconnected, reconnecting, current, delta, sdkProcessingSlowWarnThreshold)
		partition, offset := int64(-1), int64(-1)
		if p, o, ok := decodeConsumeOffset(current.lastConsumeCtx, stream); ok {
			partition, offset = p, o
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
			delta.cursorChanges, current.cursorChanges, int(elapsedSince(now, current.lastCursorChangeAt).Seconds()),
			int(elapsedSince(now, current.lastBrokerResponseAt).Seconds()), int(elapsedSince(now, current.lastMessageAt).Seconds()),
			int(interval.Seconds()), disconnected, reconnecting, partition, offset, current.lastConsumeCtx)
		streams += stream + " "

		if prior, exists := previousStates[stream]; diagnosticStateChanged(prior, state, exists) {
			log.Logger.Infof("Read-stream diagnostic state changed. region=%s stream=%s subID=%s previous=%s state=%s consumeIters=%d received=%d noBrokerResponseForSec=%d sdkProcessingActive=%t sdkProcessingForSec=%d sdkProcessingMsgID=%s sdkProcessingTopic=%s disconnected=%t reconnecting=%t partition=%d offset=%d",
				c.config.Domain, stream, sub.subscriptionID, prior, state,
				delta.brokerResponses, delta.brokerMessages, int(elapsedSince(now, current.lastBrokerResponseAt).Seconds()),
				current.sdkProcessingActive, int(sdkProcessingFor.Seconds()), current.sdkProcessingMsgID, current.sdkProcessingTopic,
				disconnected, reconnecting, partition, offset)
		}
		previousStates[stream] = state
	}

	connectedFor := 0
	if !snapshot.connectedSince.IsZero() {
		connectedFor = int(now.Sub(snapshot.connectedSince).Seconds())
	}
	log.Logger.Infof("Read-stream status. region=%s groupId=%s disconnected=%t connectedForSec=%d reconnectCount=%d streams=[%s]",
		c.config.Domain, c.config.GroupID, disconnected, connectedFor, snapshot.reconnectCount, streams)
	return snapshot.streams
}
