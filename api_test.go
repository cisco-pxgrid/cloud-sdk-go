package cloud

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cisco-pxgrid/cloud-sdk-go/internal/pubsub"
	"github.com/cisco-pxgrid/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"

	"github.com/stretchr/testify/require"
)

const (
	objectStorePath = "/unittest/objectstore"
	pubsubPath      = "/api/v2/pubsub"
)

func init() {
	// Change to http
	defaultHTTPScheme = "http"
	pubsub.HttpScheme = "http"
	pubsub.WebSocketScheme = "ws"
}

func setupTestserver() (*httptest.Server, *chi.Mux) {

	r := chi.NewRouter()
	r.Post(redeemPath, func(w http.ResponseWriter, r *http.Request) {
		resp := redeemOTPResponse{
			TenantID:   "tenant-001",
			TenantName: "tenant-001",
			Token:      "token-001",
		}
		render.JSON(w, r, resp)
	})
	r.Get(getDevicesPath, func(w http.ResponseWriter, r *http.Request) {
		d := getDeviceResponse{ID: "dev1"}
		devices := []getDeviceResponse{d}
		render.JSON(w, r, devices)
	})
	r.Put(objectStorePath, func(w http.ResponseWriter, r *http.Request) {})
	r.Get(objectStorePath, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("object1")) })
	r.Get(pubsubPath, func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	})

	// Test server
	ts := httptest.NewServer(r)
	return ts, r
}

func setupDevice(tsUrl string) (*Device, error) {
	// Construct config
	url, _ := url.Parse(tsUrl)
	config := Config{
		ID:            "appId",
		RegionalFQDN:  url.Host,
		GlobalFQDN:    url.Host,
		WriteStreamID: "writeStream",
		ReadStreamID:  "readStream",
		GetCredentials: func() (*Credentials, error) {
			return &Credentials{
				ApiKey: []byte("dummy"),
			}, nil
		},
	}
	app, err := New(config)
	if err != nil {
		return nil, err
	}
	tenant, err := app.LinkTenant("otp")
	if err != nil {
		return nil, err
	}
	devices, err := tenant.GetDevices()
	if err != nil {
		return nil, err
	}
	return &devices[0], nil
}

