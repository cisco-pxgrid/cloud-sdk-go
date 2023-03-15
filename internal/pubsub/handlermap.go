package pubsub

import (
	"fmt"
	"sync"
	"time"

	rpc "github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"
)

// handlerMap is thread safe and expires entries
// Using 2 maps to expire entries instead of keeping track of timestamps for all entries
// olderHandlers are still valid. When moving current to older, the entries are aged from 0 to atLeast
// No thread running. Expiry checks only when Get or Set is called
// This is constantly getting called as the code consumes every second
type handlerMap struct {
	expireDuration         time.Duration
	newExpireTime          time.Time
	currentHandlers map[string]func(*rpc.Response)
	olderHandlers   map[string]func(*rpc.Response)
	sync.RWMutex
}

func NewHandlerMap(expireDuration time.Duration) *handlerMap {
	return &handlerMap{
		currentHandlers: make(map[string]func(*rpc.Response)),
		olderHandlers:   make(map[string]func(*rpc.Response)),
		expireDuration:         expireDuration,
		newExpireTime:          time.Now().Add(expireDuration),
	}
}

func (h *handlerMap) GetAndDelete(id string) func(*rpc.Response) {
	h.expireCheck()
	h.RLock()
	defer h.RUnlock()
	handler := h.currentHandlers[id]
	delete(h.currentHandlers, id)
	if handler == nil {
		handler = h.olderHandlers[id]
		delete(h.olderHandlers, id)
	}
	return handler
}

func (h *handlerMap) Set(id string, handler func(*rpc.Response)) {
	h.expireCheck()
	h.Lock()
	defer h.Unlock()
	h.currentHandlers[id] = handler
}

// expireCheck deletes older and moves current to older
func (h *handlerMap) expireCheck() {
	if time.Now().Before(h.newExpireTime) {
		return
	}
	h.Lock()
	h.newExpireTime = time.Now().Add(h.expireDuration)
	expiredHandlers := h.olderHandlers
	h.olderHandlers = h.currentHandlers
	h.currentHandlers = make(map[string]func(*rpc.Response))
	h.Unlock()
	for id, handler := range expiredHandlers {
		handler(rpc.NewErrorResponse(id, fmt.Errorf("timed out waiting for response from server")))
	}
}
