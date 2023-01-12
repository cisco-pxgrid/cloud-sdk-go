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
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"

	"github.com/stretchr/testify/require"
)

const (
	objectStorePath = "/unittest/objectstore"
)

func setup() (*httptest.Server, *chi.Mux, Device, error) {
	// Change to http
	defaultHTTPScheme = "http"
	pubsub.HttpScheme = "http"

	// Base routes
	r := chi.NewRouter()
	r.Post(redeemPath, func(w http.ResponseWriter, r *http.Request) {})
	r.Get(getDevicesPath, func(w http.ResponseWriter, r *http.Request) {
		d := getDeviceResponse{ID: "dev1"}
		devices := []getDeviceResponse{d}
		render.JSON(w, r, devices)
	})
	r.Post(objectStorePath, func(w http.ResponseWriter, r *http.Request) {})
	r.Get(objectStorePath, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("object1")) })

	// Test server
	ts := httptest.NewServer(r)

	// Construct config
	url, _ := url.Parse(ts.URL)
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
	app, _ := New(config)
	tenant, _ := app.LinkTenant("otp")
	devices, _ := tenant.GetDevices()
	return ts, r, devices[0], nil
}

func TestEmptyQuery(t *testing.T) {
	ts, r, device, err := setup()
	require.NoError(t, err)
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
	resp, err := device.Query(&http.Request{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestSmallQuery(t *testing.T) {
	ts, r, device, err := setup()
	require.NoError(t, err)
	defer ts.Close()

	queryPath := fmt.Sprintf(directModePath, "dev1", "/query")
	r.Post(queryPath, func(w http.ResponseWriter, r *http.Request) {
		reqEnv := envelop{}
		json.NewDecoder(r.Body).Decode(&reqEnv)
		result := queryResponse{
			Id:     "123",
			Status: "COMPLETE",
			Code:   http.StatusOK,
			Body:   reqEnv.Body,
		}
		render.JSON(w, r, result)
	})
	resp, err := device.Query(&http.Request{Body: io.NopCloser(strings.NewReader("body1"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "body1", string(b))
	resp.Body.Close()
}

func TestLargeRequest(t *testing.T) {
	ts, r, device, err := setup()
	require.NoError(t, err)
	defer ts.Close()

	// Lower max to test logic easier
	RequestBodyMax = 10

	createPath := fmt.Sprintf(directModePath, "dev1", "/query/object")
	r.Post(createPath, func(w http.ResponseWriter, r *http.Request) {
		render.JSON(w, r, createResponse{ObjectUrl: ts.URL + objectStorePath})
	})
	queryPath := fmt.Sprintf(directModePath, "dev1", "/query")
	r.Post(queryPath, func(w http.ResponseWriter, r *http.Request) {
		reqEnv := envelop{}
		json.NewDecoder(r.Body).Decode(&reqEnv)
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
	resp, err := device.Query(&http.Request{Body: io.NopCloser(strings.NewReader("12345678901"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "small response", string(b))
	resp.Body.Close()
}

func TestLargeResponse(t *testing.T) {
	ts, r, device, err := setup()
	require.NoError(t, err)
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
	resp, err := device.Query(&http.Request{Body: io.NopCloser(strings.NewReader("small"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "object1", string(b))
	resp.Body.Close()
}

func TestStatus(t *testing.T) {
	ts, r, device, err := setup()
	require.NoError(t, err)
	defer ts.Close()

	// Lower status poll for quicker test
	StatusPollTimeMin = 100 * time.Millisecond

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
	resp, err := device.Query(&http.Request{Body: io.NopCloser(strings.NewReader("small"))})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "object1", string(b))
	resp.Body.Close()
}