func TestEmptyQuery(t *testing.T) {
	ts, r := setupTestserver()
	defer ts.Close()
	queryPath := fmt.Sprintf(directModePath, "dev1", "/query")
	r.Post(queryPath, func(w http.ResponseWriter, r *http.Request) {
		result := queryResponse{
			Id:     "123",
			Status: "COMPLETE",
			Code:   http.StatusOK,
			Body:   "",
		}
		render.JSON(w, r, result)
	})

	device, err := setupDevice(ts.URL)
	require.NoError(t, err)
	resp, err := device.Query(&http.Request{URL: &url.URL{}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestSmallQuery(t *testing.T) {
	ts, r := setupTestserver()
	defer ts.Close()
	queryPath := fmt.Sprintf(directModePath, "dev1", "/query")
	r.Post(queryPath, func(w http.ResponseWriter, r *http.Request) {
		reqEnv := envelop{}
		_ = json.NewDecoder(r.Body).Decode(&reqEnv)
		result := queryResponse{
			Id:     "123",
			Status: "COMPLETE",
			Code:   http.StatusOK,
			Body:   reqEnv.Body,
		}
		render.JSON(w, r, result)
	})

	device, err := setupDevice(ts.URL)
	require.NoError(t, err)
	resp, err := device.Query(&http.Request{URL: &url.URL{}, Body: io.NopCloser(strings.NewReader("body1"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "body1", string(b))
	resp.Body.Close()
}

func TestLargeRequest(t *testing.T) {
	ts, r := setupTestserver()
	defer ts.Close()
	createPath := fmt.Sprintf(directModePath, "dev1", "/query/object")
	r.Post(createPath, func(w http.ResponseWriter, r *http.Request) {
		render.JSON(w, r, createResponse{ObjectUrls: []string{ts.URL + objectStorePath}})
	})
	queryPath := fmt.Sprintf(directModePath, "dev1", "/query")
	r.Post(queryPath, func(w http.ResponseWriter, r *http.Request) {
		reqEnv := envelop{}
		_ = json.NewDecoder(r.Body).Decode(&reqEnv)
		if reqEnv.ObjectUrl == "" {
			http.Error(w, "Missing ObjectUrl", http.StatusBadRequest)
			return
		}
		result := queryResponse{
			Id:     "123",
			Status: "COMPLETE",
			Code:   http.StatusOK,
			Body:   base64.StdEncoding.EncodeToString([]byte("small response")),
		}
		render.JSON(w, r, result)
	})

	device, err := setupDevice(ts.URL)
	require.NoError(t, err)

	// Lower max to test logic easier
	RequestBodyMax = 10

	resp, err := device.Query(&http.Request{URL: &url.URL{}, Body: io.NopCloser(strings.NewReader("12345678901"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "small response", string(b))
	resp.Body.Close()
}

func TestLargeResponse(t *testing.T) {
	ts, r := setupTestserver()
	defer ts.Close()
	queryPath := fmt.Sprintf(directModePath, "dev1", "/query")
	r.Post(queryPath, func(w http.ResponseWriter, r *http.Request) {
		result := queryResponse{
			Id:        "123",
			Status:    "COMPLETE",
			Code:      http.StatusOK,
			ObjectUrl: ts.URL + objectStorePath,
		}
		render.JSON(w, r, result)
	})

	device, err := setupDevice(ts.URL)
	require.NoError(t, err)
	resp, err := device.Query(&http.Request{URL: &url.URL{}, Body: io.NopCloser(strings.NewReader("small"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "object1", string(b))
	resp.Body.Close()
}

func TestStatus(t *testing.T) {
	ts, r := setupTestserver()
	defer ts.Close()
	queryPath := fmt.Sprintf(directModePath, "dev1", "/query")
	r.Post(queryPath, func(w http.ResponseWriter, r *http.Request) {
		render.JSON(w, r, queryResponse{Id: "query1", Status: "RUNNING"})
	})
	count := 0
	query1Path := fmt.Sprintf(directModePath, "dev1", "/query/query1")
	r.Get(query1Path, func(w http.ResponseWriter, r *http.Request) {
		var result queryResponse
		if count < 2 {
			result = queryResponse{Id: "query1", Status: "RUNNING"}
		} else {
			result = queryResponse{
				Id:        "query1",
				Status:    "COMPLETE",
				Code:      http.StatusOK,
				ObjectUrl: ts.URL + objectStorePath,
			}
		}
		count++
		render.JSON(w, r, result)
	})

	device, err := setupDevice(ts.URL)
	require.NoError(t, err)

	// Lower status poll for quicker test
	StatusPollTimeMin = 100 * time.Millisecond

	resp, err := device.Query(&http.Request{URL: &url.URL{}, Body: io.NopCloser(strings.NewReader("small"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "object1", string(b))
	resp.Body.Close()
}

func TestFallbackQuery(t *testing.T) {
	ts, r := setupTestserver()
	defer ts.Close()
	queryPath := fmt.Sprintf(directModePath, "dev1", "/api/get")
	r.Get(queryPath, func(w http.ResponseWriter, r *http.Request) {})

	device, err := setupDevice(ts.URL)
	require.NoError(t, err)
	u, _ := url.Parse("/api/get")
	resp, err := device.Query(&http.Request{Method: http.MethodGet, URL: u})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestFallbackTooLargeQuery(t *testing.T) {
	ts, r := setupTestserver()
	defer ts.Close()
	queryPath := fmt.Sprintf(directModePath, "dev1", "/api/get")
	r.Get(queryPath, func(w http.ResponseWriter, r *http.Request) {})

	device, err := setupDevice(ts.URL)
	require.NoError(t, err)

	// Lower max to test logic easier
	RequestBodyMax = 10

	u, _ := url.Parse("/api/get")
	_, err = device.Query(&http.Request{
		Method: http.MethodGet,
		URL:    u,
		Body:   io.NopCloser(strings.NewReader("12345678901"))})
	require.Error(t, err)
}
