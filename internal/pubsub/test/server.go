package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	rpc2 "github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"nhooyr.io/websocket"
)

type Config struct {
	PubSubPath        string
	SubscriptionsPath string
	RejectConn        bool
	PublishError      bool
	ConsumeError      bool
	ConsumeDrop       bool
}

type sub struct {
	stream string
	id     string
	params []rpc2.PublishParams
}

func (s *sub) String() string {
	return fmt.Sprintf("sub{stream:%s, id:%s, params:%+v}", s.stream, s.id, s.params)
}

var subs = map[string]*sub{}
var subsMu = sync.Mutex{}

// NewRPCServer creates and starts a test HTTP server that talks RPC
func NewRPCServer(t *testing.T, cfg Config) *httptest.Server {
	r := chi.NewRouter()

	// pubsub
	r.Get(cfg.PubSubPath, func(w http.ResponseWriter, r *http.Request) {
		if cfg.RejectConn {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		c, err := websocket.Accept(w, r, nil)
		assert.NoError(t, err)

		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := context.Background()
		for {
			mt, payload, err := c.Read(ctx)
			if err != nil {
				if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
					t.Errorf("Read error: %v", err)
				}
				break
			}

			req, err := rpc2.NewRequestFromBytes(payload)
			assert.NoError(t, err)

			var resp *rpc2.Response
			switch req.Method {
			case rpc2.MethodOpen, rpc2.MethodClose:
				resp = rpc2.NewControlResponse(req.ID, true, rpc2.Error{})
			case rpc2.MethodPublish:
				params, _ := req.PublishParams()
				if cfg.PublishError {
					resp = rpc2.NewErrorResponse(req.ID, fmt.Errorf("Publish Error"))
				} else {
					for _, p := range params {
						subsMu.Lock()
						s := subs[p.Stream]
						subsMu.Unlock()
						if s != nil {
							s.params = append(s.params, *p)
						}
					}
					resp = rpc2.NewPublishResponse(req.ID, params[0].MsgID, nil)
				}
			case rpc2.MethodConsume:
				params, _ := req.ConsumeParams()
				if cfg.ConsumeError {
					resp = rpc2.NewErrorResponse(req.ID, fmt.Errorf("Consume Error"))
				} else if cfg.ConsumeDrop {
					resp = nil
				} else {
					subsMu.Lock()
					for stream, sub := range subs {
						if sub.id != params.SubscriptionID {
							continue
						}
						msgs := make([]rpc2.ConsumeMessage, 0)
						for _, p := range sub.params {
							if p.MsgID == "" {
								continue
							}
							msgs = append(msgs, rpc2.ConsumeMessage{
								MsgID:   p.MsgID,
								Payload: p.Payload,
								Headers: p.Headers,
							})
						}
						// reset the params slice
						sub.params = make([]rpc2.PublishParams, 0)
						resp = rpc2.NewConsumeResponse(req.ID, "", sub.id, stream, msgs)
					}
					subsMu.Unlock()
				}
			}
			if resp != nil {
				err = c.Write(ctx, mt, resp.Bytes())
				assert.NoError(t, err)
			}
		}
	})

	// subscriptions
	r.Route(cfg.SubscriptionsPath, func(r chi.Router) {
		// new subscription
		r.Post("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)

			var req struct {
				GroupID string   `json:"group_id"`
				Streams []string `json:"streams"`
			}
			_ = json.Unmarshal(body, &req)
			t.Logf("Received new subscription request: %+v", req)
			id := uuid.NewString()
			subsMu.Lock()
			subs[req.Streams[0]] = &sub{
				stream: req.Streams[0],
				id:     id,
			}
			subsMu.Unlock()

			resp := struct {
				ID string `json:"_id"`
			}{ID: id}

			err = json.NewEncoder(w).Encode(resp)
			assert.NoError(t, err)
		})

		// delete subscription
		r.Delete("/{id}", func(w http.ResponseWriter, r *http.Request) {
			id := chi.URLParam(r, "id")
			t.Logf("Got delete subscription request: %v", id)

			subsMu.Lock()
			for stream, s := range subs {
				if id == s.id {
					delete(subs, stream)
				}
			}
			subsMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		})
	})

	return httptest.NewTLSServer(r)
}
