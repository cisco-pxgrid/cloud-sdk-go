package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	rpc2 "github.com/cisco-pxgrid/cloud-sdk-go/internal/rpc"

	"github.com/cisco-pxgrid/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

type Config struct {
	PubSubPath         string
	SubscriptionsPath  string
	PublishError       bool
	ConsumeError       bool
	ConnHandler        func() int
	ConsumeHandler     func(conn *websocket.Conn, req *rpc2.Request) *rpc2.Response
	QueryHandler       func(w http.ResponseWriter, r *http.Request)
	QueryStatusHandler func(w http.ResponseWriter, r *http.Request)
}

type sub struct {
	stream string
	id     string
	params []rpc2.PublishParams
}

type createMultipartResponse struct {
	ObjectUrls []string `json:"objectUrls"`
	QueryID    string   `json:"queryId"`
}

type getDeviceResponse struct {
	ID         string `json:"deviceId"`
	DeviceInfo struct {
		Kind string `json:"deviceType"`
		Name string `json:"name"`
	} `json:"deviceInfo"`
	MgtInfo struct {
		Region string `json:"region"`
		Fqdn   string `json:"fqdn"`
	} `json:"mgtInfo"`
	Meta struct {
		EnrollmentStatus string `json:"enrollmentStatus"`
	} `json:"meta"`
}

func (s *sub) String() string {
	return fmt.Sprintf("sub{stream:%s, id:%s, params:%+v}", s.stream, s.id, s.params)
}

var subs = map[string]*sub{}
var subsMu = sync.Mutex{}

// NewRPCServer creates and starts a test HTTP server that talks RPC
func NewRPCServer(t *testing.T, cfg Config) (*httptest.Server, *chi.Mux) {
	if cfg.PubSubPath == "" {
		cfg.PubSubPath = "/api/v2/pubsub"
	}
	if cfg.SubscriptionsPath == "" {
		cfg.SubscriptionsPath = "/api/dxhub/v1/registry/subscriptions"
	}
	r := chi.NewRouter()

	// pubsub
	r.Get(cfg.PubSubPath, func(w http.ResponseWriter, r *http.Request) {
		if cfg.ConnHandler != nil {
			status := cfg.ConnHandler()
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
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
				if cfg.ConsumeError {
					resp = rpc2.NewErrorResponse(req.ID, fmt.Errorf("Consume Error"))
				} else if cfg.ConsumeHandler != nil {
					resp = cfg.ConsumeHandler(c, req)
				} else {
					params, _ := req.ConsumeParams()
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

	// Redeem OTP
	r.Post("/idm/api/v1/appregistry/otp/redeem", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse := `{
			"api_token": "token1",
			"tenant_id": "tenant1",
			"tenant_name": "TNT000",
			"assigned_scopes": [
				"App:Scope:1"
			],
			"attributes": {}
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jsonResponse))
	})

	// Unlink tenant
	r.Delete("/idm/api/v1/appregistry/applications/{app_id}/tenants/{tenant_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	r.Put("/unittest/objectstore", func(w http.ResponseWriter, r *http.Request) {})
	r.Get("/unittest/objectstore", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("object1")) })

	r.Post("/api/dxhub/v2/apiproxy/request/{deviceId}/direct/query", func(w http.ResponseWriter, r *http.Request) {
		if cfg.QueryHandler != nil {
			cfg.QueryHandler(w, r)
		}
	})
	r.Get("/api/dxhub/v2/apiproxy/request/{deviceId}/direct/query/{queryId}", func(w http.ResponseWriter, r *http.Request) {
		if cfg.QueryStatusHandler != nil {
			cfg.QueryStatusHandler(w, r)
		}
	})
	ts := httptest.NewTLSServer(r)

	// More routes that require test server url

	r.Post("/api/dxhub/v2/apiproxy/request/{deviceId}/direct/query/object/multipart", func(w http.ResponseWriter, r *http.Request) {
		render.JSON(w, r, createMultipartResponse{ObjectUrls: []string{ts.URL + "/unittest/objectstore"}, QueryID: "testqueryid"})
	})

	// Fetch devices
	r.Get("/api/uno/v1/registry/devices", func(w http.ResponseWriter, r *http.Request) {
		u, _ := url.Parse(ts.URL)
		resp := getDeviceResponse{
			ID: "dev1",
			DeviceInfo: struct {
				Kind string `json:"deviceType"`
				Name string `json:"name"`
			}{
				Kind: "cisco-ise",
				Name: "sjc-511-1",
			},
			MgtInfo: struct {
				Region string `json:"region"`
				Fqdn   string `json:"fqdn"`
			}{
				Region: "us-west-2",
				Fqdn:   u.Host,
			},
			Meta: struct {
				EnrollmentStatus string `json:"enrollmentStatus"`
			}{
				EnrollmentStatus: "enrolled",
			},
		}
		render.JSON(w, r, []getDeviceResponse{resp})
		// _, _ = w.Write([]byte(jsonResponse))
	})
	return ts, r
}
