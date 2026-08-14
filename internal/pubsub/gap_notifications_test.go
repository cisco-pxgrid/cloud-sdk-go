package pubsub

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadStreamGapTrackerEmitsDetectedAndRecoveredOnce(t *testing.T) {
	events := make(chan ReadStreamGapEvent, 4)
	tracker := NewReadStreamGapTracker(func(event ReadStreamGapEvent) {
		events <- event
	})

	tracker.detect("region.example.com", "stream-a", ReadStreamGapReasonBrokerResponseTimeout, 15*time.Second)
	tracker.detect("region.example.com", "stream-a", ReadStreamGapReasonBrokerResponseTimeout, 30*time.Second)

	detected := <-events
	require.Equal(t, ReadStreamGapDetected, detected.State)
	require.Equal(t, ReadStreamGapReasonBrokerResponseTimeout, detected.Reason)
	require.Equal(t, "region.example.com", detected.Region)
	require.Equal(t, "stream-a", detected.Stream)
	require.GreaterOrEqual(t, detected.Duration, 15*time.Second)
	require.Zero(t, detected.ReconnectCount)

	tracker.subscriptionStarted("stream-a")
	tracker.brokerResponse("stream-a")
	select {
	case event := <-events:
		t.Fatalf("recovery emitted before connection was ready: %+v", event)
	default:
	}

	tracker.connectionReady("stream-a")
	recovered := <-events
	require.Equal(t, ReadStreamGapRecovered, recovered.State)
	require.Equal(t, detected.Reason, recovered.Reason)
	require.Equal(t, detected.Region, recovered.Region)
	require.Equal(t, detected.Stream, recovered.Stream)
	require.GreaterOrEqual(t, recovered.Duration, detected.Duration)
	require.Equal(t, int64(1), recovered.ReconnectCount)

	tracker.connectionReady("stream-a")
	tracker.brokerResponse("stream-a")
	select {
	case event := <-events:
		t.Fatalf("duplicate recovery emitted: %+v", event)
	default:
	}
}

func TestReadStreamGapTrackerWaitsForPostReconnectBrokerResponse(t *testing.T) {
	events := make(chan ReadStreamGapEvent, 2)
	tracker := NewReadStreamGapTracker(func(event ReadStreamGapEvent) {
		events <- event
	})

	tracker.detect("region.example.com", "stream-a", ReadStreamGapReasonProcessingBackpressure, time.Minute)
	<-events
	tracker.subscriptionStarted("stream-a")
	tracker.connectionReady("stream-a")

	select {
	case event := <-events:
		t.Fatalf("recovery emitted without a broker response: %+v", event)
	default:
	}

	tracker.brokerResponse("stream-a")
	recovered := <-events
	require.Equal(t, ReadStreamGapRecovered, recovered.State)
	require.Equal(t, ReadStreamGapReasonProcessingBackpressure, recovered.Reason)
}

func TestReadStreamGapTrackerDoesNotReuseBrokerResponseFromFailedAttempt(t *testing.T) {
	events := make(chan ReadStreamGapEvent, 2)
	tracker := NewReadStreamGapTracker(func(event ReadStreamGapEvent) {
		events <- event
	})

	tracker.detect("region.example.com", "stream-a", ReadStreamGapReasonBrokerResponseTimeout, 15*time.Second)
	<-events

	tracker.subscriptionStarted("stream-a")
	tracker.brokerResponse("stream-a")
	// The first attempt never becomes ready. A later attempt must clear its broker evidence.
	tracker.subscriptionStarted("stream-a")
	tracker.connectionReady("stream-a")
	select {
	case event := <-events:
		t.Fatalf("stale broker response recovered a later subscription attempt: %+v", event)
	default:
	}

	tracker.brokerResponse("stream-a")
	recovered := <-events
	require.Equal(t, ReadStreamGapRecovered, recovered.State)
	require.Equal(t, int64(1), recovered.ReconnectCount)
}

func TestConsumeTimeoutErrorPreservesGapReason(t *testing.T) {
	err := newConsumeTimeoutError(ReadStreamGapReasonProcessingBackpressure)
	require.True(t, errors.Is(err, errConsumeTimeout))
	require.Equal(t, ReadStreamGapReasonProcessingBackpressure, consumeTimeoutReason(err))
	require.Equal(t, ReadStreamGapReasonBrokerResponseTimeout, consumeTimeoutReason(errConsumeTimeout))
}
