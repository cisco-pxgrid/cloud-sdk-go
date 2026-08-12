// Copyright (c) 2026, Cisco Systems, Inc.
// All rights reserved.

package pubsub

import (
	"context"
	"sync"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/log"
)

// sdkProcessingSlowWarnThreshold is how long the synchronous SDK subscription callback may run
// before the pub/sub layer logs a warning. The actual user DeviceMessageHandler is timed separately.
var sdkProcessingSlowWarnThreshold = 10 * time.Second

// sdkProcessingWatchInterval limits repeated warnings while SDK processing remains blocked.
var sdkProcessingWatchInterval = 15 * time.Second

type sdkProcessingWatch struct {
	mu          sync.Mutex
	start       time.Time
	lastWarning time.Time
	msgID       string
	topic       string
}

func (w *sdkProcessingWatch) begin(msgID, topic string) {
	w.mu.Lock()
	w.start = time.Now()
	w.lastWarning = time.Time{}
	w.msgID = msgID
	w.topic = topic
	w.mu.Unlock()
}

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

// runSDKProcessingWatchdog reports blocked SDK subscription processing. It is deliberately
// independent of internalConnection and stops with the subscription context.
func runSDKProcessingWatchdog(ctx context.Context, logger log.SDKLogger, region, stream, subID string, watch *sdkProcessingWatch, threshold, reminderInterval time.Duration) {
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
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			elapsed, msgID, topic, initial, due := watch.warningDue(now, threshold, reminderInterval)
			if !due {
				continue
			}
			if initial {
				logger.Warnf("SDK read-stream processing slow/blocked; subscriber cannot consume until it returns. region=%s stream=%s subID=%s msgID=%s topic=%s elapsedSec=%d",
					region, stream, subID, msgID, topic, int(elapsed.Seconds()))
			} else {
				logger.Warnf("SDK read-stream processing still blocked; subscriber cannot consume until it returns. region=%s stream=%s subID=%s msgID=%s topic=%s elapsedSec=%d",
					region, stream, subID, msgID, topic, int(elapsed.Seconds()))
			}
		}
	}
}
